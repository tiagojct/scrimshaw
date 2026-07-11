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
	mux.HandleFunc("POST /items/{id}/highlights", s.withSession(s.highlight))
	mux.HandleFunc("POST /items/bulk", s.withSession(s.bulk))
	mux.HandleFunc("GET /feeds/new", s.withSession(s.newFeed))
	mux.HandleFunc("POST /feeds", s.withSession(s.createFeed))
	mux.HandleFunc("GET /save", s.withSession(s.newSave))
	mux.HandleFunc("POST /save", s.withSession(s.saveURL))
	mux.HandleFunc("GET /share", s.withSession(s.share))
	mux.HandleFunc("GET /tokens", s.withSession(s.tokens))
	mux.HandleFunc("POST /tokens", s.withSession(s.createToken))
	mux.HandleFunc("POST /api/save", s.apiSave)
	mux.HandleFunc("OPTIONS /api/save", s.apiOptions)
	mux.HandleFunc("GET /api/items", s.apiItems)
	mux.HandleFunc("GET /api/feeds", s.apiFeeds)
	mux.HandleFunc("GET /api/search", s.apiSearch)
	mux.HandleFunc("GET /api/highlights", s.apiHighlights)
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
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	options := store.ListOptions{
		Tag: r.URL.Query().Get("tag"), State: r.URL.Query().Get("state"),
		ItemType: r.URL.Query().Get("type"), Sort: r.URL.Query().Get("sort"),
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
	var b strings.Builder
	b.WriteString(`<nav class="toolbar"><a href="/feeds/new">Subscribe to a feed</a><a href="/save">Save a page</a><a href="/highlights">Highlights</a><a href="/import">Import data</a><a href="/tokens">API tokens</a><a href="/export.json">JSON export</a><a href="/export.opml">OPML export</a></nav>`)
	b.WriteString(`<nav class="views">` +
		viewTab("/", "Inbox", "", options.State) +
		viewTab("/?state=unread", "Unread", "unread", options.State) +
		viewTab("/?state=starred", "Saved", "starred", options.State) +
		viewTab("/?state=archived", "Archived", "archived", options.State) +
		`</nav>`)
	fmt.Fprintf(&b, `<div class="filters"><form action="/search"><label>Search <input name="q" type="search" placeholder="Search everything"></label><button>Search</button></form><form action="/"><input type="hidden" name="state" value="%s"><label>Sort <select name="sort">%s%s%s</select></label><button>Apply</button></form></div>`,
		template.HTMLEscapeString(options.State),
		optionTag("", "Newest", options.Sort), optionTag("oldest", "Oldest", options.Sort), optionTag("unread", "Unread first", options.Sort))
	if len(tagCounts) > 0 {
		b.WriteString(`<p class="tagbar">Unread tags: `)
		for _, count := range tagCounts {
			fmt.Fprintf(&b, `<a href="/?tag=%s">%s (%d)</a>`, url.QueryEscape(count.Name), template.HTMLEscapeString(count.Name), count.Count)
		}
		b.WriteString(`</p>`)
	}
	if len(items) == 0 {
		b.WriteString(`<p class="note">Nothing here yet.</p>`)
	}
	b.WriteString(`<form method="post" action="/items/bulk"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf(r)) + `"><ul class="items">`)
	for _, item := range items {
		classes := template.HTMLEscapeString(item.ReadState)
		if item.Starred {
			classes += " starred"
		}
		badges := ""
		if item.Starred {
			badges += ` <span class="badge star">Saved</span>`
		}
		if item.Archived {
			badges += ` <span class="badge">Archived</span>`
		}
		fmt.Fprintf(&b, `<li class="%s"><input type="checkbox" name="item" value="%d" aria-label="Select item"> <a href="/items/%d">%s</a> <small>%s</small>%s</li>`, classes, item.ID, item.ID, template.HTMLEscapeString(item.Title), template.HTMLEscapeString(item.Author), badges)
	}
	b.WriteString(`</ul><div class="bulk-actions"><button name="action" value="read">Mark selected read</button><button name="action" value="archive">Archive selected</button></div></form>`)
	if options.Page > 1 || options.Page*options.PerPage < total {
		b.WriteString(`<div class="pager">`)
		if options.Page > 1 {
			fmt.Fprintf(&b, `<a href="/?page=%d">Previous</a>`, options.Page-1)
		}
		if options.Page*options.PerPage < total {
			fmt.Fprintf(&b, `<a href="/?page=%d">Next</a>`, options.Page+1)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<form method="post" action="/logout"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf(r)) + `"><button>Log out</button></form>`)
	s.render(w, "Items", b.String(), "")
}
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
	state := "read"
	if item.ReadState == "read" {
		state = "unread"
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
	if item.Starred {
		badges.WriteString(` &middot; <span class="badge star">Saved</span>`)
	}
	if item.Archived {
		badges.WriteString(` &middot; <span class="badge">Archived</span>`)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<article data-item-id="%d"><h1>%s</h1><p class="meta"><a class="original-link" href="%s" rel="noopener noreferrer">Open original</a>%s%s</p><div class="reader">%s</div></article>`,
		item.ID, template.HTMLEscapeString(item.Title), template.HTMLEscapeString(item.URL), snapshotLink, badges.String(), sanitize.HTML(item.ExtractedText))
	starValue, starLabel := "1", "Save"
	if item.Starred {
		starValue, starLabel = "0", "Saved"
	}
	archiveValue, archiveLabel := "1", "Archive"
	if item.Archived {
		archiveValue, archiveLabel = "0", "Move to inbox"
	}
	fmt.Fprintf(&b, `<div class="reader-actions"><form class="read-form" method="post" action="/items/%d/read"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="state" value="%s"><button>Mark %s</button></form><form class="star-form" method="post" action="/items/%d/star"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="starred" value="%s"><button%s>%s</button></form><form class="archive-form" method="post" action="/items/%d/archive"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="archived" value="%s"><button>%s</button></form></div>`,
		item.ID, token, state, state,
		item.ID, token, starValue, starButtonAttr(item.Starred), starLabel,
		item.ID, token, archiveValue, archiveLabel)
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
	if err = s.store.SetReadState(r.Context(), id, r.FormValue("state")); err != nil {
		s.internalError(w, err)
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
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/items/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
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
		fmt.Fprintf(&b, `<li><a href="/items/%d">%s</a> <small>%s</small></li>`, item.ID, template.HTMLEscapeString(item.Title), template.HTMLEscapeString(item.Author))
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
	highlights, err := s.store.ListHighlights(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	var b strings.Builder
	b.WriteString(`<section class="highlight-list"><h1>Highlights</h1>`)
	if len(highlights) == 0 {
		b.WriteString(`<p class="note">No highlights yet. Open an article and select text to make one.</p></section>`)
		s.render(w, "Highlights", b.String(), "")
		return
	}
	b.WriteString(`<ul>`)
	for _, highlight := range highlights {
		fmt.Fprintf(&b, `<li><q>%s</q>`, template.HTMLEscapeString(highlight.Quote))
		if highlight.Note != "" {
			fmt.Fprintf(&b, `<p class="note">%s</p>`, template.HTMLEscapeString(highlight.Note))
		}
		fmt.Fprintf(&b, ` <a href="/items/%d">Open item</a></li>`, highlight.ItemID)
	}
	b.WriteString(`</ul></section>`)
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
	if err := s.store.BulkUpdate(r.Context(), ids, r.FormValue("action")); err != nil {
		s.render(w, "Items", "", "Select one or more items and an action.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
func (s *Server) newSave(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "Save page", `<form method="post" action="/save"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Page URL <input type="url" name="url" required autofocus></label><label>Tags, comma-separated <input name="tags"></label><button>Save and archive</button></form>`, "")
}
func (s *Server) share(w http.ResponseWriter, r *http.Request, _ string) {
	rawURL := r.URL.Query().Get("url")
	if err := fetch.ValidateURL(rawURL); err != nil {
		s.render(w, "Save page", "", "The shared URL is invalid.")
		return
	}
	s.render(w, "Save page", `<form method="post" action="/save"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Page URL <input type="url" name="url" required autofocus value="`+template.HTMLEscapeString(rawURL)+`"></label><label>Tags, comma-separated <input name="tags"></label><button>Save and archive</button></form>`, "")
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, _ string) {
	s.render(w, "API tokens", `<form method="post" action="/tokens"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf(r))+`"><label>Name <input name="name" required></label><button>Create save token</button></form>`, "")
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, _ string) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.render(w, "API tokens", "", "Token name is required.")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		s.internalError(w, err)
		return
	}
	sum := sha256.Sum256([]byte(token))
	if err := s.store.CreateAPIToken(r.Context(), name, base64.RawURLEncoding.EncodeToString(sum[:])); err != nil {
		s.internalError(w, err)
		return
	}
	s.render(w, "API token", `<p>Copy this token now. It will not be shown again.</p><code>`+template.HTMLEscapeString(token)+`</code>`, "")
}

func (s *Server) apiSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.cfg.Saver == nil {
		s.internalError(w, errors.New("manual saver is not configured"))
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		http.Error(w, "missing API token", http.StatusUnauthorized)
		return
	}
	sum := sha256.Sum256([]byte(token))
	valid, err := s.store.ValidAPIToken(r.Context(), base64.RawURLEncoding.EncodeToString(sum[:]))
	if err != nil || !valid {
		http.Error(w, "invalid API token", http.StatusUnauthorized)
		return
	}
	var request struct {
		URL  string   `json:"url"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	id, err := s.cfg.Saver.Save(r.Context(), request.URL, request.Tags)
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

func (s *Server) apiAuthorized(r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	valid, err := s.store.ValidAPIToken(r.Context(), base64.RawURLEncoding.EncodeToString(sum[:]))
	return err == nil && valid
}

func (s *Server) apiJSON(w http.ResponseWriter, r *http.Request, value any) {
	if !s.apiAuthorized(r) {
		http.Error(w, "invalid API token", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.internalError(w, err)
	}
}

func (s *Server) apiItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.AllItems(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.apiJSON(w, r, items)
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
	items, err := s.store.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, "invalid search query", http.StatusBadRequest)
		return
	}
	s.apiJSON(w, r, items)
}
func (s *Server) apiHighlights(w http.ResponseWriter, r *http.Request) {
	highlights, err := s.store.ListHighlights(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.apiJSON(w, r, highlights)
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
	id, err := s.cfg.Saver.Save(r.Context(), r.FormValue("url"), strings.Split(r.FormValue("tags"), ","))
	if err != nil {
		s.log.Warn("page save failed", "error", err)
		s.render(w, "Save page", "", "Could not save that page. Check the URL and try again.")
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

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><link rel="manifest" href="/manifest.webmanifest"><link rel="stylesheet" href="/app.css"><title>{{.Title}} | Scrimshaw</title></head><body><header class="masthead"><a class="brand" href="/">Scrimshaw</a></header>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}<main>{{.Body}}</main><script src="/app.js" defer></script></body></html>`))

func starButtonAttr(starred bool) string {
	if starred {
		return ` class="primary"`
	}
	return ""
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
