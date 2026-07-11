package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	exporter "github.com/tiagojct/scrimshaw/internal/export"
	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/importers"
	"github.com/tiagojct/scrimshaw/internal/reader"
	"github.com/tiagojct/scrimshaw/internal/sanitize"
	"github.com/tiagojct/scrimshaw/internal/server/static"
	"github.com/tiagojct/scrimshaw/internal/store"
	"github.com/tiagojct/scrimshaw/web"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	SessionSecret []byte
	CookieSecure  bool
	Saver         *reader.Saver
	SnapshotsDir  string
	ImageCacheDir string
	Fetcher       *fetch.Client
	ExportDir     string
}

type Server struct {
	store *store.Store
	log   *slog.Logger
	cfg   Config
}

func New(s *store.Store, logger *slog.Logger, cfg Config) *Server {
	return &Server{store: s, log: logger, cfg: cfg}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)
	mux.HandleFunc("GET /service-worker.js", s.serviceWorker)
	mux.HandleFunc("GET /app.css", s.asset("app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", s.asset("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /setup", s.setup)
	mux.HandleFunc("POST /setup", s.setup)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.withSession(s.logout))
	mux.HandleFunc("GET /", s.withSession(s.index))
	mux.HandleFunc("GET /items/{id}", s.withSession(s.reader))
	mux.HandleFunc("GET /items/{id}/snapshot", s.withSession(s.snapshot))
	mux.HandleFunc("POST /items/{id}/read", s.withSession(s.setReadState))
	mux.HandleFunc("POST /items/{id}/archive", s.withSession(s.archive))
	mux.HandleFunc("POST /items/{id}/star", s.withSession(s.star))
	mux.HandleFunc("POST /items/{id}/share", s.withSession(s.shareItem))
	mux.HandleFunc("POST /items/{id}/readlater", s.withSession(s.readLaterItem))
	mux.HandleFunc("POST /items/{id}/bookmark", s.withSession(s.bookmarkItem))
	mux.HandleFunc("POST /items/{id}/highlights", s.withSession(s.highlight))
	mux.HandleFunc("POST /items/bulk", s.withSession(s.bulk))
	mux.HandleFunc("GET /feeds/new", s.withSession(s.newFeed))
	mux.HandleFunc("POST /feeds", s.withSession(s.createFeed))
	mux.HandleFunc("GET /save", s.withSession(s.newSave))
	mux.HandleFunc("POST /save", s.withSession(s.saveURL))
	mux.HandleFunc("GET /share", s.withSession(s.share))
	mux.HandleFunc("GET /settings", s.withSession(s.settings))
	mux.HandleFunc("GET /tokens", s.withSession(s.tokens))
	mux.HandleFunc("POST /tokens", s.withSession(s.createToken))
	mux.HandleFunc("POST /api/save", s.apiSave)
	mux.HandleFunc("OPTIONS /api/save", s.apiOptions)
	mux.HandleFunc("GET /api/items", s.apiItems)
	mux.HandleFunc("GET /api/shared", s.apiShared)
	mux.HandleFunc("GET /api/feeds", s.apiFeeds)
	mux.HandleFunc("GET /api/search", s.apiSearch)
	mux.HandleFunc("GET /api/highlights", s.apiHighlights)
	mux.HandleFunc("POST /api/items/{id}/read", s.apiMarkRead)
	mux.HandleFunc("OPTIONS /api/items/{id}/read", s.apiOptions)
	mux.HandleFunc("POST /api/items/{id}/highlights", s.apiAddHighlight)
	mux.HandleFunc("OPTIONS /api/items/{id}/highlights", s.apiOptions)
	mux.HandleFunc("GET /export.json", s.withSession(s.exportJSON))
	mux.HandleFunc("GET /export.opml", s.withSession(s.exportOPML))
	mux.HandleFunc("POST /export/markdown", s.withSession(s.exportMarkdown))
	mux.HandleFunc("GET /import/netscape", s.withSession(s.netscapeImportForm))
	mux.HandleFunc("POST /import/netscape", s.withSession(s.netscapeImport))
	mux.HandleFunc("GET /import", s.withSession(s.importForm))
	mux.HandleFunc("POST /import", s.withSession(s.importFile))
	mux.HandleFunc("GET /search", s.withSession(s.search))
	mux.HandleFunc("GET /highlights", s.withSession(s.highlights))
	mux.HandleFunc("GET /images", s.withSession(s.image))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	_, err := s.store.UserPasswordHash(r.Context())
	if err == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.internalError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		s.render(w, "Setup", `<form method="post"><label>Password <input type="password" name="password" required minlength="12" autofocus></label><button>Create account</button></form>`, "")
		return
	}
	password := r.FormValue("password")
	if len(password) < 12 {
		s.render(w, "Setup", "", "Password must contain at least 12 characters.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.CreateUser(r.Context(), string(hash)); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, "Login", `<form method="post"><label>Password <input type="password" name="password" required autofocus></label><button>Log in</button></form>`, "")
		return
	}
	address, _, _ := net.SplitHostPort(r.RemoteAddr)
	allowed, err := s.store.LoginAllowed(r.Context(), address)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !allowed {
		s.render(w, "Login", "", "Too many login attempts. Try again later.")
		return
	}
	hash, err := s.store.UserPasswordHash(r.Context())
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("password"))) != nil {
		_ = s.store.RecordLoginFailure(r.Context(), address)
		s.render(w, "Login", "", "Invalid credentials.")
		return
	}
	if err := s.store.ClearLoginFailures(r.Context(), address); err != nil {
		s.internalError(w, err)
		return
	}
	id, err := randomToken(32)
	if err != nil {
		s.internalError(w, err)
		return
	}
	csrf, err := randomToken(32)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.CreateSession(r.Context(), id, csrf, time.Now().Add(24*time.Hour)); err != nil {
		s.internalError(w, err)
		return
	}
	s.setSessionCookie(w, id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) withSession(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.sessionID(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		csrf, err := s.store.SessionCSRF(r.Context(), id)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !hmac.Equal([]byte(csrf), []byte(r.FormValue("csrf_token"))) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), csrfKey{}, csrf)), id)
	}
}

type csrfKey struct{}

func csrf(r *http.Request) string { value, _ := r.Context().Value(csrfKey{}).(string); return value }

func (s *Server) logout(w http.ResponseWriter, r *http.Request, session string) {
	_ = s.store.DeleteSession(r.Context(), session)
	http.SetCookie(w, &http.Cookie{Name: "scrimshaw_session", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
func (s *Server) index(w http.ResponseWriter, r *http.Request, _ string) {
	// Bare "/" is the dashboard; every list view carries an explicit ?view=.
	if r.URL.Query().Get("view") == "" {
		s.dashboard(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	view := r.URL.Query().Get("view")
	sort := r.URL.Query().Get("sort")
	tag := r.URL.Query().Get("tag")
	v, ok := viewsByKey[view]
	if !ok {
		v = viewsByKey["feeds"]
	}
	options := store.ListOptions{
		Tag: tag, State: v.state, Source: v.source, ReadLater: v.readLater, Bookmarked: v.bookmarked,
		IncludeArchived: v.includeArchived, Sort: sort,
		Page: page, PerPage: 50,
	}
	items, total, err := s.store.ListPage(r.Context(), options)
	if err != nil {
		s.internalError(w, err)
		return
	}
	tagCounts, err := s.store.UnreadTagCounts(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}

	// Preserve the active view (and sort) across pagination and tag filters.
	link := func(extra url.Values) string {
		q := url.Values{"view": {v.key}}
		if sort != "" {
			q.Set("sort", sort)
		}
		for key, values := range extra {
			q[key] = values
		}
		return "/?" + q.Encode()
	}

	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<nav class="views">`)
	for _, item := range viewOrder {
		b.WriteString(viewTab("/?view="+item.key, item.label, item.key, v.key))
	}
	b.WriteString(`</nav>`)
	fmt.Fprintf(&b, `<div class="filters"><form action="/search"><label>Search <input name="q" type="search" placeholder="Search everything"></label><button>Search</button></form><form action="/"><input type="hidden" name="view" value="%s"><label>Sort <select name="sort">%s%s%s</select></label><button>Apply</button></form></div>`,
		template.HTMLEscapeString(v.key),
		optionTag("", "Newest", sort), optionTag("oldest", "Oldest", sort), optionTag("unread", "Unread first", sort))
	if tag != "" {
		fmt.Fprintf(&b, `<p class="tagbar">Tag: %s &middot; <a href="%s">clear</a></p>`, template.HTMLEscapeString(tag), link(nil))
	} else if len(tagCounts) > 0 {
		b.WriteString(`<p class="tagbar">Unread tags: `)
		for _, count := range tagCounts {
			fmt.Fprintf(&b, `<a href="/?view=all&tag=%s">%s (%d)</a>`, url.QueryEscape(count.Name), template.HTMLEscapeString(count.Name), count.Count)
		}
		b.WriteString(`</p>`)
	}
	if len(items) == 0 {
		fmt.Fprintf(&b, `<p class="note">%s</p>`, v.empty)
	}
	// Bold-for-unread only reads as meaningful in the reading queues; in the
	// permanent collections every item renders the same.
	listClass := "items"
	if v.key == "feeds" || v.key == "later" {
		listClass = "items queue"
	}
	fmt.Fprintf(&b, `<form method="post" action="/items/bulk"><input type="hidden" name="csrf_token" value="%s"><ul class="%s">`, template.HTMLEscapeString(csrf(r)), listClass)
	for _, item := range items {
		classes := template.HTMLEscapeString(item.ReadState)
		if item.Starred {
			classes += " starred"
		}
		badges := ""
		if v.showSource {
			badges += itemKindBadges(item)
		}
		if item.Starred {
			badges += ` <span class="badge star">Starred</span>`
		}
		if item.Shared {
			badges += ` <span class="badge shared">Shared</span>`
		}
		if linkBroken(item) {
			badges += ` <span class="badge broken">Broken link</span>`
		}
		meta := ""
		if item.Author != "" {
			meta += `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
		}
		if rel := relativeTime(item.AddedAt); rel != "" {
			meta += `<time>` + rel + `</time>`
		}
		meta += badges
		fmt.Fprintf(&b, `<li class="%s"><input type="checkbox" name="item" value="%d" aria-label="Select item"><div class="item-main"><a href="/items/%d">%s</a><div class="item-meta">%s</div></div></li>`,
			classes, item.ID, item.ID, template.HTMLEscapeString(item.Title), meta)
	}
	b.WriteString(`</ul><div class="bulk-actions"><button name="action" value="read">Mark selected read</button><button name="action" value="archive">Archive selected</button></div></form>`)
	if options.Page > 1 || options.Page*options.PerPage < total {
		b.WriteString(`<div class="pager">`)
		if options.Page > 1 {
			fmt.Fprintf(&b, `<a href="%s">Previous</a>`, link(url.Values{"page": {strconv.Itoa(options.Page - 1)}}))
		}
		if options.Page*options.PerPage < total {
			fmt.Fprintf(&b, `<a href="%s">Next</a>`, link(url.Values{"page": {strconv.Itoa(options.Page + 1)}}))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf(r)) + `"><button>Log out</button></form>`)
	s.render(w, v.label, b.String(), "")
}

const dashboardToolbar = `<nav class="toolbar"><a href="/save">Add a link</a><a href="/feeds/new">Add a feed</a><a href="/highlights">Highlights</a><a href="/settings">Settings</a></nav>`

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, _ ...string) {
	st, err := s.store.Stats(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	later, _, err := s.store.ListPage(r.Context(), store.ListOptions{ReadLater: "1", State: "unread", PerPage: 5})
	if err != nil {
		s.internalError(w, err)
		return
	}
	bookmarks, _, err := s.store.ListPage(r.Context(), store.ListOptions{Bookmarked: "1", IncludeArchived: true, PerPage: 5})
	if err != nil {
		s.internalError(w, err)
		return
	}
	feedItems, _, err := s.store.ListPage(r.Context(), store.ListOptions{Source: "feed", State: "unread", PerPage: 5})
	if err != nil {
		s.internalError(w, err)
		return
	}

	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Dashboard</h1><div class="stat-grid">`)
	stat := func(href string, value int, label string) {
		fmt.Fprintf(&b, `<a class="stat" href="%s"><span class="stat-value">%d</span><span class="stat-label">%s</span></a>`, href, value, label)
	}
	stat("/?view=feeds&sort=unread", st.UnreadFeeds, "Unread in feeds")
	stat("/?view=later", st.ReadLaterUnread, "To read")
	stat("/?view=bookmarks", st.Bookmarks, "Bookmarks")
	stat("/?view=starred", st.Starred, "Starred")
	stat("/highlights", st.Highlights, "Highlights")
	if st.BrokenLinks > 0 {
		stat("/?view=bookmarks", st.BrokenLinks, "Broken links")
	}
	b.WriteString(`</div>`)

	section := func(title, href, moreLabel string, items []store.Item) {
		fmt.Fprintf(&b, `<section class="dash-section"><h2><a href="%s">%s</a></h2>`, href, title)
		if len(items) == 0 {
			b.WriteString(`<p class="note">Nothing here yet.</p></section>`)
			return
		}
		b.WriteString(`<ul class="items">`)
		for _, item := range items {
			meta := ""
			if item.Author != "" {
				meta += `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
			}
			if rel := relativeTime(item.AddedAt); rel != "" {
				meta += `<time>` + rel + `</time>`
			}
			if linkBroken(item) {
				meta += ` <span class="badge broken">Broken link</span>`
			}
			fmt.Fprintf(&b, `<li><div class="item-main"><a href="/items/%d">%s</a><div class="item-meta">%s</div></div></li>`,
				item.ID, template.HTMLEscapeString(item.Title), meta)
		}
		fmt.Fprintf(&b, `</ul><p class="note"><a href="%s">%s</a></p></section>`, href, moreLabel)
	}
	section("To read", "/?view=later", "All read-later", later)
	section("Recent bookmarks", "/?view=bookmarks", "All bookmarks", bookmarks)
	section("Unread in feeds", "/?view=feeds&sort=unread", "All feeds", feedItems)

	b.WriteString(`<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf(r)) + `"><button>Log out</button></form>`)
	s.render(w, "Dashboard", b.String(), "")
}

// itemView describes one tab over the shared items table.
type itemView struct {
	key, label, source, state, readLater, bookmarked, empty string
	showSource, includeArchived                             bool
}

var viewOrder = []itemView{
	{key: "feeds", label: "Feeds", source: "feed", empty: "No feed items yet. Subscribe to a feed to start."},
	{key: "later", label: "Read Later", readLater: "1", empty: "Nothing to read yet. Add a link, or send a feed item here with Read later.", showSource: true},
	{key: "bookmarks", label: "Bookmarks", bookmarked: "1", includeArchived: true, empty: "No bookmarks yet. Add a link, or bookmark a feed item.", showSource: true},
	{key: "starred", label: "Starred", state: "starred", empty: "No starred items yet.", showSource: true},
	{key: "archived", label: "Archived", state: "archived", empty: "Nothing archived. Reading an item files it here.", showSource: true},
	{key: "all", label: "All", empty: "Nothing here yet.", showSource: true},
}

var viewsByKey = func() map[string]itemView {
	m := make(map[string]itemView, len(viewOrder))
	for _, v := range viewOrder {
		m[v.key] = v
	}
	return m
}()

func (s *Server) reader(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	item, err := s.store.Item(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	snapshotLink := ""
	if item.SnapshotPath.Valid {
		snapshotLink = fmt.Sprintf(` &middot; <a href="/items/%d/snapshot">Open offline snapshot</a>`, item.ID)
	}
	highlights, err := s.store.HighlightsForItem(r.Context(), item.ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	token := template.HTMLEscapeString(csrf(r))
	quotes := make([]string, 0, len(highlights))
	for _, h := range highlights {
		quotes = append(quotes, h.Quote)
	}
	quotesJSON, _ := json.Marshal(quotes)

	var badges strings.Builder
	if item.Author != "" {
		fmt.Fprintf(&badges, ` &middot; %s`, template.HTMLEscapeString(item.Author))
	}
	badges.WriteString(itemKindBadges(item))
	if item.Starred {
		badges.WriteString(` <span class="badge star">Starred</span>`)
	}
	if item.Shared {
		badges.WriteString(` <span class="badge shared">Shared</span>`)
	}
	if item.Archived {
		badges.WriteString(` <span class="badge">Archived</span>`)
	}
	if linkBroken(item) {
		badges.WriteString(` <span class="badge broken">Broken link</span>`)
	}

	var dates strings.Builder
	fmt.Fprintf(&dates, `Added %s`, item.AddedAt.Format("Jan 2, 2006"))
	if d := shortDate(item.PublishedAt); d != "" {
		fmt.Fprintf(&dates, ` &middot; Published %s`, d)
	}
	if d := shortDate(item.ReadAt); d != "" {
		fmt.Fprintf(&dates, ` &middot; Read %s`, d)
	}

	content := fmt.Sprintf(`<div class="reader">%s</div>`, sanitize.HTML(item.ExtractedText))
	if item.ExtractedText == "" {
		content = fmt.Sprintf(`<div class="reader"><p class="note">This is a stored link. Use Read later below to fetch the article for reading.</p><p><a href="%s" rel="noopener noreferrer">%s</a></p></div>`,
			template.HTMLEscapeString(item.URL), template.HTMLEscapeString(item.URL))
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<article data-item-id="%d"><h1>%s</h1><p class="meta"><a class="original-link" href="%s" rel="noopener noreferrer">Open original</a>%s%s</p><p class="meta dates">%s</p>%s</article>`,
		item.ID, template.HTMLEscapeString(item.Title), template.HTMLEscapeString(item.URL), snapshotLink, badges.String(), dates.String(), content)

	// Return to the list the item belongs to after it is filed away.
	backURL := "/?view=feeds"
	switch {
	case item.ReadLater:
		backURL = "/?view=later"
	case item.Bookmarked:
		backURL = "/?view=bookmarks"
	}
	starValue, starLabel := "1", "Star"
	if item.Starred {
		starValue, starLabel = "0", "Starred"
	}
	shareValue, shareLabel := "1", "Share"
	if item.Shared {
		shareValue, shareLabel = "0", "Shared"
	}
	laterValue, laterLabel := "1", "Read later"
	if item.ReadLater {
		laterValue, laterLabel = "0", "In reading list"
	}
	bookmarkValue, bookmarkLabel := "1", "Bookmark"
	if item.Bookmarked {
		bookmarkValue, bookmarkLabel = "0", "Bookmarked"
	}
	b.WriteString(`<div class="reader-actions">`)
	// Reading files an item into Archived; "Move to inbox" reverses it.
	if item.Archived {
		fmt.Fprintf(&b, `<form class="read-form" method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="unread"><button class="primary">Move to inbox</button></form>`, item.ID, token)
	} else {
		fmt.Fprintf(&b, `<form class="read-form" method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="read"><input type="hidden" name="return" value="%s"><button class="primary">Mark read</button></form>`, item.ID, token, template.HTMLEscapeString(backURL))
	}
	fmt.Fprintf(&b, `<form class="star-form" method="post" action="/items/%d/star"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="starred" value="%s"><button%s>%s</button></form>`, item.ID, token, starValue, starButtonAttr(item.Starred), starLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/readlater"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="read_later" value="%s"><button%s>%s</button></form>`, item.ID, token, laterValue, starButtonAttr(item.ReadLater), laterLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/bookmark"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="bookmarked" value="%s"><button%s>%s</button></form>`, item.ID, token, bookmarkValue, starButtonAttr(item.Bookmarked), bookmarkLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/share"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="shared" value="%s"><button%s>%s</button></form>`, item.ID, token, shareValue, starButtonAttr(item.Shared), shareLabel)
	b.WriteString(`</div>`)
	// Selection popover and the hidden form it submits to create a highlight.
	fmt.Fprintf(&b, `<div class="hl-pop" id="hl-pop"><button type="button">Highlight</button></div><form id="hl-form" method="post" action="/items/%d/highlights"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" id="hl-quote" name="quote"><input type="hidden" name="note" value=""></form>`, item.ID, token)
	b.WriteString(`<section class="highlight-list"><h2>Highlights</h2>`)
	if len(highlights) > 0 {
		b.WriteString(`<ul>`)
		for _, h := range highlights {
			fmt.Fprintf(&b, `<li><q>%s</q>`, template.HTMLEscapeString(h.Quote))
			if h.Note != "" {
				fmt.Fprintf(&b, `<p class="note">%s</p>`, template.HTMLEscapeString(h.Note))
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ul>`)
	} else {
		b.WriteString(`<p class="note">Select any passage in the article to highlight it.</p>`)
	}
	fmt.Fprintf(&b, `<details class="manual-highlight"><summary>Add a highlight manually</summary><form method="post" action="/items/%d/highlights"><input type="hidden" name="csrf_token" value="%s"><label>Highlight <input name="quote" required></label><label>Note <input name="note"></label><button>Add highlight</button></form></details></section>`, item.ID, token)
	fmt.Fprintf(&b, `<script type="application/json" id="hl-data">%s</script>`, quotesJSON)
	s.render(w, item.Title, b.String(), "")
}
func (s *Server) setReadState(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state := r.FormValue("state")
	if err = s.store.SetReadState(r.Context(), id, state); err != nil {
		s.internalError(w, err)
		return
	}
	// Marking read files the item away; go back to its list. Unread returns to
	// the item so it can be re-read.
	if state == "read" {
		http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
func (s *Server) archive(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	archived := r.FormValue("archived") != "0"
	if err = s.store.SetArchived(r.Context(), id, archived); err != nil {
		s.internalError(w, err)
		return
	}
	if archived {
		http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// safeReturn accepts only same-origin absolute paths, guarding against open
// redirects via an attacker-supplied return value.
func safeReturn(dest string) string {
	if strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//") {
		return dest
	}
	return "/"
}
func (s *Server) star(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetStarred(r.Context(), id, r.FormValue("starred") != "0"); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
func (s *Server) shareItem(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetShared(r.Context(), id, r.FormValue("shared") != "0"); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// readLaterItem promotes a stored link to Read Later (fetching the article if it
// has none yet) or demotes a read-later item back to a bookmark.
func (s *Server) readLaterItem(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	readLater := r.FormValue("read_later") != "0"
	if err := s.store.SetReadLater(r.Context(), id, readLater); err != nil {
		s.internalError(w, err)
		return
	}
	if readLater && s.cfg.Saver != nil {
		item, err := s.store.Item(r.Context(), id)
		if err == nil && item.ExtractedText == "" {
			if err := s.cfg.Saver.Extract(r.Context(), id, item.URL); err != nil {
				s.log.Warn("promote to read later: extract failed", "item", id, "error", err)
			}
		}
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) bookmarkItem(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetBookmarked(r.Context(), id, r.FormValue("bookmarked") != "0"); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
func (s *Server) highlight(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.AddHighlight(r.Context(), id, r.FormValue("quote"), r.FormValue("note"), 0); err != nil {
		s.render(w, "Highlight", "", "A highlight needs text.")
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	item, err := s.store.Item(r.Context(), id)
	if err != nil || !item.SnapshotPath.Valid || filepath.Dir(item.SnapshotPath.String) != filepath.Clean(s.cfg.SnapshotsDir) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(item.SnapshotPath.String)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	http.ServeContent(w, r, filepath.Base(item.SnapshotPath.String), info.ModTime(), file)
}
func (s *Server) search(w http.ResponseWriter, r *http.Request, _ string) {
	query := r.URL.Query().Get("q")
	items, err := s.store.Search(r.Context(), query)
	if err != nil {
		s.render(w, "Search", "", "Search query is invalid.")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="filters"><form action="/search"><label>Search <input name="q" type="search" value="%s" autofocus></label><button>Search</button></form></div><ul class="items">`, template.HTMLEscapeString(query))
	for _, item := range items {
		meta := ""
		if item.Author != "" {
			meta = `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
		}
		fmt.Fprintf(&b, `<li><div class="item-main"><a href="/items/%d">%s</a><div class="item-meta">%s</div></div></li>`, item.ID, template.HTMLEscapeString(item.Title), meta)
	}
	b.WriteString("</ul>")
	if len(items) == 0 && query != "" {
		b.WriteString(`<p class="note">No matches.</p>`)
	}
	s.render(w, "Search", b.String(), "")
}
func (s *Server) image(w http.ResponseWriter, r *http.Request, _ string) {
	if s.cfg.Fetcher == nil || s.cfg.ImageCacheDir == "" {
		http.NotFound(w, r)
		return
	}
	rawURL := r.URL.Query().Get("url")
	if err := fetch.ValidateURL(rawURL); err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256([]byte(rawURL))
	path := filepath.Join(s.cfg.ImageCacheDir, base64.RawURLEncoding.EncodeToString(sum[:]))
	body, err := os.ReadFile(path)
	contentType := ""
	if errors.Is(err, os.ErrNotExist) {
		body, headers, err := s.cfg.Fetcher.GetMedia(r.Context(), rawURL)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType = headers.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
			http.NotFound(w, r)
			return
		}
		if err := os.MkdirAll(s.cfg.ImageCacheDir, 0700); err != nil {
			s.internalError(w, err)
			return
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			s.internalError(w, err)
			return
		}
	} else if err != nil {
		s.internalError(w, err)
		return
	}
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	_, _ = w.Write(body)
}
func (s *Server) highlights(w http.ResponseWriter, r *http.Request, _ string) {
	highlights, err := s.store.ListHighlightsDetailed(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	fmt.Fprintf(&b, `<h1>Highlights</h1><p class="subtitle">%d passage%s saved across your reading.</p>`, len(highlights), plural(len(highlights)))
	if len(highlights) == 0 {
		b.WriteString(`<p class="note">No highlights yet. Open an article and select any passage to save it.</p>`)
		s.render(w, "Highlights", b.String(), "")
		return
	}
	b.WriteString(`<div class="highlights">`)
	for _, h := range highlights {
		b.WriteString(`<article class="hl-card">`)
		fmt.Fprintf(&b, `<blockquote>%s</blockquote>`, template.HTMLEscapeString(h.Quote))
		if h.Note != "" {
			fmt.Fprintf(&b, `<p class="hl-note">%s</p>`, template.HTMLEscapeString(h.Note))
		}
		title := h.ItemTitle
		if title == "" {
			title = "Untitled"
		}
		fmt.Fprintf(&b, `<p class="hl-source"><a href="/items/%d">%s</a><time>%s</time></p></article>`,
			h.ItemID, template.HTMLEscapeString(title), relativeTime(h.CreatedAt))
	}
	b.WriteString(`</div>`)
	s.render(w, "Highlights", b.String(), "")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
func (s *Server) bulk(w http.ResponseWriter, r *http.Request, _ string) {
	values := r.Form["item"]
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid item selection", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
	}
	if err := s.store.BulkUpdate(r.Context(), ids, r.FormValue("action")); err != nil {
		s.render(w, "Items", "", "Select one or more items and an action.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request, _ string) {
	token := template.HTMLEscapeString(csrf(r))
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Settings</h1>`)
	b.WriteString(`<section class="dash-section"><h2>Import</h2><p><a href="/import">Import data</a> from Pocket, Instapaper, linkding, Readeck, or a browser bookmarks file.</p></section>`)
	b.WriteString(`<section class="dash-section"><h2>Export</h2><p>Download <a href="/export.json">everything as JSON</a> or <a href="/export.opml">feeds as OPML</a>.</p>`)
	fmt.Fprintf(&b, `<form method="post" action="/export/markdown"><input type="hidden" name="csrf_token" value="%s"><button>Export Markdown to the configured folder</button></form></section>`, token)
	b.WriteString(`<section class="dash-section"><h2>API and integrations</h2><p><a href="/tokens">API tokens</a> for the browser extension, an Obsidian plugin, and publishing shared links to your website.</p></section>`)
	fmt.Fprintf(&b, `<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="%s"><button>Log out</button></form>`, token)
	s.render(w, "Settings", b.String(), "")
}

func (s *Server) newFeed(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Subscribe", `<form method="post" action="/feeds"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Feed URL <input type="url" name="url" required autofocus></label><label>Tags, comma-separated <input name="tags"></label><button>Subscribe</button></form>`, "")
}
func (s *Server) createFeed(w http.ResponseWriter, r *http.Request, _ string) {
	_, err := s.store.AddFeed(r.Context(), r.FormValue("url"), time.Hour, strings.Split(r.FormValue("tags"), ","))
	if err != nil {
		s.log.Warn("feed subscription failed", "error", err)
		s.render(w, "Subscribe", "", "Could not subscribe. Check that the URL is valid.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func saveForm(csrfToken, rawURL string) string {
	return `<div class="form-page"><h1>Add a link</h1>` +
		`<form class="stacked" method="post" action="/save"><input type="hidden" name="csrf_token" value="` + csrfToken + `">` +
		`<label>URL <input type="url" name="url" required autofocus placeholder="https://example.com/article" value="` + template.HTMLEscapeString(rawURL) + `"></label>` +
		`<label>Tags <input name="tags" placeholder="comma-separated, optional"></label>` +
		`<fieldset class="choice"><legend>How to save it</legend>` +
		`<label class="radio"><input type="radio" name="read_later" value="1" checked><span><strong>Read later</strong><small>Fetch the full article so you can read and highlight it.</small></span></label>` +
		`<label class="radio"><input type="radio" name="read_later" value="0"><span><strong>Bookmark</strong><small>Just store the link in your bookmarks and linklog.</small></span></label>` +
		`</fieldset><button class="primary">Add</button></form></div>`
}

func (s *Server) newSave(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Add a link", saveForm(template.HTMLEscapeString(csrf(r)), ""), "")
}
func (s *Server) share(w http.ResponseWriter, r *http.Request, _ string) {
	rawURL := r.URL.Query().Get("url")
	if err := fetch.ValidateURL(rawURL); err != nil {
		s.render(w, "Add a link", "", "The shared URL is invalid.")
		return
	}
	s.render(w, "Add a link", saveForm(template.HTMLEscapeString(csrf(r)), rawURL), "")
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "API tokens", `<form method="post" action="/tokens"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Name <input name="name" required></label><fieldset><legend>Scopes</legend><label><input type="checkbox" name="scope" value="read" checked> Read (retrieve items and highlights)</label><label><input type="checkbox" name="scope" value="write"> Write (save pages, mark read, add highlights)</label></fieldset><button>Create token</button></form><p class="note">Give an Obsidian plugin a read+write token; give your website a read-only token for the shared linklog.</p>`, "")
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, _ string) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.render(w, "API tokens", "", "Token name is required.")
		return
	}
	var scopes []string
	for _, scope := range r.Form["scope"] {
		if scope == "read" || scope == "write" {
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		s.render(w, "API tokens", "", "Select at least one scope.")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		s.internalError(w, err)
		return
	}
	sum := sha256.Sum256([]byte(token))
	if err := s.store.CreateAPIToken(r.Context(), name, base64.RawURLEncoding.EncodeToString(sum[:]), strings.Join(scopes, " ")); err != nil {
		s.internalError(w, err)
		return
	}
	s.render(w, "API token", `<p>Copy this token now. It will not be shown again. Scopes: `+template.HTMLEscapeString(strings.Join(scopes, ", "))+`</p><code>`+template.HTMLEscapeString(token)+`</code>`, "")
}

func (s *Server) apiSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.cfg.Saver == nil {
		s.internalError(w, errors.New("manual saver is not configured"))
		return
	}
	if !s.requireScope(w, r, "write") {
		return
	}
	var request struct {
		URL       string   `json:"url"`
		Tags      []string `json:"tags"`
		ReadLater *bool    `json:"read_later"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Default to read-later; pass "read_later": false to store a link-only bookmark.
	var id int64
	var err error
	if request.ReadLater != nil && !*request.ReadLater {
		id, err = s.cfg.Saver.SaveLink(r.Context(), request.URL, request.Tags)
	} else {
		id, err = s.cfg.Saver.Save(r.Context(), request.URL, request.Tags)
	}
	if err != nil {
		s.log.Warn("API page save failed", "error", err)
		http.Error(w, "could not save URL", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":%d}`, id)
}

func (s *Server) apiOptions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tokenScopes(r *http.Request) string {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	scopes, err := s.store.TokenScopes(r.Context(), base64.RawURLEncoding.EncodeToString(sum[:]))
	if err != nil {
		return ""
	}
	return scopes
}

func hasScope(scopes, want string) bool {
	for _, sc := range strings.Fields(scopes) {
		if sc == want || (want == "write" && sc == "save") {
			return true
		}
	}
	return false
}

// requireScope enforces a token scope on an API request, writing 401 and
// returning false when the token lacks it.
func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	if hasScope(s.tokenScopes(r), scope) {
		return true
	}
	http.Error(w, "invalid or insufficiently scoped API token", http.StatusUnauthorized)
	return false
}

func (s *Server) apiJSON(w http.ResponseWriter, r *http.Request, value any) {
	if !s.requireScope(w, r, "read") {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.internalError(w, err)
	}
}

// apiItem is the stable JSON shape for an item, with ISO-8601 dates, consumed
// by the Obsidian plugin and the website.
type apiItem struct {
	ID            int64    `json:"id"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Author        string   `json:"author,omitempty"`
	SiteName      string   `json:"site_name,omitempty"`
	Source        string   `json:"source"`
	Kind          string   `json:"kind"` // "article" or "link"
	ReadLater     bool     `json:"read_later"`
	Bookmarked    bool     `json:"bookmarked"`
	Read          bool     `json:"read"`
	Starred       bool     `json:"starred"`
	Archived      bool     `json:"archived"`
	Shared        bool     `json:"shared"`
	LinkStatus    int      `json:"link_status"`
	Tags          []string `json:"tags,omitempty"`
	Content       string   `json:"content,omitempty"`
	AddedAt       string   `json:"added_at"`
	PublishedAt   string   `json:"published_at,omitempty"`
	ReadAt        string   `json:"read_at,omitempty"`
	LinkCheckedAt string   `json:"link_checked_at,omitempty"`
}

func ns(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func toAPIItem(i store.Item, tags []string, withContent bool) apiItem {
	kind := "article"
	if i.Bookmarked && !i.ReadLater {
		kind = "link"
	}
	item := apiItem{
		ID: i.ID, URL: i.URL, Title: i.Title, Author: i.Author, SiteName: i.SiteName,
		Source: i.Source, Kind: kind, ReadLater: i.ReadLater, Bookmarked: i.Bookmarked, Read: i.ReadState == "read",
		Starred: i.Starred, Archived: i.Archived, Shared: i.Shared, LinkStatus: i.LinkStatus,
		Tags: tags, AddedAt: i.AddedAt.UTC().Format(time.RFC3339),
		PublishedAt: ns(i.PublishedAt), ReadAt: ns(i.ReadAt), LinkCheckedAt: ns(i.LinkCheckedAt),
	}
	if withContent {
		item.Content = i.ExtractedText
	}
	return item
}

func (s *Server) apiItemsList(w http.ResponseWriter, r *http.Request, items []store.Item, withContent bool) {
	out := make([]apiItem, 0, len(items))
	for _, item := range items {
		tags, err := s.store.ItemTags(r.Context(), item.ID)
		if err != nil {
			s.internalError(w, err)
			return
		}
		out = append(out, toAPIItem(item, tags, withContent))
	}
	s.apiJSON(w, r, out)
}

func (s *Server) apiItems(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "read") {
		return
	}
	items, err := s.store.AllItems(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.apiItemsList(w, r, items, true)
}

// apiShared powers the website: shared links (read_later=0) form the linklog,
// shared articles (read_later=1) the read-articles page. Filter with
// ?read_later=1 or ?read_later=0.
func (s *Server) apiShared(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "read") {
		return
	}
	items, _, err := s.store.ListPage(r.Context(), store.ListOptions{
		Shared: "1", ReadLater: r.URL.Query().Get("read_later"), Bookmarked: r.URL.Query().Get("bookmarked"), PerPage: 100,
	})
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.apiItemsList(w, r, items, false)
}

func (s *Server) apiFeeds(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.store.AllFeeds(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.apiJSON(w, r, feeds)
}
func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "read") {
		return
	}
	items, err := s.store.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, "invalid search query", http.StatusBadRequest)
		return
	}
	s.apiItemsList(w, r, items, false)
}

type apiHighlight struct {
	ID        int64  `json:"id"`
	ItemID    int64  `json:"item_id"`
	Quote     string `json:"quote"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

func toAPIHighlight(h store.Highlight) apiHighlight {
	return apiHighlight{ID: h.ID, ItemID: h.ItemID, Quote: h.Quote, Note: h.Note, CreatedAt: h.CreatedAt.UTC().Format(time.RFC3339)}
}

func (s *Server) apiHighlights(w http.ResponseWriter, r *http.Request) {
	highlights, err := s.store.ListHighlights(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]apiHighlight, 0, len(highlights))
	for _, h := range highlights {
		out = append(out, toAPIHighlight(h))
	}
	s.apiJSON(w, r, out)
}

// apiMarkRead lets the Obsidian plugin push read state. Body: {"read": true}.
func (s *Server) apiMarkRead(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "write") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid item id", http.StatusBadRequest)
		return
	}
	var request struct {
		Read bool `json:"read"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	state := "unread"
	if request.Read {
		state = "read"
	}
	if err := s.store.SetReadState(r.Context(), id, state); err != nil {
		s.internalError(w, err)
		return
	}
	item, err := s.store.Item(r.Context(), id)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toAPIItem(item, nil, false))
}

// apiAddHighlight lets the Obsidian plugin push a highlight. Body:
// {"quote": "...", "note": "..."}.
func (s *Server) apiAddHighlight(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "write") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid item id", http.StatusBadRequest)
		return
	}
	var request struct {
		Quote string `json:"quote"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.AddHighlight(r.Context(), id, request.Quote, request.Note, 0); err != nil {
		http.Error(w, "a highlight needs text", http.StatusBadRequest)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) exportJSON(w http.ResponseWriter, r *http.Request, _ string) {
	body, err := exporter.JSON(r.Context(), s.store)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="scrimshaw.json"`)
	_, _ = w.Write(body)
}
func (s *Server) exportOPML(w http.ResponseWriter, r *http.Request, _ string) {
	body, err := exporter.OPML(r.Context(), s.store)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml")
	w.Header().Set("Content-Disposition", `attachment; filename="scrimshaw.opml"`)
	_, _ = w.Write(body)
}
func (s *Server) exportMarkdown(w http.ResponseWriter, r *http.Request, _ string) {
	if s.cfg.ExportDir == "" {
		s.internalError(w, errors.New("markdown export is not configured"))
		return
	}
	if err := exporter.Markdown(r.Context(), s.store, s.cfg.ExportDir); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) netscapeImportForm(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Import bookmarks", `<form method="post" action="/import/netscape" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Bookmarks HTML <input type="file" name="bookmarks" accept=".html,text/html" required></label><button>Import</button></form>`, "")
}

func (s *Server) netscapeImport(w http.ResponseWriter, r *http.Request, _ string) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "invalid import", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("bookmarks")
	if err != nil {
		http.Error(w, "bookmark file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	count, err := importers.NetscapeBookmarks(r.Context(), file, s.store)
	if err != nil {
		s.log.Warn("bookmark import failed", "error", err)
		s.render(w, "Import bookmarks", "", "Could not import the bookmark file.")
		return
	}
	s.render(w, "Import bookmarks", `<p>Imported `+strconv.Itoa(count)+` bookmarks.</p>`, "")
}

func (s *Server) importForm(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Import", `<form method="post" action="/import" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Format <select name="format"><option value="netscape">Netscape bookmarks (Pocket, browsers)</option><option value="instapaper">Instapaper CSV</option><option value="linkding">Linkding JSON</option><option value="readeck">Readeck JSON</option><option value="opml">OPML feeds</option></select></label><label>File <input type="file" name="file" required></label><button>Import</button></form>`, "")
}

func (s *Server) importFile(w http.ResponseWriter, r *http.Request, _ string) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "invalid import", http.StatusBadRequest)
		return
	}
	format := r.FormValue("format")
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "import file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	count, err := importers.Import(r.Context(), format, file, s.store)
	if err != nil {
		s.log.Warn("import failed", "format", format, "error", err)
		s.render(w, "Import", "", "Could not import that file. Check the format and try again.")
		return
	}
	s.render(w, "Import", `<p>Imported `+strconv.Itoa(count)+` items.</p>`, "")
}

func (s *Server) saveURL(w http.ResponseWriter, r *http.Request, _ string) {
	if s.cfg.Saver == nil {
		s.internalError(w, errors.New("manual saver is not configured"))
		return
	}
	rawURL := r.FormValue("url")
	tags := strings.Split(r.FormValue("tags"), ",")
	var id int64
	var err error
	if r.FormValue("read_later") == "1" {
		id, err = s.cfg.Saver.Save(r.Context(), rawURL, tags)
	} else {
		id, err = s.cfg.Saver.SaveLink(r.Context(), rawURL, tags)
	}
	if err != nil {
		s.log.Warn("page save failed", "error", err)
		s.render(w, "Add a link", "", "Could not save that URL. Check it and try again.")
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) manifest(w http.ResponseWriter, _ *http.Request) {
	body, err := webassets.Files.ReadFile("manifest.webmanifest")
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	_, _ = w.Write(body)
}

func (s *Server) serviceWorker(w http.ResponseWriter, _ *http.Request) {
	body, err := webassets.Files.ReadFile("service-worker.js")
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(body)
}

func (s *Server) asset(name, contentType string) http.HandlerFunc {
	body, err := static.Files.ReadFile(name)
	return func(w http.ResponseWriter, _ *http.Request) {
		if err != nil {
			s.internalError(w, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><link rel="manifest" href="/manifest.webmanifest"><link rel="stylesheet" href="/app.css"><title>{{.Title}} | Scrimshaw</title></head><body><header class="topbar"><div class="bar-inner"><a class="brand" href="/">Scrimshaw</a><a class="brand-add" href="/save">Add a link</a></div></header><main class="container">{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{.Body}}</main><script src="/app.js" defer></script></body></html>`))

func starButtonAttr(starred bool) string {
	if starred {
		return ` class="primary"`
	}
	return ""
}

// linkBroken reports whether a stored link failed its last reachability check.
func linkBroken(item store.Item) bool {
	return (item.Bookmarked || item.Source == "manual") && (item.LinkStatus >= 400 || item.LinkStatus < 0)
}

// itemKindBadges renders the provenance and saved-state badges for an item:
// Feed for feed articles, plus Read later and Bookmarked as they apply.
func itemKindBadges(item store.Item) string {
	var out string
	if item.Source == "feed" {
		out += ` <span class="badge feed">Feed</span>`
	}
	if item.ReadLater {
		out += ` <span class="badge later">Read later</span>`
	}
	if item.Bookmarked {
		out += ` <span class="badge bookmark">Bookmarked</span>`
	}
	if out == "" {
		out = ` <span class="badge">Saved</span>`
	}
	return out
}

func shortDate(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	t, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		return ""
	}
	return t.Format("Jan 2, 2006")
}

// relativeTime renders a compact age like "5m", "3h", "2d", or a date.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case t.Year() == time.Now().Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2006")
	}
}

func viewTab(href, label, state, current string) string {
	if state == current {
		return fmt.Sprintf(`<a href="%s" class="active" aria-current="page">%s</a>`, href, label)
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, href, label)
}

func optionTag(value, label, current string) string {
	selected := ""
	if value == current {
		selected = " selected"
	}
	return fmt.Sprintf(`<option value="%s"%s>%s</option>`, value, selected, label)
}

func (s *Server) render(w http.ResponseWriter, title, body, errorText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, map[string]any{"Title": title, "Body": template.HTML(body), "Error": errorText}); err != nil {
		s.log.Error("render template", "error", err)
	}
}
func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Server) setSessionCookie(w http.ResponseWriter, id string) {
	mac := hmac.New(sha256.New, s.cfg.SessionSecret)
	mac.Write([]byte(id))
	value := id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{Name: "scrimshaw_session", Value: value, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}
func (s *Server) sessionID(r *http.Request) (string, error) {
	cookie, err := r.Cookie("scrimshaw_session")
	if err != nil {
		return "", err
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return "", errors.New("malformed session")
	}
	mac := hmac.New(sha256.New, s.cfg.SessionSecret)
	mac.Write([]byte(parts[0]))
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return "", errors.New("invalid session")
	}
	return parts[0], nil
}
