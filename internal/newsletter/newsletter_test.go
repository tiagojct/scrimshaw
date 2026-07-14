package newsletter

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-imap"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// bytesLiteral implements imap.Literal (io.Reader + Len()) over a raw RFC822
// message, so importMessage can be tested against real MIME parsing without
// a live IMAP server.
type bytesLiteral struct{ *bytes.Reader }

func (b bytesLiteral) Len() int { return b.Reader.Len() }

func newTestMessage(t *testing.T, uid uint32, raw string) (*imap.Message, *imap.BodySectionName) {
	t.Helper()
	section, err := imap.ParseBodySectionName(imap.FetchItem("BODY.PEEK[]"))
	if err != nil {
		t.Fatal(err)
	}
	// (*Message).GetBody strips the PEEK marker from the section it's asked
	// for before comparing (matching how a real server echoes FETCH
	// responses without it), so the stored map key must be the no-PEEK form
	// — imap.FetchBody, i.e. "BODY[]" — not the section requested with it.
	respSection, err := imap.ParseBodySectionName(imap.FetchBody + "[]")
	if err != nil {
		t.Fatal(err)
	}
	msg := &imap.Message{
		Uid:  uid,
		Body: map[*imap.BodySectionName]imap.Literal{respSection: bytesLiteral{bytes.NewReader([]byte(raw))}},
	}
	return msg, section
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Service{Store: s, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
}

const htmlEmail = "From: Jane Doe <jane@example.test>\r\n" +
	"Subject: Weekly Newsletter\r\n" +
	"Message-Id: <abc123@example.test>\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>Hello <strong>world</strong></p><script>alert(1)</script></body></html>\r\n"

const textEmail = "From: plain@example.test\r\n" +
	"Subject: Plain Text Update\r\n" +
	"Message-Id: <plain456@example.test>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Just plain text, with <a tag-looking thing> in it.\r\n"

func TestImportMessageHTMLBody(t *testing.T) {
	svc := newTestService(t)
	msg, section := newTestMessage(t, 1, htmlEmail)
	if err := svc.importMessage(context.Background(), msg, section); err != nil {
		t.Fatal(err)
	}
	// mail.Header.MessageID() strips the RFC 5322 angle-bracket delimiters,
	// so the stored key is the bare id, not "<abc123@example.test>".
	id, err := svc.Store.ItemIDByURL(context.Background(), "https://newsletter.invalid/"+url.PathEscape("abc123@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Weekly Newsletter" {
		t.Fatalf("title = %q", item.Title)
	}
	if item.Author != "Jane Doe" {
		t.Fatalf("author = %q", item.Author)
	}
	if !item.ReadLater {
		t.Fatal("newsletter items should be filed as read later")
	}
	if strings.Contains(item.ExtractedText, "<script") {
		t.Fatalf("email HTML must be sanitized: %q", item.ExtractedText)
	}
	if !strings.Contains(item.ExtractedText, "<strong>world</strong>") {
		t.Fatalf("safe HTML should be preserved: %q", item.ExtractedText)
	}
}

func TestImportMessagePlainTextBody(t *testing.T) {
	svc := newTestService(t)
	msg, section := newTestMessage(t, 2, textEmail)
	if err := svc.importMessage(context.Background(), msg, section); err != nil {
		t.Fatal(err)
	}
	id, err := svc.Store.ItemIDByURL(context.Background(), "https://newsletter.invalid/"+url.PathEscape("plain456@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Plain Text Update" {
		t.Fatalf("title = %q", item.Title)
	}
	// A stray "<a tag-looking thing>" in plain text must never become live
	// markup once wrapped in <p> and sanitized.
	if strings.Contains(item.ExtractedText, "<a tag-looking") {
		t.Fatalf("plain text must be escaped before wrapping in HTML: %q", item.ExtractedText)
	}
	if !strings.Contains(item.ExtractedText, "&lt;a tag-looking thing&gt;") {
		t.Fatalf("expected the escaped form to survive sanitization: %q", item.ExtractedText)
	}
}

func TestImportMessageDedupesByMessageID(t *testing.T) {
	svc := newTestService(t)
	msg1, section1 := newTestMessage(t, 1, htmlEmail)
	if err := svc.importMessage(context.Background(), msg1, section1); err != nil {
		t.Fatal(err)
	}
	// A redelivered copy of the same message (different UID, same Message-Id)
	// must dedupe cleanly, not error.
	msg2, section2 := newTestMessage(t, 99, htmlEmail)
	if err := svc.importMessage(context.Background(), msg2, section2); err != nil {
		t.Fatalf("redelivered message should dedupe silently, got error: %v", err)
	}
	items, err := svc.Store.AllItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly one item after a duplicate import, got %d", len(items))
	}
}

func TestImportMessageRejectsAnEmptyBody(t *testing.T) {
	svc := newTestService(t)
	raw := "From: nobody@example.test\r\nSubject: Empty\r\nMessage-Id: <empty@example.test>\r\nContent-Type: text/plain\r\n\r\n"
	msg, section := newTestMessage(t, 1, raw)
	err := svc.importMessage(context.Background(), msg, section)
	if err == nil {
		t.Fatal("an email with no body content should fail to import")
	}
}

func TestImportMessageMissingBodyErrors(t *testing.T) {
	svc := newTestService(t)
	section, err := imap.ParseBodySectionName(imap.FetchItem("BODY.PEEK[]"))
	if err != nil {
		t.Fatal(err)
	}
	msg := &imap.Message{Uid: 1} // no Body entry at all
	if err := svc.importMessage(context.Background(), msg, section); err == nil {
		t.Fatal("a message with no fetched body should error, not panic")
	}
}
