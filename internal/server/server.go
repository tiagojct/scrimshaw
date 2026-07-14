package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	exporter "github.com/tiagojct/scrimshaw/internal/export"
	"github.com/tiagojct/scrimshaw/internal/feeds"
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
	BaseURL       string
	Saver         *reader.Saver
	Feeds         *feeds.Service
	SnapshotsDir  string
	ImageCacheDir string
	Fetcher       *fetch.Client
	ExportDir     string
}

// baseURL returns the app's absolute origin (scheme://host), from the configured
// base URL or, failing that, the request. Used to build bookmarklet/API snippets.
func (s *Server) baseURL(r *http.Request) string {
	candidate := s.cfg.BaseURL
	if candidate == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		candidate = scheme + "://" + r.Host
	}
	u, err := url.Parse(candidate)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	// The host comes from a request header when BaseURL is unset; reject anything
	// that could break out of the HTML attribute or JS string it is embedded in.
	if strings.ContainsAny(u.Host, "\"'<>\\ \t\n") {
		return ""
	}
	return u.Scheme + "://" + u.Host
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
	mux.HandleFunc("POST /items/{id}/extract", s.withSession(s.extractItem))
	mux.HandleFunc("POST /items/{id}/bookmark", s.withSession(s.bookmarkItem))
	mux.HandleFunc("GET /triage", s.withSession(s.triage))
	mux.HandleFunc("GET /habits", s.withSession(s.habits))
	mux.HandleFunc("POST /saved-views", s.withSession(s.createSavedView))
	mux.HandleFunc("POST /saved-views/{id}/delete", s.withSession(s.deleteSavedView))
	mux.HandleFunc("POST /triage/{id}/bookmark", s.withSession(s.triageBookmark))
	mux.HandleFunc("POST /items/{id}/highlights", s.withSession(s.highlight))
	mux.HandleFunc("POST /items/{id}/tags", s.withSession(s.setTags))
	mux.HandleFunc("POST /items/{id}/delete", s.withSession(s.deleteItem))
	mux.HandleFunc("POST /items/bulk", s.withSession(s.bulk))
	mux.HandleFunc("GET /feeds", s.withSession(s.feedsList))
	mux.HandleFunc("GET /feeds/new", s.withSession(s.newFeed))
	mux.HandleFunc("POST /feeds", s.withSession(s.createFeed))
	mux.HandleFunc("POST /feeds/refresh", s.withSession(s.refreshAllFeeds))
	mux.HandleFunc("POST /feeds/{id}/refresh", s.withSession(s.refreshFeed))
	mux.HandleFunc("POST /feeds/{id}/settings", s.withSession(s.feedSettings))
	mux.HandleFunc("POST /feeds/{id}/delete", s.withSession(s.deleteFeed))
	mux.HandleFunc("GET /save", s.withSession(s.newSave))
	mux.HandleFunc("POST /save", s.withSession(s.saveURL))
	mux.HandleFunc("GET /share", s.withSession(s.share))
	mux.HandleFunc("GET /settings", s.withSession(s.settings))
	mux.HandleFunc("POST /settings/password", s.withSession(s.changePassword))
	mux.HandleFunc("POST /settings/tags/rename", s.withSession(s.renameTag))
	mux.HandleFunc("POST /settings/tags/merge", s.withSession(s.mergeTag))
	mux.HandleFunc("GET /tokens", s.withSession(s.tokens))
	mux.HandleFunc("POST /tokens", s.withSession(s.createToken))
	mux.HandleFunc("POST /api/save", s.apiSave)
	mux.HandleFunc("OPTIONS /api/save", s.apiOptions)
	mux.HandleFunc("GET /api/items", s.apiItems)
	mux.HandleFunc("GET /api/shared", s.apiShared)
	mux.HandleFunc("GET /feed.xml", s.feedXML)
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
	mux.HandleFunc("POST /import/opml", s.withSession(s.importOPML))
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

const setupBody = `<div class="form-page auth"><h1>Welcome to Scrimshaw</h1><p class="subtitle">Create your account to get started.</p><form method="post"><label>Password (at least 12 characters) <input type="password" name="password" required minlength="12" autofocus></label><button class="primary">Create account</button></form></div>`

const loginBody = `<div class="form-page auth"><h1>Sign in</h1><form method="post"><label>Password <input type="password" name="password" required autofocus></label><button class="primary">Log in</button></form></div>`

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
		s.render(w, "Setup", setupBody, "")
		return
	}
	password := r.FormValue("password")
	if len(password) < 12 {
		s.render(w, "Setup", setupBody, "Password must contain at least 12 characters.")
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
		s.render(w, "Login", loginBody, "")
		return
	}
	address, _, _ := net.SplitHostPort(r.RemoteAddr)
	allowed, err := s.store.LoginAllowed(r.Context(), address)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !allowed {
		s.render(w, "Login", loginBody, "Too many login attempts. Try again later.")
		return
	}
	hash, err := s.store.UserPasswordHash(r.Context())
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("password"))) != nil {
		_ = s.store.RecordLoginFailure(r.Context(), address)
		s.render(w, "Login", loginBody, "Invalid credentials.")
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
	if v.key == "today" {
		// Computed per-request (not stored on the static itemView) since "today"
		// moves; bounds are on COALESCE(published_at, added_at), so manual saves
		// and undated feed items count by when they were added.
		start := time.Now()
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
		options.Since = start.UTC().Format(time.RFC3339)
		options.Until = start.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	}
	items, total, err := s.store.ListPage(r.Context(), options)
	if err != nil {
		s.internalError(w, err)
		return
	}
	var tagCounts []store.Count
	if tag == "" {
		tagCounts, err = s.store.UnreadTagCounts(r.Context(), options)
		if err != nil {
			s.internalError(w, err)
			return
		}
	}

	// Preserve the active view, sort, and tag across pagination.
	link := func(extra url.Values) string {
		q := url.Values{"view": {v.key}}
		if sort != "" {
			q.Set("sort", sort)
		}
		if tag != "" {
			q.Set("tag", tag)
		}
		for key, values := range extra {
			q[key] = values
		}
		return "/?" + q.Encode()
	}

	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<nav class="views" aria-label="Views">`)
	for _, item := range viewOrder {
		b.WriteString(viewTab("/?view="+item.key, item.label, item.key, v.key))
	}
	b.WriteString(`</nav>`)
	fmt.Fprintf(&b, `<h1 class="view-title">%s</h1>`, v.label)
	fmt.Fprintf(&b, `<div class="filters"><form action="/search"><label>Search <input name="q" type="search" placeholder="Search everything"></label><button>Search</button></form><form action="/"><input type="hidden" name="view" value="%s"><input type="hidden" name="tag" value="%s"><label>Sort <select name="sort">%s%s%s</select></label><button>Apply</button></form>`,
		template.HTMLEscapeString(v.key), template.HTMLEscapeString(tag),
		optionTag("", "Newest", sort), optionTag("oldest", "Oldest", sort), optionTag("unread", "Unread first", sort))
	if v.key == "later" {
		// A display-only preference (remembered client-side, not posted
		// anywhere), same pattern as the reader's reading-profile picker.
		b.WriteString(`<label class="density-picker">Density <select id="density-mode"><option value="">Standard</option><option value="density-compact">Compact</option></select></label>`)
	}
	b.WriteString(`</div>`)
	// A saved view is just a label on the current URL — view/tag/sort/search
	// already encode fully into it, so there's no separate filter to build.
	// Kept out of .filters: pinning is a bookmarking action on the result,
	// not another filter control.
	fmt.Fprintf(&b, `<div class="pin-view-bar"><form class="inline-action pin-view" method="post" action="/saved-views"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="path" value="%s"><input name="label" placeholder="Name this view" required><button>Pin this view</button></form></div>`,
		template.HTMLEscapeString(csrf(r)), template.HTMLEscapeString(r.URL.RequestURI()))
	if tag != "" {
		clear := "/?view=" + url.QueryEscape(v.key)
		if sort != "" {
			clear += "&sort=" + url.QueryEscape(sort)
		}
		fmt.Fprintf(&b, `<p class="tagbar">Tag: %s &middot; <a href="%s">clear</a></p>`, template.HTMLEscapeString(tag), clear)
	} else if len(tagCounts) > 0 {
		b.WriteString(`<p class="tagbar">Unread tags: `)
		for _, count := range tagCounts {
			fmt.Fprintf(&b, `<a class="%s" href="/?view=%s&tag=%s">%s (%d)</a>`, tagChipClass(count.Name), url.QueryEscape(v.key), url.QueryEscape(count.Name), template.HTMLEscapeString(count.Name), count.Count)
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
	fmt.Fprintf(&b, `<form method="post" action="/items/bulk"><input type="hidden" name="csrf_token" value="%s"><ul class="%s" data-view="%s">`, template.HTMLEscapeString(csrf(r)), listClass, template.HTMLEscapeString(v.key))
	for _, item := range items {
		classes := template.HTMLEscapeString(item.ReadState)
		if item.Starred {
			classes += " starred"
		}
		// In the "Read" view every item is read, so the badge would be noise there.
		meta := itemMeta(item, v.showSource, v.key != "archived")
		// Favicons only in the Feeds view: other views mix sources, and a
		// blank favicon column there would read as broken rather than help.
		icon := ""
		if v.key == "feeds" && item.Source == "feed" {
			icon = feedIcon(item.FeedTitle.String, item.FeedFaviconURL.String)
		}
		fmt.Fprintf(&b, `<li class="%s"><input type="checkbox" name="item" value="%d" aria-label="Select %s"><div class="item-main"><a href="/items/%d">%s%s</a><div class="item-meta">%s</div></div></li>`,
			classes, item.ID, template.HTMLEscapeString(item.Title), item.ID, icon, template.HTMLEscapeString(item.Title), meta)
	}
	b.WriteString(`</ul><div class="bulk-actions">`)
	if v.key == "archived" {
		b.WriteString(`<button name="action" value="unread">Mark selected unread</button>`)
	} else {
		b.WriteString(`<button name="action" value="read">Mark selected read</button>`)
	}
	b.WriteString(`<button class="danger-btn" name="action" value="delete">Delete selected</button></div></form>`)
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

const dashboardToolbar = `<nav class="toolbar" aria-label="Sections"><a href="/feeds">Feeds</a><a href="/feeds/new">Add a feed</a><a href="/search">Search</a><a href="/highlights">Highlights</a><a href="/settings">Settings</a></nav>`

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
	savedViews, err := s.store.AllSavedViews(r.Context())
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
	b.WriteString(`<p class="note"><a href="/habits">Reading habits</a></p>`)
	if len(savedViews) > 0 {
		token := template.HTMLEscapeString(csrf(r))
		b.WriteString(`<section class="dash-section"><h2>Saved views</h2><ul class="saved-views">`)
		for _, view := range savedViews {
			fmt.Fprintf(&b, `<li><a href="%s">%s</a><form method="post" action="/saved-views/%d/delete"><input type="hidden" name="csrf_token" value="%s"><button class="link-btn" aria-label="Remove %s">&times;</button></form></li>`,
				template.HTMLEscapeString(view.Path), template.HTMLEscapeString(view.Label), view.ID, token, template.HTMLEscapeString(view.Label))
		}
		b.WriteString(`</ul></section>`)
	}

	// showSource mirrors the corresponding view's own showSource flag (see
	// viewOrder below) so an item carries the same badges here as it would
	// in its full list view.
	section := func(title, href, moreLabel string, items []store.Item, showSource bool) {
		fmt.Fprintf(&b, `<section class="dash-section"><h2><a href="%s">%s</a></h2>`, href, title)
		if len(items) == 0 {
			b.WriteString(`<p class="note">Nothing here yet.</p></section>`)
			return
		}
		b.WriteString(`<ul class="items">`)
		for _, item := range items {
			meta := itemMeta(item, showSource, true)
			fmt.Fprintf(&b, `<li><div class="item-main"><a href="/items/%d">%s</a><div class="item-meta">%s</div></div></li>`,
				item.ID, template.HTMLEscapeString(item.Title), meta)
		}
		fmt.Fprintf(&b, `</ul><p class="note"><a href="%s">%s</a></p></section>`, href, moreLabel)
	}
	section("To read", "/?view=later", "All read-later", later, true)
	if st.ReadLaterUnread > 0 {
		b.WriteString(`<p class="note"><a href="/triage">Triage the Read Later queue</a> — burn it down one item at a time.</p>`)
	}
	section("Recent bookmarks", "/?view=bookmarks", "All bookmarks", bookmarks, true)
	section("Unread in feeds", "/?view=feeds&sort=unread", "All feeds", feedItems, false)

	b.WriteString(`<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf(r)) + `"><button>Log out</button></form>`)
	s.render(w, "Dashboard", b.String(), "")
}

// habits shows reading activity and Read Later backlog over the last 12
// weeks, plus the most-used tags — a low-stakes motivational page, not a
// workflow, so it's fine that it's built from data already stored rather
// than a tracked time series.
func (s *Server) habits(w http.ResponseWriter, r *http.Request, _ string) {
	stats, err := s.store.ReadingHabits(r.Context(), 12)
	if err != nil {
		s.internalError(w, err)
		return
	}
	tagCounts, err := s.store.AllTagCounts(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	slices.SortFunc(tagCounts, func(a, b store.Count) int { return b.Count - a.Count })
	if len(tagCounts) > 8 {
		tagCounts = tagCounts[:8]
	}

	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Reading habits</h1>`)
	b.WriteString(`<section class="dash-section"><h2>Items read per week</h2>`)
	b.WriteString(string(weeklyBarChart(stats, func(st store.WeekStat) int { return st.Read })))
	b.WriteString(`</section>`)
	b.WriteString(`<section class="dash-section"><h2>Read Later backlog</h2><p class="note">Reconstructed from added/read timestamps, not a tracked history — an item deleted since then leaves no trace here.</p>`)
	b.WriteString(string(weeklyBarChart(stats, func(st store.WeekStat) int { return st.Backlog })))
	b.WriteString(`</section>`)
	if len(tagCounts) > 0 {
		b.WriteString(`<section class="dash-section"><h2>Top tags</h2><p class="tagbar">`)
		for _, c := range tagCounts {
			fmt.Fprintf(&b, `<span class="%s">%s (%d)</span> `, tagChipClass(c.Name), template.HTMLEscapeString(c.Name), c.Count)
		}
		b.WriteString(`</p></section>`)
	}
	s.render(w, "Reading habits", b.String(), "")
}

// weeklyBarChart renders a minimal inline SVG bar chart — no charting
// library, matching the no-npm-dependency rule. All values are
// server-computed integers/dates, never user input, so building the SVG by
// string concatenation carries no injection risk here.
func weeklyBarChart(stats []store.WeekStat, value func(store.WeekStat) int) template.HTML {
	max := 1
	for _, st := range stats {
		if v := value(st); v > max {
			max = v
		}
	}
	const barW, gap, chartH, labelH = 22, 10, 70, 28
	width := len(stats)*(barW+gap) + gap
	height := chartH + labelH
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Weekly chart">`, width, height, width, height)
	for i, st := range stats {
		v := value(st)
		barH := int(float64(v) / float64(max) * chartH)
		if barH < 2 {
			barH = 2
		}
		x, y := gap+i*(barW+gap), chartH-barH
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" class="chart-bar"><title>%s: %d</title></rect>`,
			x, y, barW, barH, st.Start.Format("Jan 2"), v)
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="chart-value">%d</text>`, x+barW/2, chartH+12, v)
		if i == 0 || i == len(stats)-1 {
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="chart-label">%s</text>`, x+barW/2, chartH+26, st.Start.Format("Jan 2"))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// itemView describes one tab over the shared items table.
type itemView struct {
	key, label, source, state, readLater, bookmarked, empty string
	showSource, includeArchived                             bool
}

var viewOrder = []itemView{
	{key: "today", label: "Today", includeArchived: true, showSource: true, empty: "Nothing published today yet."},
	{key: "feeds", label: "Feeds", source: "feed", empty: "No feed items yet. Subscribe to a feed to start."},
	{key: "later", label: "Read Later", readLater: "1", empty: "Nothing to read yet. Add a link, or send a feed item here with Read later. An item leaves this list once marked read here; opening it elsewhere (e.g. in Feeds) doesn't count.", showSource: true},
	{key: "bookmarks", label: "Bookmarks", bookmarked: "1", includeArchived: true, empty: "No bookmarks yet. Add a link, or bookmark a feed item.", showSource: true},
	{key: "starred", label: "Starred", state: "starred", empty: "No starred items yet.", showSource: true},
	{key: "archived", label: "Read", state: "archived", empty: "Nothing read yet. Reading an item files it here; it stays in Bookmarks or Starred if it was one.", showSource: true},
	{key: "all", label: "All", includeArchived: true, empty: "Nothing here yet.", showSource: true},
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
	// Opening a plain feed item dismisses it from Feeds, matching every
	// mainstream feed reader's "open = read" convention. Scoped to items not
	// also claimed for Read Later or Bookmarks: those are a separate reading
	// commitment, so a peek here must not silently drop them out of that
	// queue (see PromoteToReadLater's doc comment for the other half of this).
	if item.Source == "feed" && item.ReadState == "unread" && !item.ReadLater && !item.Bookmarked {
		if err := s.store.SetReadState(r.Context(), item.ID, "read"); err != nil {
			s.internalError(w, err)
			return
		}
		item, err = s.store.Item(r.Context(), id)
		if err != nil {
			s.internalError(w, err)
			return
		}
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
	if item.ReadState == "read" {
		badges.WriteString(` <span class="badge read">Read</span>`)
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
		content = fmt.Sprintf(`<div class="reader"><p class="note">This is a stored link. Use Read later or Fetch full text below to get the article for reading.</p><p><a href="%s" rel="noopener noreferrer">%s</a></p></div>`,
			template.HTMLEscapeString(item.URL), template.HTMLEscapeString(item.URL))
	}
	// Return to the list the item belongs to. Feed items are auto-read on
	// open, so this back link (no state change) is the normal one-click way
	// out; the Mark read / Mark unread buttons below only change state.
	backURL, backLabel := "/?view=feeds", "Feeds"
	switch {
	case item.ReadLater:
		backURL, backLabel = "/?view=later", "Read Later"
	case item.Bookmarked:
		backURL, backLabel = "/?view=bookmarks", "Bookmarks"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<p class="back-link"><a href="%s">&larr; %s</a></p>`, backURL, backLabel)
	fmt.Fprintf(&b, `<article data-item-id="%d"><h1>%s</h1><p class="meta"><a class="original-link" href="%s" rel="noopener noreferrer">Open original</a>%s%s</p><p class="meta dates">%s</p>%s</article>`,
		item.ID, template.HTMLEscapeString(item.Title), template.HTMLEscapeString(item.URL), snapshotLink, badges.String(), dates.String(), content)
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
	// Mark read files the item away and returns to its list; Mark unread
	// reverses it. Feed items arrive here already read (auto-read on open),
	// so their read-state control is the quiet "Mark unread", not a loud
	// primary button — the back link above is the normal way out.
	if item.ReadState == "read" {
		fmt.Fprintf(&b, `<form class="read-form" method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="unread"><button>Mark unread</button></form>`, item.ID, token)
	} else {
		fmt.Fprintf(&b, `<form class="read-form" method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="read"><input type="hidden" name="return" value="%s"><button class="primary">Mark read</button></form>`, item.ID, token, template.HTMLEscapeString(backURL))
	}
	fmt.Fprintf(&b, `<form class="star-form" method="post" action="/items/%d/star"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="starred" value="%s"><button%s>%s</button></form>`, item.ID, token, starValue, starButtonAttr(item.Starred), starLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/readlater"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="read_later" value="%s"><button%s>%s</button></form>`, item.ID, token, laterValue, starButtonAttr(item.ReadLater), laterLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/bookmark"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="bookmarked" value="%s"><button%s>%s</button></form>`, item.ID, token, bookmarkValue, starButtonAttr(item.Bookmarked), bookmarkLabel)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/share"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="shared" value="%s"><button%s>%s</button></form>`, item.ID, token, shareValue, starButtonAttr(item.Shared), shareLabel)
	if item.ExtractedText == "" || item.Source == "feed" {
		// Independent of the Read Later toggle: fetch the full article without
		// also committing it to that queue. Feed items always offer this even
		// with content already present, since that content is just the feed's
		// own summary/description, not a full extraction.
		fetchLabel := "Fetch full text"
		if item.ExtractedText != "" {
			fetchLabel = "Fetch full article (replace summary)"
		}
		fmt.Fprintf(&b, `<form method="post" action="/items/%d/extract"><input type="hidden" name="csrf_token" value="%s"><button>%s</button></form>`, item.ID, token, fetchLabel)
	}
	// A display-only preference (remembered client-side in localStorage, not
	// posted anywhere), so named presets beat a raw slider the same way the
	// feed refresh-interval dropdown does.
	b.WriteString(`<label class="reading-profile-picker">Reading <select id="reading-profile"><option value="">Standard</option><option value="reading-compact">Compact</option><option value="reading-relaxed">Relaxed</option></select></label>`)
	b.WriteString(`</div>`)

	// Tags editor.
	tags, err := s.store.ItemTags(r.Context(), item.ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	tagLabel := "Tags"
	tagsOpen := ""
	if len(tags) > 0 {
		tagLabel = "Tags: " + template.HTMLEscapeString(strings.Join(tags, ", "))
		tagsOpen = " open"
	}
	fmt.Fprintf(&b, `<details class="tags-edit"%s><summary>%s</summary><form method="post" action="/items/%d/tags"><input type="hidden" name="csrf_token" value="%s"><label>Comma-separated tags <input name="tags" value="%s"></label><button>Save tags</button></form></details>`,
		tagsOpen, tagLabel, item.ID, token, template.HTMLEscapeString(strings.Join(tags, ", ")))

	// Selection popover and the hidden form it submits to create a highlight.
	fmt.Fprintf(&b, `<div class="hl-pop" id="hl-pop"><button type="button">Highlight</button></div><form id="hl-form" method="post" action="/items/%d/highlights"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" id="hl-quote" name="quote"><input type="hidden" name="note" value=""></form>`, item.ID, token)
	b.WriteString(`<section class="highlight-list"><h2>Highlights &amp; notes</h2>`)
	if len(highlights) > 0 {
		b.WriteString(`<ul>`)
		for _, h := range highlights {
			if h.Quote != "" {
				fmt.Fprintf(&b, `<li><q>%s</q>`, template.HTMLEscapeString(h.Quote))
				if h.Note != "" {
					fmt.Fprintf(&b, `<p class="note">%s</p>`, template.HTMLEscapeString(h.Note))
				}
				b.WriteString(`</li>`)
			} else {
				fmt.Fprintf(&b, `<li class="note-only"><p>%s</p></li>`, template.HTMLEscapeString(h.Note))
			}
		}
		b.WriteString(`</ul>`)
	} else {
		b.WriteString(`<p class="note">Select any passage in the article to highlight it, or add a note below.</p>`)
	}
	fmt.Fprintf(&b, `<details class="manual-highlight"><summary>Add a note</summary><form method="post" action="/items/%d/highlights"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="quote" value=""><label>Note <textarea name="note" rows="3" required placeholder="Your thoughts on this article"></textarea></label><button>Add note</button></form></details></section>`, item.ID, token)
	fmt.Fprintf(&b, `<details class="danger"><summary>Delete this item</summary><p class="note">Permanently removes this item, its highlights, and its snapshot. This cannot be undone.</p><form method="post" action="/items/%d/delete"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="return" value="%s"><button class="danger-btn">Delete permanently</button></form></details>`, item.ID, token, template.HTMLEscapeString(backURL))
	fmt.Fprintf(&b, `<script type="application/json" id="hl-data">%s</script>`, quotesJSON)
	notice := ""
	if r.URL.Query().Get("extract_failed") == "1" {
		notice = "Could not fetch the full article. The link may be blocked or unreachable."
	}
	s.render(w, item.Title, b.String(), notice)
}
func (s *Server) setReadState(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state := r.FormValue("state")
	if state != "read" && state != "unread" {
		http.Error(w, "invalid read state", http.StatusBadRequest)
		return
	}
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
	// Require a same-origin absolute path. Reject "//host" and "/\host", which
	// browsers normalize to protocol-relative off-site navigations.
	if strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//") && !strings.HasPrefix(dest, "/\\") {
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

// extractItem fetches an item's full article text without touching
// read_later or bookmarked — the standalone counterpart to readLaterItem's
// implicit fetch-on-promote, for reading the full text without committing
// the item to any queue.
func (s *Server) extractItem(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Saver == nil {
		s.internalError(w, errors.New("saver is not configured"))
		return
	}
	item, err := s.store.Item(r.Context(), id)
	if err != nil {
		s.internalError(w, err)
		return
	}
	idStr := strconv.FormatInt(id, 10)
	if err := s.cfg.Saver.Extract(r.Context(), id, item.URL); err != nil {
		s.log.Warn("fetch full text failed", "item", id, "error", err)
		http.Redirect(w, r, "/items/"+idStr+"?extract_failed=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/items/"+idStr, http.StatusSeeOther)
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
	if readLater {
		if err := s.store.PromoteToReadLater(r.Context(), id); err != nil {
			s.internalError(w, err)
			return
		}
	} else if err := s.store.SetReadLater(r.Context(), id, false); err != nil {
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

// triage is a fast, one-item-at-a-time way to burn down the Read Later queue,
// distinct from the list view. It walks the unread read-later items oldest
// first via plain pagination (Page N, PerPage 1) rather than any new
// schema: Keep just moves to page N+1 (a GET link, since it changes
// nothing); Skip and Bookmark each remove the current item from the
// unread+read-later set, so redirecting back to the same page N reveals
// whatever now occupies that position.
func (s *Server) triage(w http.ResponseWriter, r *http.Request, _ string) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	items, total, err := s.store.ListPage(r.Context(), store.ListOptions{
		ReadLater: "1", State: "unread", Sort: "oldest", Page: page, PerPage: 1,
	})
	if err != nil {
		s.internalError(w, err)
		return
	}
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Triage</h1>`)
	if len(items) == 0 {
		if page > 1 {
			b.WriteString(`<p class="note">Queue clear. Nothing left to triage.</p>`)
		} else {
			b.WriteString(`<p class="note">Nothing in Read Later to triage.</p>`)
		}
		b.WriteString(`<p><a href="/?view=later">Back to Read Later</a></p>`)
		s.render(w, "Triage", b.String(), "")
		return
	}
	item := items[0]
	token := template.HTMLEscapeString(csrf(r))
	returnURL := "/triage?page=" + strconv.Itoa(page)
	fmt.Fprintf(&b, `<p class="triage-progress">%d left in Read Later</p>`, total)
	meta := ""
	if item.Author != "" {
		meta += `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
	}
	meta += timeTag(item.AddedAt)
	fmt.Fprintf(&b, `<article class="triage-card"><h2><a href="/items/%d">%s</a></h2><div class="item-meta">%s</div><p class="triage-excerpt">%s</p></article>`,
		item.ID, template.HTMLEscapeString(item.Title), meta, excerpt(item.ExtractedText, "", 80))
	fmt.Fprintf(&b, `<div class="triage-actions">`)
	fmt.Fprintf(&b, `<a class="button" id="triage-keep" href="/triage?page=%d">Keep</a>`, page+1)
	fmt.Fprintf(&b, `<form method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="read"><input type="hidden" name="return" value="%s"><button id="triage-skip">Skip</button></form>`,
		item.ID, token, template.HTMLEscapeString(returnURL))
	fmt.Fprintf(&b, `<form method="post" action="/triage/%d/bookmark"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="return" value="%s"><button class="primary" id="triage-bookmark">Bookmark</button></form>`,
		item.ID, token, template.HTMLEscapeString(returnURL))
	b.WriteString(`</div>`)
	b.WriteString(`<p class="note triage-hint"><kbd>k</kbd> keep &middot; <kbd>x</kbd> skip &middot; <kbd>b</kbd> bookmark</p>`)
	s.render(w, "Triage", b.String(), "")
}

// triageBookmark bookmarks an item and clears read_later, unlike the
// reader's plain Bookmark toggle which leaves read_later untouched — triage
// is specifically about clearing the queue, so "keep the link, I'm done
// triaging it" has to actually shrink the queue, not just add a flag.
func (s *Server) triageBookmark(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetBookmarked(r.Context(), id, true); err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.SetReadLater(r.Context(), id, false); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther)
}

// createSavedView pins a label to the current view's URL. The submitted path
// is passed through safeReturn (same guard as the reader's "back" redirect)
// before it's ever stored, since a saved view is later rendered as a plain
// <a href> on the dashboard.
func (s *Server) createSavedView(w http.ResponseWriter, r *http.Request, _ string) {
	path := safeReturn(r.FormValue("path"))
	label := strings.TrimSpace(r.FormValue("label"))
	if label != "" {
		if _, err := s.store.AddSavedView(r.Context(), label, path); err != nil {
			s.internalError(w, err)
			return
		}
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (s *Server) deleteSavedView(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteSavedView(r.Context(), id); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) highlight(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.AddHighlight(r.Context(), id, r.FormValue("quote"), r.FormValue("note"), 0); err != nil {
		s.log.Warn("add highlight failed", "item", id, "error", err)
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
func searchFormBody(query string) string {
	return dashboardToolbar + `<h1>Search</h1><div class="filters"><form action="/search"><label>Search <input name="q" type="search" value="` + template.HTMLEscapeString(query) + `" autofocus placeholder="Titles, article text, snapshots"></label><button class="primary">Search</button></form></div>`
}

var (
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
	wordRe    = regexp.MustCompile(`[\p{L}\p{N}]+`)
	urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)
)

// firstURL returns the first http(s) URL found in text, for share payloads that
// pack the link into a free-text field.
func firstURL(text string) string {
	return urlInText.FindString(text)
}

func normalizeWord(w string) string {
	return strings.ToLower(strings.Trim(w, ".,;:!?\"'’“”()[]{}—–…"))
}

// excerpt builds a safe, tag-free snippet of an item's content, windowed around
// the first matching query term and with matching words wrapped in <mark>. Each
// word is escaped individually, so only the <mark> tags are ever live HTML.
func excerpt(content, query string, maxWords int) template.HTML {
	text := html.UnescapeString(htmlTagRe.ReplaceAllString(content, " "))
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	terms := map[string]bool{}
	for _, t := range wordRe.FindAllString(strings.ToLower(query), -1) {
		terms[t] = true
	}
	hit := -1
	for i, wd := range words {
		if terms[normalizeWord(wd)] {
			hit = i
			break
		}
	}
	start, end := 0, len(words)
	if hit >= 0 {
		start = hit - maxWords/2
		if start < 0 {
			start = 0
		}
		end = start + maxWords
		if end > len(words) {
			end = len(words)
		}
	} else if len(words) > maxWords {
		end = maxWords
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("… ")
	}
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte(' ')
		}
		if terms[normalizeWord(words[i])] {
			b.WriteString("<mark>" + template.HTMLEscapeString(words[i]) + "</mark>")
		} else {
			b.WriteString(template.HTMLEscapeString(words[i]))
		}
	}
	if end < len(words) {
		b.WriteString(" …")
	}
	return template.HTML(b.String())
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, _ string) {
	query := r.URL.Query().Get("q")
	items, err := s.store.Search(r.Context(), query)
	if err != nil {
		s.render(w, "Search", searchFormBody(query), "Search query is invalid.")
		return
	}
	var b strings.Builder
	b.WriteString(searchFormBody(query))
	b.WriteString(`<ul class="items">`)
	for _, item := range items {
		meta := ""
		if item.Author != "" {
			meta = `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
		}
		meta += timeTag(item.AddedAt)
		snippet := ""
		if ex := excerpt(item.ExtractedText, query, 34); ex != "" {
			snippet = `<p class="snippet">` + string(ex) + `</p>`
		}
		fmt.Fprintf(&b, `<li><div class="item-main"><a href="/items/%d">%s</a><div class="item-meta">%s</div>%s</div></li>`,
			item.ID, template.HTMLEscapeString(item.Title), meta, snippet)
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
		var headers http.Header
		// Assign the outer body/err (no ':='), or the response would be empty on
		// the first, cache-miss request.
		body, headers, err = s.cfg.Fetcher.GetMedia(r.Context(), rawURL)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType = headers.Get("Content-Type")
		lower := strings.ToLower(contentType)
		// SVG can carry scripts and is served same-origin; reject it rather than
		// leaning only on the page CSP.
		if !strings.HasPrefix(lower, "image/") || strings.Contains(lower, "svg") {
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
	b.WriteString(`<h1>Highlights</h1><p class="subtitle">Passages and notes saved from your reading.</p>`)
	if len(highlights) == 0 {
		b.WriteString(`<p class="note">Nothing yet. Open an article, select any passage to highlight it, or add a note.</p>`)
		s.render(w, "Highlights", b.String(), "")
		return
	}
	b.WriteString(`<div class="highlights">`)
	for _, h := range highlights {
		if h.Quote != "" {
			b.WriteString(`<article class="hl-card">`)
			fmt.Fprintf(&b, `<blockquote>%s</blockquote>`, template.HTMLEscapeString(h.Quote))
			if h.Note != "" {
				fmt.Fprintf(&b, `<p class="hl-note">%s</p>`, template.HTMLEscapeString(h.Note))
			}
		} else {
			b.WriteString(`<article class="hl-card note-card">`)
			fmt.Fprintf(&b, `<p class="hl-note-body">%s</p>`, template.HTMLEscapeString(h.Note))
		}
		title := h.ItemTitle
		if title == "" {
			title = "Untitled"
		}
		fmt.Fprintf(&b, `<p class="hl-source"><a href="/items/%d">%s</a>%s</p></article>`,
			h.ItemID, template.HTMLEscapeString(title), timeTag(h.CreatedAt))
	}
	b.WriteString(`</div>`)
	s.render(w, "Highlights", b.String(), "")
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
	// Return to the list the action was invoked from. On the confirm step the
	// referer is the confirmation page, so it carries the original list forward
	// in a hidden field.
	back := safeReturn(r.FormValue("return"))
	if back == "/" {
		if ref, err := url.Parse(r.Referer()); err == nil && ref.Path != "" {
			candidate := ref.Path
			if ref.RawQuery != "" {
				candidate += "?" + ref.RawQuery
			}
			back = safeReturn(candidate)
		}
	}
	if len(ids) == 0 {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	switch r.FormValue("action") {
	case "delete":
		s.render(w, "Delete items", s.bulkDeleteConfirm(r, ids, back), "")
		return
	case "delete-confirm":
		if err := s.bulkDelete(r.Context(), ids); err != nil {
			s.internalError(w, err)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	default:
		_ = s.store.BulkUpdate(r.Context(), ids, r.FormValue("action"))
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
}

// bulkDeleteConfirm renders the confirmation page for a bulk delete.
func (s *Server) bulkDeleteConfirm(r *http.Request, ids []int64, back string) string {
	token := template.HTMLEscapeString(csrf(r))
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	fmt.Fprintf(&b, `<h1>Delete %d item%s?</h1><p class="subtitle">This permanently removes them, their highlights, notes, tags, and snapshots. It cannot be undone.</p>`, len(ids), plural(len(ids)))
	b.WriteString(`<ul class="items">`)
	for _, id := range ids {
		item, err := s.store.Item(r.Context(), id)
		title := "Item " + strconv.FormatInt(id, 10)
		if err == nil && item.Title != "" {
			title = item.Title
		}
		fmt.Fprintf(&b, `<li><div class="item-main"><span>%s</span></div></li>`, template.HTMLEscapeString(title))
	}
	b.WriteString(`</ul>`)
	fmt.Fprintf(&b, `<form method="post" action="/items/bulk"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="action" value="delete-confirm"><input type="hidden" name="return" value="%s">`, token, template.HTMLEscapeString(back))
	for _, id := range ids {
		fmt.Fprintf(&b, `<input type="hidden" name="item" value="%d">`, id)
	}
	fmt.Fprintf(&b, `<div class="bulk-actions"><button class="danger-btn" type="submit">Delete %d item%s</button><a class="button" href="%s">Cancel</a></div></form>`,
		len(ids), plural(len(ids)), template.HTMLEscapeString(back))
	return b.String()
}

// bulkDelete removes the selected items and their snapshot files.
func (s *Server) bulkDelete(ctx context.Context, ids []int64) error {
	paths, err := s.store.SnapshotPaths(ctx, ids)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if s.cfg.SnapshotsDir != "" && filepath.Dir(p) == filepath.Clean(s.cfg.SnapshotsDir) {
			_ = os.Remove(p)
		}
	}
	return s.store.DeleteItems(ctx, ids)
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request, _ string) {
	s.settingsPage(w, r, settingsNotices{})
}

// settingsNotices carries an inline status message for one section of the
// Settings page, so a failed sub-form (change password, rename/merge a tag)
// can be re-rendered in place instead of redirecting through a query param.
type settingsNotices struct {
	password, tags string
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, notices settingsNotices) {
	token := template.HTMLEscapeString(csrf(r))
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Settings</h1>`)
	b.WriteString(`<section class="dash-section"><h2>Account</h2>`)
	if notices.password != "" {
		fmt.Fprintf(&b, `<p class="note">%s</p>`, template.HTMLEscapeString(notices.password))
	}
	fmt.Fprintf(&b, `<form class="stacked" method="post" action="/settings/password"><input type="hidden" name="csrf_token" value="%s"><label>Current password <input type="password" name="current_password" required autocomplete="current-password"></label><label>New password (at least 12 characters) <input type="password" name="new_password" required minlength="12" autocomplete="new-password"></label><button class="primary">Change password</button></form></section>`, token)
	b.WriteString(`<section class="dash-section"><h2>Tags</h2>`)
	if notices.tags != "" {
		fmt.Fprintf(&b, `<p class="note">%s</p>`, template.HTMLEscapeString(notices.tags))
	}
	if counts, err := s.store.AllTagCounts(r.Context()); err == nil && len(counts) > 0 {
		b.WriteString(`<p class="tagbar">`)
		for _, c := range counts {
			fmt.Fprintf(&b, `<span class="%s">%s (%d)</span> `, tagChipClass(c.Name), template.HTMLEscapeString(c.Name), c.Count)
		}
		b.WriteString(`</p>`)
	}
	fmt.Fprintf(&b, `<form class="stacked" method="post" action="/settings/tags/rename"><input type="hidden" name="csrf_token" value="%s"><label>Rename tag <input name="from" required placeholder="old name"></label><label>To <input name="to" required placeholder="new name"></label><button>Rename</button></form>`, token)
	fmt.Fprintf(&b, `<form class="stacked" method="post" action="/settings/tags/merge"><input type="hidden" name="csrf_token" value="%s"><label>Merge tag <input name="from" required placeholder="tag to remove"></label><label>Into <input name="into" required placeholder="tag to keep"></label><button>Merge</button></form></section>`, token)
	b.WriteString(`<section class="dash-section"><h2>Import</h2><p><a href="/import">Import data</a> from Pocket, Instapaper, linkding, Readeck, or a browser bookmarks file.</p></section>`)
	b.WriteString(`<section class="dash-section"><h2>Export</h2><p>Download <a href="/export.json">everything as JSON</a> or <a href="/export.opml">feeds as OPML</a>.</p>`)
	fmt.Fprintf(&b, `<form method="post" action="/export/markdown"><input type="hidden" name="csrf_token" value="%s"><button>Export Markdown to the configured folder</button></form></section>`, token)
	base := s.baseURL(r)
	// A session-based bookmarklet: it opens the pre-filled save form, so no API
	// token is ever embedded in the bookmark. baseURL already rejects hosts with
	// attribute/JS-breaking characters; escape again as defense in depth.
	escBase := template.HTMLEscapeString(base)
	bookmarklet := "javascript:(function(){window.open('" + escBase + "/share?url='+encodeURIComponent(location.href)+'&amp;title='+encodeURIComponent(document.title),'scrimshaw','width=480,height=680');})();"
	b.WriteString(`<section class="dash-section"><h2>Save from anywhere</h2>`)
	if base != "" {
		b.WriteString(`<p>Drag this to your bookmarks bar, then click it on any page to save that page (it uses your logged-in session, no token needed):</p>`)
		b.WriteString(`<p><a class="button bookmarklet" href="` + bookmarklet + `">Save to Scrimshaw</a></p>`)
		b.WriteString(`<p class="note">On iOS Safari: bookmark any page, then edit that bookmark's address and paste the snippet above.</p>`)
		fmt.Fprintf(&b, `<p><strong>iOS Shortcut</strong> (adds Scrimshaw to the Share Sheet): make a Shortcut that accepts URLs, then "Get Contents of URL" with method <code>POST</code> to <code>%s/api/save</code>, header <code>Authorization: Bearer &lt;your write token&gt;</code>, and JSON body <code>{"url": Shortcut Input, "read_later": true}</code>. Create the token under <a href="/tokens">API tokens</a> with the write scope.</p>`, template.HTMLEscapeString(base))
	}
	b.WriteString(`<p>The <strong>browser extension</strong> in <code>extension/</code> adds a toolbar button and a right-click "Save to Scrimshaw" menu.</p></section>`)
	b.WriteString(`<section class="dash-section"><h2>API and integrations</h2><p><a href="/tokens">API tokens</a> for the browser extension, an Obsidian plugin, and publishing shared links to your website.</p></section>`)
	fmt.Fprintf(&b, `<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="%s"><button>Log out</button></form>`, token)
	s.render(w, "Settings", b.String(), "")
}

// changePassword requires the current password, re-hashes the new one, and
// logs out every session (including this one) so a stolen session cookie
// stops working the moment the password is rotated.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, _ string) {
	newPassword := r.FormValue("new_password")
	if len(newPassword) < 12 {
		s.settingsPage(w, r, settingsNotices{password: "New password must contain at least 12 characters."})
		return
	}
	hash, err := s.store.UserPasswordHash(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("current_password"))) != nil {
		s.settingsPage(w, r, settingsNotices{password: "Current password is incorrect."})
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.SetUserPasswordHash(r.Context(), string(newHash)); err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.DeleteAllSessions(r.Context()); err != nil {
		s.internalError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "scrimshaw_session", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) renameTag(w http.ResponseWriter, r *http.Request, _ string) {
	from, to := r.FormValue("from"), r.FormValue("to")
	if err := s.store.RenameTag(r.Context(), from, to); err != nil {
		s.settingsPage(w, r, settingsNotices{tags: err.Error()})
		return
	}
	s.settingsPage(w, r, settingsNotices{tags: fmt.Sprintf("Renamed %q to %q.", from, to)})
}

func (s *Server) mergeTag(w http.ResponseWriter, r *http.Request, _ string) {
	from, into := r.FormValue("from"), r.FormValue("into")
	if err := s.store.MergeTag(r.Context(), from, into); err != nil {
		s.settingsPage(w, r, settingsNotices{tags: err.Error()})
		return
	}
	s.settingsPage(w, r, settingsNotices{tags: fmt.Sprintf("Merged %q into %q.", from, into)})
}

func feedFormBody(csrfToken string) string {
	return dashboardToolbar + `<div class="form-page"><h1>Add a feed</h1><form class="stacked" method="post" action="/feeds"><input type="hidden" name="csrf_token" value="` + csrfToken +
		`"><label>Feed URL <input type="url" name="url" required autofocus placeholder="https://example.com/feed.xml"></label><label>Tags <input name="tags" placeholder="comma-separated, optional"></label><button class="primary">Subscribe</button></form></div>`
}

func (s *Server) newFeed(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Add a feed", feedFormBody(template.HTMLEscapeString(csrf(r))), "")
}
func (s *Server) createFeed(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := s.store.AddFeed(r.Context(), r.FormValue("url"), time.Hour, strings.Split(r.FormValue("tags"), ","))
	if err != nil {
		s.log.Warn("feed subscription failed", "error", err)
		s.render(w, "Add a feed", feedFormBody(template.HTMLEscapeString(csrf(r))), "Could not subscribe. Check that the URL is valid.")
		return
	}
	// Best-effort and one-time: not finding a favicon is a normal outcome for
	// many sites (the UI falls back to a monogram), not something to retry or
	// surface to the user.
	if s.cfg.Fetcher != nil {
		if favicon := feeds.DiscoverFavicon(r.Context(), s.cfg.Fetcher, r.FormValue("url")); favicon != "" {
			if err := s.store.SetFeedFavicon(r.Context(), id, favicon); err != nil {
				s.log.Warn("store feed favicon failed", "feed_id", id, "error", err)
			}
		}
	}
	http.Redirect(w, r, "/feeds", http.StatusSeeOther)
}

var refreshIntervals = []struct {
	Seconds int
	Label   string
}{
	{900, "Every 15 minutes"}, {1800, "Every 30 minutes"}, {3600, "Every hour"},
	{10800, "Every 3 hours"}, {21600, "Every 6 hours"}, {43200, "Every 12 hours"}, {86400, "Once a day"},
}

func intervalLabel(d time.Duration) string {
	secs := int(d.Seconds())
	for _, o := range refreshIntervals {
		if o.Seconds == secs {
			return o.Label
		}
	}
	return fmt.Sprintf("Every %d min", secs/60)
}

func (s *Server) feedsList(w http.ResponseWriter, r *http.Request, _ string) {
	feeds, err := s.store.AllFeeds(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	token := template.HTMLEscapeString(csrf(r))
	notice := ""
	if r.URL.Query().Get("refreshed") == "1" {
		notice = `<p class="note">Refreshed.</p>`
	}
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	b.WriteString(`<h1>Feeds</h1><p class="subtitle">Your subscriptions. They refresh automatically; each can be set below.</p>`)
	b.WriteString(`<div class="bulk-actions"><a class="button" href="/feeds/new">Add a feed</a><a class="button" href="/?view=feeds">Browse feed items</a>`)
	if len(feeds) > 0 {
		fmt.Fprintf(&b, `<form class="inline-action" method="post" action="/feeds/refresh"><input type="hidden" name="csrf_token" value="%s"><button>Refresh all now</button></form>`, token)
	}
	b.WriteString(`</div>`)
	b.WriteString(notice)
	if len(feeds) == 0 {
		b.WriteString(`<p class="note">No feeds yet. Add one to start.</p>`)
		s.render(w, "Feeds", b.String(), "")
		return
	}
	b.WriteString(`<ul class="feed-list">`)
	for _, f := range feeds {
		title := f.Title
		if title == "" {
			title = f.URL
		}
		meta := `<span class="feed-url">` + template.HTMLEscapeString(f.URL) + `</span> &middot; ` + intervalLabel(f.RefreshInterval)
		if f.Disabled {
			meta += ` <span class="badge broken">Disabled</span>`
		}
		if f.LastError != "" {
			meta += ` <span class="badge broken">` + template.HTMLEscapeString(truncate(f.LastError, 60)) + `</span>`
		}
		options := ""
		for _, o := range refreshIntervals {
			sel := ""
			if o.Seconds == int(f.RefreshInterval.Seconds()) {
				sel = " selected"
			}
			options += fmt.Sprintf(`<option value="%d"%s>%s</option>`, o.Seconds, sel, o.Label)
		}
		fmt.Fprintf(&b, `<li><div class="item-main"><a href="%s" rel="noopener noreferrer">%s%s</a><div class="item-meta">%s</div></div><div class="feed-actions"><form class="inline-action" method="post" action="/feeds/%d/refresh"><input type="hidden" name="csrf_token" value="%s"><button>Refresh</button></form><details class="feed-settings"><summary>Settings</summary><form class="stacked" method="post" action="/feeds/%d/settings"><input type="hidden" name="csrf_token" value="%s"><label>Refresh <select name="interval">%s</select></label><label><input type="checkbox" name="fetch_full_content" value="1"%s> Fetch the full article for each item</label><label><input type="checkbox" name="auto_snapshot" value="1"%s> Save an offline snapshot of each item</label><label>Content rules (one per line: <code>skip &lt;keyword or /regex/&gt;</code> or <code>tag:name &lt;keyword or /regex/&gt;</code>) <textarea name="rules" rows="3" placeholder="skip sponsored&#10;tag:golang /\bgo\b/">%s</textarea></label><button>Save settings</button></form><form method="post" action="/feeds/%d/delete"><input type="hidden" name="csrf_token" value="%s"><button class="danger-btn">Unsubscribe</button></form></details></div></li>`,
			template.HTMLEscapeString(f.URL), feedIcon(title, f.FaviconURL), template.HTMLEscapeString(title), meta,
			f.ID, token, f.ID, token, options, checkedAttr(f.FetchFullContent), checkedAttr(f.AutoSnapshot), template.HTMLEscapeString(f.Rules), f.ID, token)
	}
	b.WriteString(`</ul>`)
	s.render(w, "Feeds", b.String(), "")
}

func checkedAttr(on bool) string {
	if on {
		return " checked"
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

const tagPaletteSize = 6

// tagChipClass deterministically maps a tag name to one of a small fixed
// palette (app.css's --gl-tag-1..6), so tags are scannable at a glance
// without any per-tag color to configure or store.
func tagChipClass(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(name))))
	return fmt.Sprintf("tag-chip tag-c%d", h.Sum32()%tagPaletteSize+1)
}

// feedIcon renders a feed's real favicon (proxied through /images the same
// as any other fetched image — cached, SSRF-guarded, SVG rejected) when one
// was discovered at subscribe time, or a generated monogram — same hash
// palette as tagChipClass, for visual consistency — otherwise.
func feedIcon(title, faviconURL string) string {
	if faviconURL != "" {
		return fmt.Sprintf(`<img class="favicon" src="/images?url=%s" alt="" width="16" height="16" loading="lazy">`, url.QueryEscape(faviconURL))
	}
	letter := "?"
	if r := []rune(strings.TrimSpace(title)); len(r) > 0 {
		letter = strings.ToUpper(string(r[0]))
	}
	return fmt.Sprintf(`<span class="favicon-mono %s" aria-hidden="true">%s</span>`, tagChipClass(title), template.HTMLEscapeString(letter))
}

func (s *Server) refreshFeed(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Feeds == nil {
		s.internalError(w, errors.New("feed service is not configured"))
		return
	}
	feed, err := s.store.Feed(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	_ = s.cfg.Feeds.RefreshFeed(r.Context(), feed)
	http.Redirect(w, r, "/feeds?refreshed=1", http.StatusSeeOther)
}

func (s *Server) refreshAllFeeds(w http.ResponseWriter, r *http.Request, _ string) {
	if s.cfg.Feeds == nil {
		s.internalError(w, errors.New("feed service is not configured"))
		return
	}
	_ = s.cfg.Feeds.RefreshAll(r.Context())
	http.Redirect(w, r, "/feeds?refreshed=1", http.StatusSeeOther)
}

func (s *Server) feedSettings(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	seconds, _ := strconv.Atoi(r.FormValue("interval"))
	if err := s.store.SetFeedRefresh(r.Context(), id, time.Duration(seconds)*time.Second, r.FormValue("fetch_full_content") == "1", r.FormValue("auto_snapshot") == "1"); err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.SetFeedRules(r.Context(), id, r.FormValue("rules")); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/feeds", http.StatusSeeOther)
}

func (s *Server) deleteFeed(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteFeed(r.Context(), id); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/feeds", http.StatusSeeOther)
}

func (s *Server) setTags(w http.ResponseWriter, r *http.Request, _ string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetItemTags(r.Context(), id, strings.Split(r.FormValue("tags"), ",")); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request, _ string) {
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
	// Remove the snapshot file too, but only from inside the snapshots directory.
	if item.SnapshotPath.Valid && s.cfg.SnapshotsDir != "" && filepath.Dir(item.SnapshotPath.String) == filepath.Clean(s.cfg.SnapshotsDir) {
		_ = os.Remove(item.SnapshotPath.String)
	}
	if err := s.store.DeleteItem(r.Context(), id); err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, safeReturn(r.FormValue("return")), http.StatusSeeOther)
}

func saveForm(csrfToken, rawURL, tags string) string {
	return dashboardToolbar + `<div class="form-page"><h1>Add a link</h1>` +
		`<form class="stacked" method="post" action="/save"><input type="hidden" name="csrf_token" value="` + csrfToken + `">` +
		`<label>URL <input type="url" name="url" required autofocus placeholder="https://example.com/article" value="` + template.HTMLEscapeString(rawURL) + `"></label>` +
		`<label>Tags <input name="tags" placeholder="comma-separated, optional" value="` + template.HTMLEscapeString(tags) + `"></label>` +
		`<fieldset class="choice"><legend>How to save it</legend>` +
		`<label class="radio"><input type="radio" name="read_later" value="1" checked><span><strong>Read later</strong><small>Fetch the full article so you can read and highlight it.</small></span></label>` +
		`<label class="radio"><input type="radio" name="read_later" value="0"><span><strong>Bookmark</strong><small>Just store the link in your bookmarks and linklog.</small></span></label>` +
		`</fieldset><button class="primary">Add</button></form></div>`
}

func (s *Server) newSave(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Add a link", saveForm(template.HTMLEscapeString(csrf(r)), "", ""), "")
}

// share is the target for the bookmarklet, the PWA share sheet, and an iOS
// Shortcut. Different sources put the URL in url, text, or title, so it looks
// in all three and pre-fills the save form for the logged-in user.
func (s *Server) share(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()
	rawURL := strings.TrimSpace(q.Get("url"))
	if rawURL == "" {
		rawURL = firstURL(q.Get("text"))
	}
	if rawURL == "" {
		rawURL = firstURL(q.Get("title"))
	}
	s.render(w, "Add a link", saveForm(template.HTMLEscapeString(csrf(r)), rawURL, q.Get("tags")), "")
}

func tokensFormBody(csrfToken string) string {
	return dashboardToolbar + `<div class="form-page"><h1>API tokens</h1><form class="stacked" method="post" action="/tokens"><input type="hidden" name="csrf_token" value="` + csrfToken +
		`"><label>Name <input name="name" required placeholder="e.g. Obsidian, Website"></label><fieldset class="choice"><legend>Scopes</legend><label class="radio"><input type="checkbox" name="scope" value="read" checked><span><strong>Read</strong><small>Retrieve items, highlights, and the shared linklog.</small></span></label><label class="radio"><input type="checkbox" name="scope" value="write"><span><strong>Write</strong><small>Save pages, mark read, and add highlights.</small></span></label></fieldset><button class="primary">Create token</button></form><p class="note">Give an Obsidian plugin a read+write token; give your website a read-only token.</p></div>`
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "API tokens", tokensFormBody(template.HTMLEscapeString(csrf(r))), "")
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, _ string) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.render(w, "API tokens", tokensFormBody(template.HTMLEscapeString(csrf(r))), "Token name is required.")
		return
	}
	var scopes []string
	for _, scope := range r.Form["scope"] {
		if scope == "read" || scope == "write" {
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		s.render(w, "API tokens", tokensFormBody(template.HTMLEscapeString(csrf(r))), "Select at least one scope.")
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
	body := dashboardToolbar + `<div class="form-page"><h1>Token created</h1><p>Copy this token now. It will not be shown again. Scopes: ` + template.HTMLEscapeString(strings.Join(scopes, ", ")) + `</p><p><code class="token">` + template.HTMLEscapeString(token) + `</code></p>`
	if hasScope(strings.Join(scopes, " "), "read") {
		feedURL := s.baseURL(r) + "/feed.xml?token=" + url.QueryEscape(token)
		body += `<p>This token can also subscribe to your shared linklog in any feed reader:</p><p><code class="token">` + template.HTMLEscapeString(feedURL) + `</code></p>`
	}
	body += `</div>`
	s.render(w, "API token", body, "")
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
	return s.scopesForToken(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// scopesForToken looks up a raw (unhashed) token's scopes directly, for
// callers that can't use an Authorization header — currently just feedXML,
// since feed readers have no way to send one.
func (s *Server) scopesForToken(ctx context.Context, token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	scopes, err := s.store.TokenScopes(ctx, base64.RawURLEncoding.EncodeToString(sum[:]))
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

// Atom 1.0 types, marshaled via encoding/xml rather than hand-built strings
// like the rest of this file — XML escaping (especially inside
// type="html" content, where the escaped text IS the payload a feed reader
// unescapes back into markup) is easy to get subtly wrong by hand.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title     string      `xml:"title"`
	ID        string      `xml:"id"`
	Link      atomLink    `xml:"link"`
	Published string      `xml:"published"`
	Updated   string      `xml:"updated"`
	Author    *atomAuthor `xml:"author,omitempty"`
	Content   atomContent `xml:"content"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

// feedXML serves the shared linklog/reading log as Atom, for subscribing in
// any feed reader rather than polling /api/shared as JSON. RSS readers can't
// send an Authorization header, so — unlike every other /api/* route — the
// read-scoped token travels in the URL (?token=...); a missing or
// insufficiently scoped token 404s rather than 401s, since this is meant to
// be an unguessable URL a feed reader polls unattended, not an interactive
// auth prompt.
func (s *Server) feedXML(w http.ResponseWriter, r *http.Request) {
	if !hasScope(s.scopesForToken(r.Context(), r.URL.Query().Get("token")), "read") {
		http.NotFound(w, r)
		return
	}
	items, _, err := s.store.ListPage(r.Context(), store.ListOptions{
		Shared: "1", ReadLater: r.URL.Query().Get("read_later"), Bookmarked: r.URL.Query().Get("bookmarked"), PerPage: 100,
	})
	if err != nil {
		s.internalError(w, err)
		return
	}
	base := s.baseURL(r)
	feed := atomFeed{
		Title:   "Scrimshaw — Shared",
		ID:      base + "/feed.xml",
		Updated: time.Now().UTC().Format(time.RFC3339),
		Links:   []atomLink{{Href: base + "/"}, {Href: base + r.URL.RequestURI(), Rel: "self"}},
	}
	for _, item := range items {
		updated := item.AddedAt.UTC().Format(time.RFC3339)
		entry := atomEntry{
			Title:     item.Title,
			ID:        item.URL,
			Link:      atomLink{Href: item.URL},
			Published: updated,
			Updated:   updated,
			Content:   atomContent{Type: "html", Body: sanitize.HTML(item.ExtractedText)},
		}
		if item.Author != "" {
			entry.Author = &atomAuthor{Name: item.Author}
		}
		feed.Entries = append(feed.Entries, entry)
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		s.log.Warn("encode feed.xml failed", "error", err)
	}
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
	if _, err := s.store.Item(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	} else if err != nil {
		s.internalError(w, err)
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
	if _, err := s.store.Item(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	} else if err != nil {
		s.internalError(w, err)
		return
	}
	if err := s.store.AddHighlight(r.Context(), id, request.Quote, request.Note, 0); err != nil {
		http.Error(w, "a highlight needs selected text or a note", http.StatusBadRequest)
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

func netscapeFormBody(csrfToken string) string {
	return dashboardToolbar + `<div class="form-page"><h1>Import bookmarks</h1><form class="stacked" method="post" action="/import/netscape" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="` + csrfToken +
		`"><label>Bookmarks HTML <input type="file" name="bookmarks" accept=".html,text/html" required></label><button class="primary">Import</button></form></div>`
}

func (s *Server) netscapeImportForm(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Import bookmarks", netscapeFormBody(template.HTMLEscapeString(csrf(r))), "")
}

func (s *Server) netscapeImport(w http.ResponseWriter, r *http.Request, _ string) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		s.render(w, "Import bookmarks", netscapeFormBody(template.HTMLEscapeString(csrf(r))), "That upload was not valid.")
		return
	}
	file, _, err := r.FormFile("bookmarks")
	if err != nil {
		s.render(w, "Import bookmarks", netscapeFormBody(template.HTMLEscapeString(csrf(r))), "Choose a bookmarks file to import.")
		return
	}
	defer file.Close()
	count, err := importers.NetscapeBookmarks(r.Context(), file, s.store)
	if err != nil {
		s.log.Warn("bookmark import failed", "error", err)
		s.render(w, "Import bookmarks", netscapeFormBody(template.HTMLEscapeString(csrf(r))), "Could not import the bookmark file.")
		return
	}
	s.render(w, "Import bookmarks", dashboardToolbar+`<h1>Import bookmarks</h1><p class="note">Imported `+strconv.Itoa(count)+` bookmarks.</p>`, "")
}

func importFormBody(csrfToken string) string {
	return dashboardToolbar + `<div class="form-page"><h1>Import data</h1><form class="stacked" method="post" action="/import" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="` + csrfToken +
		`"><label>Format <select name="format"><option value="netscape">Netscape bookmarks (Pocket, browsers)</option><option value="instapaper">Instapaper CSV</option><option value="linkding">Linkding JSON</option><option value="readeck">Readeck JSON</option><option value="opml">OPML feeds</option></select></label><label>File <input type="file" name="file" required></label><button class="primary">Import</button></form></div>`
}

func (s *Server) importForm(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Import data", importFormBody(template.HTMLEscapeString(csrf(r))), "")
}

func (s *Server) importFile(w http.ResponseWriter, r *http.Request, _ string) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		s.render(w, "Import data", importFormBody(template.HTMLEscapeString(csrf(r))), "That upload was not valid.")
		return
	}
	format := r.FormValue("format")
	file, _, err := r.FormFile("file")
	if err != nil {
		s.render(w, "Import data", importFormBody(template.HTMLEscapeString(csrf(r))), "Choose a file to import.")
		return
	}
	defer file.Close()
	// OPML feeds get a preview step so you can tag each feed before importing.
	if format == "opml" {
		feeds, err := importers.ParseOPML(file)
		if err != nil || len(feeds) == 0 {
			s.render(w, "Import data", importFormBody(template.HTMLEscapeString(csrf(r))), "No feeds found in that OPML file.")
			return
		}
		s.render(w, "Import feeds", s.opmlPreviewBody(csrf(r), feeds), "")
		return
	}
	count, err := importers.Import(r.Context(), format, file, s.store)
	if err != nil {
		s.log.Warn("import failed", "format", format, "error", err)
		s.render(w, "Import data", importFormBody(template.HTMLEscapeString(csrf(r))), "Could not import that file. Check the format and try again.")
		return
	}
	s.render(w, "Import data", dashboardToolbar+`<h1>Import data</h1><p class="note">Imported `+strconv.Itoa(count)+` items.</p>`, "")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *Server) opmlPreviewBody(csrfToken string, feeds []importers.OPMLFeed) string {
	token := template.HTMLEscapeString(csrfToken)
	var b strings.Builder
	b.WriteString(dashboardToolbar)
	fmt.Fprintf(&b, `<h1>Import feeds</h1><p class="subtitle">%d feed%s found. Add tags to any of them, then import.</p>`, len(feeds), plural(len(feeds)))
	fmt.Fprintf(&b, `<form method="post" action="/import/opml"><input type="hidden" name="csrf_token" value="%s"><ul class="opml-preview">`, token)
	for _, f := range feeds {
		title := f.Title
		if title == "" {
			title = f.URL
		}
		fmt.Fprintf(&b, `<li><div class="item-main"><span class="opml-title">%s</span><span class="feed-url">%s</span></div><input type="hidden" name="url" value="%s"><input name="tags" placeholder="tags, optional" aria-label="Tags for %s"></li>`,
			template.HTMLEscapeString(title), template.HTMLEscapeString(f.URL), template.HTMLEscapeString(f.URL), template.HTMLEscapeString(title))
	}
	b.WriteString(`</ul><button class="primary">Import feeds</button> <a class="button" href="/import">Cancel</a></form>`)
	return b.String()
}

func (s *Server) importOPML(w http.ResponseWriter, r *http.Request, _ string) {
	urls := r.Form["url"]
	tags := r.Form["tags"]
	imported := 0
	for i, rawURL := range urls {
		tagStr := ""
		if i < len(tags) {
			tagStr = tags[i]
		}
		if _, err := s.store.AddFeed(r.Context(), rawURL, time.Hour, strings.Split(tagStr, ",")); err == nil {
			imported++
		}
	}
	s.render(w, "Import feeds", dashboardToolbar+fmt.Sprintf(`<h1>Import feeds</h1><p class="note">Imported %d feed%s. <a href="/feeds">Manage feeds</a>.</p>`, imported, plural(imported)), "")
}

func (s *Server) saveURL(w http.ResponseWriter, r *http.Request, _ string) {
	if s.cfg.Saver == nil {
		s.internalError(w, errors.New("manual saver is not configured"))
		return
	}
	rawURL := r.FormValue("url")
	tags := strings.Split(r.FormValue("tags"), ",")
	readLater := r.FormValue("read_later") == "1"
	var id int64
	var err error
	if readLater {
		id, err = s.cfg.Saver.Save(r.Context(), rawURL, tags)
	} else {
		id, err = s.cfg.Saver.SaveLink(r.Context(), rawURL, tags)
	}
	if errors.Is(err, store.ErrItemExists) {
		id, err = s.mergeIntoExisting(r.Context(), rawURL, readLater)
	}
	if err != nil {
		s.log.Warn("page save failed", "error", err)
		s.render(w, "Add a link", saveForm(template.HTMLEscapeString(csrf(r)), rawURL, r.FormValue("tags")), "Could not save that URL. Check it and try again.")
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// mergeIntoExisting handles saving a URL that's already in the datastore (as a
// feed item or an earlier manual save): rather than erroring, it flips the
// requested flag on the existing item and, when promoting to read later,
// best-effort extracts full content if the item doesn't have any yet — the
// same tolerance readLaterItem already applies, so a bot-blocked or slow
// extraction doesn't turn a duplicate-URL save into a hard failure either.
func (s *Server) mergeIntoExisting(ctx context.Context, rawURL string, readLater bool) (int64, error) {
	id, err := s.store.ItemIDByURL(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	if readLater {
		if err := s.store.PromoteToReadLater(ctx, id); err != nil {
			return 0, err
		}
		if item, err := s.store.Item(ctx, id); err == nil && item.ExtractedText == "" && s.cfg.Saver != nil {
			if err := s.cfg.Saver.Extract(ctx, id, item.URL); err != nil {
				s.log.Warn("merge into existing: extract failed", "item", id, "error", err)
			}
		}
		return id, nil
	}
	if err := s.store.SetBookmarked(ctx, id, true); err != nil {
		return 0, err
	}
	return id, nil
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

// shortcutsHelpDialog documents the exact shortcuts wired in app.js's
// keyboardNav — the source of truth is the code, not SPEC.md's wishlist.
const shortcutsHelpDialog = `<dialog id="shortcuts-help" class="shortcuts-help"><h2>Keyboard shortcuts</h2><dl>
<dt><kbd>j</kbd> <kbd>k</kbd></dt><dd>Next / previous item</dd>
<dt><kbd>o</kbd></dt><dd>Open the focused item</dd>
<dt><kbd>/</kbd></dt><dd>Focus search</dd>
<dt><kbd>g</kbd> <kbd>a</kbd></dt><dd>Go home</dd>
<dt><kbd>g</kbd> <kbd>f</kbd></dt><dd>Add a feed</dd>
<dt><kbd>m</kbd></dt><dd>Mark read or unread (in the reader)</dd>
<dt><kbd>s</kbd></dt><dd>Star (in the reader)</dd>
<dt><kbd>v</kbd></dt><dd>Open the original page (in the reader)</dd>
<dt><kbd>?</kbd></dt><dd>Show this help</dd>
</dl><button type="button" class="shortcuts-help-close">Close</button></dialog>`

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><link rel="manifest" href="/manifest.webmanifest"><link rel="stylesheet" href="/app.css"><title>{{.Title}} | Scrimshaw</title></head><body><header class="topbar"><div class="bar-inner"><a class="brand" href="/">Scrimshaw</a><a class="brand-add" href="/save">Add a link</a></div></header><main class="container">{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{.Body}}</main>` + shortcutsHelpDialog + `<script src="/app.js" defer></script></body></html>`))

// starButtonAttr marks a toggle button as active. Active toggles use a quiet
// tinted treatment so the one solid-accent primary action stays the rare mark.
func starButtonAttr(active bool) string {
	if active {
		return ` class="toggle-on"`
	}
	return ""
}

// linkBroken reports whether a bookmarked link failed its last reachability
// check. Only bookmarks are link-checked (matching Stats.BrokenLinks).
func linkBroken(item store.Item) bool {
	return item.Bookmarked && (item.LinkStatus >= 400 || item.LinkStatus < 0)
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

// itemMeta renders an item's row meta line (author, age, badges) — the one
// place both the full list view and the dashboard's condensed sections
// build it, so the same item looks the same regardless of where it's shown.
func itemMeta(item store.Item, showSource, showReadBadge bool) string {
	meta := ""
	if item.Author != "" {
		meta += `<span class="author">` + template.HTMLEscapeString(item.Author) + `</span>`
	}
	meta += timeTag(item.AddedAt)
	if showSource {
		meta += itemKindBadges(item)
	}
	if showReadBadge && item.ReadState == "read" {
		meta += ` <span class="badge read">Read</span>`
	}
	if item.Starred {
		meta += ` <span class="badge star">Starred</span>`
	}
	if item.Shared {
		meta += ` <span class="badge shared">Shared</span>`
	}
	if linkBroken(item) {
		meta += ` <span class="badge broken">Broken link</span>`
	}
	return meta
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

// timeTag renders a machine-readable <time> element, or "" for a zero time.
func timeTag(t time.Time) string {
	rel := relativeTime(t)
	if rel == "" {
		return ""
	}
	return `<time datetime="` + t.UTC().Format(time.RFC3339) + `">` + rel + `</time>`
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
