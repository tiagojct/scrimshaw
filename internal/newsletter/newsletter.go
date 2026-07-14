// Package newsletter polls an external IMAP mailbox and converts unread
// emails into read-later items — a "kill-the-newsletter"-style bridge for
// the meaningful share of read-later material that arrives by email, not
// RSS. Deliberately built as an outbound poller, not an inbound SMTP
// receiver: running your own SMTP server would mean an open port, MX
// records, and permanent spam/abuse exposure disproportionate to a
// single-user personal reader. Point a mailbox you already control at it
// instead — a dedicated address, an alias, or a filtered label.
package newsletter

import (
	"context"
	"errors"
	stdhtml "html"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"

	"github.com/tiagojct/scrimshaw/internal/sanitize"
	"github.com/tiagojct/scrimshaw/internal/store"
)

// Service polls one IMAP mailbox on an interval.
type Service struct {
	// Host is "host:port" (e.g. "imap.gmail.com:993"); the connection is
	// always implicit TLS (IMAPS), matching what every mainstream provider
	// requires — there's no plaintext or STARTTLS fallback.
	Host, User, Password, Folder string
	Store                        *store.Store
	Logger                       *slog.Logger
}

// RunOnce connects, imports unread messages as read-later items, and marks
// every message it looked at \Seen — successes and failures alike — so one
// malformed email can't block every future poll forever; a failure is
// logged and effectively skipped after this one attempt.
func (s *Service) RunOnce(ctx context.Context) error {
	folder := s.Folder
	if folder == "" {
		folder = "INBOX"
	}
	c, err := client.DialTLS(s.Host, nil)
	if err != nil {
		return errors.New("connect: " + err.Error())
	}
	defer c.Logout()
	if err := c.Login(s.User, s.Password); err != nil {
		return errors.New("login: " + err.Error())
	}
	if _, err := c.Select(folder, false); err != nil {
		return errors.New("select " + folder + ": " + err.Error())
	}

	uids, err := c.UidSearch(&imap.SearchCriteria{WithoutFlags: []string{imap.SeenFlag}})
	if err != nil {
		return errors.New("search: " + err.Error())
	}
	if len(uids) == 0 {
		return nil
	}

	// BODY.PEEK[] fetches the full message without implicitly setting
	// \Seen (plain BODY[]/RFC822 would) — \Seen is set explicitly below,
	// once, after every message in this batch has been attempted.
	section, err := imap.ParseBodySectionName(imap.FetchItem("BODY.PEEK[]"))
	if err != nil {
		return err
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uids...)
	messages := make(chan *imap.Message, len(uids))
	fetchErr := make(chan error, 1)
	go func() {
		fetchErr <- c.UidFetch(seqset, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, messages)
	}()

	imported := 0
	for msg := range messages {
		if err := s.importMessage(ctx, msg, section); err != nil {
			s.Logger.Warn("import newsletter email failed", "uid", msg.Uid, "error", err)
			continue
		}
		imported++
	}
	if err := <-fetchErr; err != nil {
		return errors.New("fetch: " + err.Error())
	}
	if imported > 0 {
		s.Logger.Info("newsletter import", "count", imported, "checked", len(uids))
	}

	return c.UidStore(seqset, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag}, nil)
}

func (s *Service) importMessage(ctx context.Context, msg *imap.Message, section *imap.BodySectionName) error {
	body := msg.GetBody(section)
	if body == nil {
		return errors.New("message has no body")
	}
	mr, err := mail.CreateReader(body)
	if err != nil {
		return errors.New("parse message: " + err.Error())
	}
	subject, _ := mr.Header.Subject()
	messageID, _ := mr.Header.MessageID()
	from := ""
	if addrs, err := mr.Header.AddressList("From"); err == nil && len(addrs) > 0 {
		from = addrs[0].Name
		if from == "" {
			from = addrs[0].Address
		}
	}

	var htmlBody, textBody string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("parse message part: " + err.Error())
		}
		inline, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue // an attachment, not a body part
		}
		contentType, _, _ := inline.ContentType()
		b, err := io.ReadAll(p.Body)
		if err != nil {
			continue
		}
		switch contentType {
		case "text/html":
			if htmlBody == "" {
				htmlBody = string(b)
			}
		case "text/plain":
			if textBody == "" {
				textBody = string(b)
			}
		}
	}
	if htmlBody == "" && textBody == "" {
		return errors.New("email has no text or HTML body")
	}
	content := htmlBody
	if content == "" {
		content = "<p>" + stdhtml.EscapeString(textBody) + "</p>"
	}
	content = sanitize.HTML(content)

	if subject == "" {
		subject = from
	}
	if subject == "" {
		subject = "Untitled email"
	}

	// Emails have no real URL, but the item model and dedup both assume one.
	// items.canonical_url must be http(s) (store.CanonicalURL rejects other
	// schemes), so this uses the reserved .invalid TLD (RFC 2606 — guaranteed
	// to never resolve) keyed on Message-ID, which is the correct unique
	// identifier for an email and dedups a redelivered message correctly.
	// The reader's "open original" link will not go anywhere for these items
	// — there is no original page, only the email content itself.
	key := messageID
	if key == "" {
		key = "uid-" + strconv.FormatUint(uint64(msg.Uid), 10)
	}
	rawURL := "https://newsletter.invalid/" + url.PathEscape(key)

	_, err = s.Store.InsertManualItem(ctx, rawURL, subject, from, "", content, nil, true)
	if errors.Is(err, store.ErrItemExists) {
		return nil // already imported (e.g. a redelivered message), not a failure
	}
	return err
}

// Run polls on a ticker, importing immediately then every interval, until
// ctx is canceled — the same pattern as feeds.Service.Run/links.Checker.Run.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("newsletter poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
