# CLAUDE.md

Guidance for working in the Scrimshaw codebase. SPEC.md is the full feature specification; read it for what to build. This file is the durable how: conventions, invariants, and the rules that must not be broken. Keep it short and current.

## What this is

Scrimshaw is a single-user, self-hosted Go application that unifies an RSS reader, a bookmarks archive, and a read-it-later reader into one SQLite datastore and one server-rendered interface. All three functions are equal citizens.

## Core principles

- One datastore. A feed entry, a bookmark, and a saved article are the same object: a URL with fetched content. They share the items table, tags, states, and search. Do not split them into separate tables or separate code paths.
- Server-rendered, no SPA. HTML comes from Go html/template. Interactivity is HTMX plus small vanilla JavaScript. There is no bundler, no npm, no build step for the frontend.
- Single static binary. The app must compile to one binary and run with only a data directory. Any dependency on an external process or system binary is out of scope for v1.
- Data portability is a feature, not an afterthought. Everything a user creates must be exportable in an open format, and snapshots stay as readable HTML files on disk.

## Technology

- Go, single static binary. SQLite via modernc.org/sqlite (pure Go, CGO_ENABLED=0; keep it CGO-free).
- SQLite with FTS5, WAL mode. Snapshots as single-file HTML on disk (internal/reader Saver); paths stored in SQLite.
- mmcdole/gofeed for feed parsing.
- go-shiori/go-readability for extraction.
- microcosm-cc/bluemonday for HTML sanitization.
- Plain CSS for theming, following the Glauca design system (~/github/glauca; Pruina light default, Profundum dark via prefers-color-scheme). CSS and JS live in internal/server/static/ (app.css, app.js), embedded and served same-origin so the CSP needs no inline exceptions. Design intent: pale ground, the accent (dies blue) is rare and load-bearing — at most one solid-accent primary action per view; active tabs/toggles use a quiet accent tint, badges are borderless tinted chips. Keep it minimal. The accent color's source of truth is internal/server/static/accent.json; `go generate ./internal/server/static/...` (gentokens) rewrites the marked `--gl-accent*` block in app.css from it — a Go tooling step, not a frontend build step, so app.css stays a plain static embedded file with no runtime theming. Tag colors (`--gl-tag-1..6`, tagChipClass in server.go) are the opposite pattern: no source file, a pure per-name hash at render time.

## Repository

Module path: github.com/tiagojct/scrimshaw. The repository root is the scrimshaw folder, and all paths below are relative to it.

## Layout

```
cmd/scrimshaw/      main, entrypoint, config loading; starts feed scheduler goroutine
internal/
  server/           routing, handlers, middleware, templates (auth, csrf, sessions)
  store/            sqlite access, migration runner, session/token storage
  feeds/            polling scheduler, dedup, failure lifecycle
  fetch/            safe http client, SSRF guard, readability
  sanitize/         bluemonday policy
  reader/           reader view assembly, snapshot saver
  export/           markdown, opml, json
  importers/        opml, instapaper, linkding, readeck, netscape
migrations/         ordered, versioned sql, embedded via go:embed
web/                pwa manifest, service worker (embedded)
extension/          browser extension (Manifest V3, unpacked)
obsidian/           obsidian plugin (plain JS, no build), consumes the JSON API
```

## Architecture pointers

- Routing: std-lib http.ServeMux with Go 1.22 method+pattern syntax in `Routes()` (internal/server/server.go). No router dependency.
- HTML: one root `pageTemplate` shell in internal/server/server.go (a sticky `.topbar` with the brand + quick "Add a link", then a centered `.container` main) links /app.css and /app.js; page bodies are built as escaped strings and injected as template.HTML. Static CSS/JS are embedded from internal/server/static/ and served by the `asset` handler. There is no html/template templates directory. Item rows are two-line (title + a meta line of author, `relativeTime` age, and tonal badges); badges carry a type class (`feed`/`later`/`bookmark`/`star`/`shared`/`broken`) styled in app.css.
- Highlights: the reader marks saved highlights in place (app.js walks text nodes and wraps them in `<mark class="hl">`) and lets you select article text to create one (a popover posts to /items/{id}/highlights). Saved quotes are passed to the page as a `<script type="application/json" id="hl-data">` island; json.Marshal escapes `<` so it is XSS-safe. A manual add form remains as the no-JS fallback.
- Three workflows over one items table (Miniflux + Linkding + Readeck). `read_later` and `bookmarked` are orthogonal boolean flags that apply to ANY item, including feed articles: Feeds = `source=feed`, Read Later = `read_later=1`, Bookmarks = `bookmarked=1`. Adding a link (`/save`) with the "Read later" box ticked runs the Saver (fetch + extract + snapshot, sets read_later); unticked runs `Saver.SaveLink` (link + title only, sets bookmarked). `Saver.extract` branches on `isPDF` (Content-Type, falling back to the `%PDF-` file signature) before readability, so a PDF URL gets real extracted text via `extractPDF` instead of failing HTML article extraction. Every item's reader has Read later and Bookmark toggle buttons (`/items/{id}/readlater`, `/items/{id}/bookmark`), so a feed article can be sent to either; promoting to read-later extracts in place if it has no content. Any extraction failure (bot-blocked page, PDF with no extractable text, etc.) falls back to a plain link rather than losing the save (see `Save`'s doc comment).
- Reading files an item away: `SetReadState('read')` also stamps `read_at` and archives; `'unread'` clears both and un-archives. So marking read removes an item from its active reading list and it shows up in Archived (still searchable). The reader's primary button is "Mark read" (returns to the item's list) or, when archived, "Move to inbox". Bookmarks and Starred are permanent collections, not queues: their views set `IncludeArchived` (and Starred filters `starred=1` directly), so a bookmarked or starred item stays listed even after it is read/archived. Read Later is a queue (no `IncludeArchived`), so there's exactly one read/archived pair per item, not one per workflow — two things follow from that: (1) `reader()` auto-marks a plain feed item (`source='feed'`, not also `ReadLater`/`Bookmarked`) read on open, matching every mainstream feed reader's "open = read" convention, but skips that for anything already claimed by Read Later or Bookmarks, so a peek doesn't silently drop it out of that queue; (2) `store.PromoteToReadLater` (used by `readLaterItem` and `mergeIntoExisting` instead of a bare `SetReadLater`) resets `read_state`/`read_at`/`archived` to unread in the same statement as setting the flag, since otherwise an item already archived (e.g. from being opened as a plain feed item) would flip `read_later=1` but never actually appear in the Read Later view. `POST /items/{id}/extract` (`extractItem`) is the standalone counterpart to the fetch-on-promote in both of those: full-text extraction without touching `read_later`/`bookmarked` at all, for reading the full article without committing it to any queue — its button relabels to "replace summary" for feed items, since those already carry the RSS description as `extracted_text` and the button's job there is upgrading it to a real extraction, not filling an empty field.
- Management: `/feeds` lists subscriptions (unsubscribe via `POST /feeds/{id}/delete`, which keeps items — feed_id is set null). Each feed has a refresh interval + fetch-full-content and auto-snapshot options (`POST /feeds/{id}/settings`; auto-snapshot stays default off). Manual refresh is `POST /feeds/{id}/refresh` (re-enables an auto-disabled feed first, then polls now via feeds.Service.RefreshFeed) and `POST /feeds/refresh` refreshes all; the server holds the feeds.Service via Config.Feeds. The reader edits tags (`POST /items/{id}/tags`, replace semantics via store.SetItemTags) and deletes an item (`POST /items/{id}/delete`, which cascades highlights/tags/FTS and removes the snapshot file).
- Image handling: `sanitize.HTML` routes every `<img>` through the `/images` proxy. It recovers a fetchable source before proxying — normalizing protocol-relative (`//host/...`) URLs to https, promoting lazy-load attributes (`data-src`/`data-original`/`data-lazy-src`) and `srcset` over placeholder/`data:` srcs — so CDN and lazy-loaded images are not dropped.
- Views are `?view=` tabs (`viewOrder`/`viewsByKey`): today, feeds, later, bookmarks, starred, archived (labelled "Read" — reading files items here), all; store.ListPage filters by Source/State/ReadLater/Bookmarked/Shared/Since/Until. Today spans every source and is computed per-request (not stored on the static itemView) as the local calendar day bounded against `COALESCE(published_at, added_at)`, so it also catches undated manual saves by their added time. Bare `/` renders the dashboard (`dashboard`), not a list. A read item shows a "Read" badge in collection views and stays in Bookmarks/Starred. Bulk actions are Mark read (or Mark unread in the Read view) and Delete selected — no bulk archive; `bulkDelete` removes items and their snapshot files. Star/share are reversible toggles posting an explicit `!= "0"` value. The main toolbar (`dashboardToolbar`) holds only frequent actions (Feeds, Add a feed, Search, Highlights, Settings); "Add a link" lives in the sticky topbar's quick-add slot instead, visible on every page. Import, exports, and API tokens live on `/settings`.
- `GET /triage` is a one-item-at-a-time way to burn down the Read Later queue, distinct from the list view. It walks unread read-later items oldest-first via plain `ListOptions{Page, PerPage: 1}` — no new schema. Keep is a bare GET link to page N+1 (no state change); Skip (reuses `POST /items/{id}/read`) and the triage-only `POST /triage/{id}/bookmark` (bookmarks AND clears `read_later`, unlike the reader's plain bookmark toggle) both remove the item from the unread+read-later set, so redirecting back to the same page N reveals whatever now occupies that position.
- `GET /habits` (linked from the dashboard) shows reading activity: `store.ReadingHabits` buckets `read_at`/backlog into 12 rolling 7-day windows (not calendar weeks, to sidestep ISO-week/timezone edge cases) — read counts are a real query, but backlog-per-week is *reconstructed* from `added_at`/`read_at` against present-day rows, not a tracked time series, so a since-deleted item leaves no trace. Rendered as minimal inline SVG bars (`weeklyBarChart`), no charting dependency.
- Saved views: a `saved_views` row is just a `label` on a `path` string — view/tag/sort/search already encode fully into the URL the app generates, so there's no separate filter representation to build or maintain. "Pin this view" on any list posts the current `r.URL.RequestURI()` (passed through `safeReturn` before it's ever stored, since it's later rendered as a plain `<a href>` on the dashboard) to `POST /saved-views`.
- Dates and the API: items carry `added_at`, `published_at`, `read_at` (stamped by SetReadState), `link_checked_at`; highlights carry `created_at`. The JSON API uses stable DTOs (`apiItem`/`apiHighlight`, ISO-8601) — never marshal raw store rows. Tokens have `read`/`write` scopes (`requireScope`); the Obsidian plugin (`obsidian/`, plain JS mirroring `extension/`, no build step; uses Obsidian's `requestUrl` to dodge CORS) uses read+write — Sync pulls `/api/items`+`/api/highlights` into one Markdown note per item (upserted by a `scrimshaw_id` frontmatter key; re-sync refreshes only frontmatter so body annotations survive), and commands push `/api/items/{id}/read`, `/api/items/{id}/highlights`, and `/api/save`; a website uses a read token against `/api/shared` (`?read_later=0` linklog, `?read_later=1` reading log). `GET /feed.xml?token=...` serves the same shared set as Atom (types in server.go, marshaled via encoding/xml, never hand-built) — the only route that accepts a token via query string instead of the `Authorization` header (`scopesForToken`, factored out of `tokenScopes`), since feed readers can't send custom headers; a missing/wrong/wrong-scope token 404s rather than 401s. The dead-link checker (`internal/links`) runs hourly like the feed poller, recording `link_status` (HTTP code, or negative for a transport error).
- Auth/CSRF: `withSession` middleware in internal/server wraps authenticated routes; CSRF checked on non-GET with constant-time compare. `/api/*` routes use bearer tokens instead. Changing the password (`POST /settings/password`, on the Settings page) requires the current password, then calls `store.DeleteAllSessions` and clears the requester's own cookie too, so every device must sign in again.
- Saving from outside: `GET /share` is the shared funnel for the bookmarklet, PWA share target, and iOS Shortcut — it reads a URL from `url`, `text`, or `title` and pre-fills the session-authed save form (no token). The bookmarklet (built in Settings from `Server.baseURL`, which prefers `SCRIMSHAW_BASE_URL`) opens `/share`, so no API token is ever embedded. The `extension/` MV3 background worker adds a right-click menu and `Alt+Shift+S`, both posting to `/api/save` with a stored write token.
- Migrations: SQL files in migrations/ embedded with go:embed; `store.Open` applies unapplied versions in a transaction on startup.
- Feed polling: internal/feeds Service; main starts `Run(ctx, time.Minute)` as a goroutine, which polls due feeds with conditional GET (ETag/Last-Modified) and disables a feed after 5 consecutive failures. A feed URL that permanently redirects (301/308, detected via `fetch.Client.GetTrackingRedirects`) is rewritten in place instead of left to erode the failure counter. Each feed carries a `rules` text column (one rule per line, `skip <pattern>` or `tag:<name> <pattern>`, plain text is a case-insensitive substring match and `/pattern/` is a regexp — see `internal/feeds/rules.go`), evaluated per entry in `pollOne` before insert (skip) and after a fresh insert (tag), edited from the feed's Settings panel on `/feeds`.
- Favicons: `feeds.DiscoverFavicon` runs once at subscribe time (`createFeed`, best-effort — a miss is normal, not logged as a warning), parsing `<link rel="icon">` from the site's homepage and falling back to `/favicon.ico`; either candidate is confirmed with a `Status` check before being stored in `feeds.favicon_url`, so a stored URL is never one known to 404. Rendering (`feedIcon` in server.go) proxies a real favicon through the existing `/images?url=` cache (same SSRF guard, SVG rejected) or, with none discovered, draws a generated monogram — first letter of the title, colored via the same `tagChipClass` hash palette as tags, so an icon-less feed still looks considered rather than blank. No re-discovery: a site that changes its favicon later keeps the one found at subscribe time.
- Backups: internal/backup Service, same `Run(ctx, interval)` ticker pattern as feeds/links; main starts it daily, taking a `VACUUM INTO` snapshot into `data/backups/` and keeping the 7 most recent. No cron job is required for this baseline protection.
- Weekly digest: internal/digest Service (same ticker pattern), main starts it every 7 days. `export.WeeklyDigest` writes one Markdown file to `data/exports/digests/` covering highlights from the last 7 days (a real query on `highlights.created_at`) and the current starred collection (not time-scoped — starred items have no timestamp, so the digest says "Starred", not "starred this week", to avoid implying precision the data doesn't have). Writes nothing (and logs why) when there's nothing to report, rather than leaving empty files. No email sending — deferred per the feature-backlog review, since it would need outbound SMTP-relay config for comparatively low payoff.
- Newsletter ingestion: internal/newsletter Service, same ticker pattern, opt-in — main only starts it when `SCRIMSHAW_IMAP_HOST` is set (fails fast at startup if `SCRIMSHAW_IMAP_USER`/`_PASSWORD` are then missing). Polls an external IMAP mailbox (`github.com/emersion/go-imap`, implicit TLS only) for `\Seen`-less messages via `UidSearch`, fetches each with `BODY.PEEK[]` (doesn't mark `\Seen` on its own, unlike plain `BODY[]`/`RFC822`), parses with `github.com/emersion/go-message/mail`, and inserts as a read-later item — every message this pass looked at is marked `\Seen` afterward regardless of per-message success, so one malformed email can't block the mailbox forever. Prefers the `text/html` part over `text/plain` (plain text is escaped and wrapped in `<p>` before the same `sanitize.HTML` every other content path uses). Dedups on `Message-ID` via a synthetic `https://newsletter.invalid/<id>` canonical URL (`.invalid` is the RFC 2606 reserved never-resolves TLD — `store.CanonicalURL` requires http(s), so a `mailto:` scheme wasn't an option); the reader's "open original" link goes nowhere for these items, since there is no original page. Deliberately an outbound poller, not an inbound SMTP receiver — running your own mail server would mean an open port, MX records, and permanent spam exposure disproportionate to a personal reader; point a mailbox you already control at it instead.
- Tests live beside sources as `_test.go` in each package. No Makefile, no linter config; the commands below are the whole toolchain.

## Commands

```sh
go build -o scrimshaw ./cmd/scrimshaw   # build
go run ./cmd/scrimshaw                   # run locally
go test ./...                            # test
docker compose up -d                     # run via docker
```

Migrations run automatically on startup. They are ordered, versioned, and embedded; never edit a migration that has shipped, add a new one.

## Hard rules, do not break

- Never ship a default password. First run creates the admin account.
- Never render fetched third-party HTML without sanitizing it first. Every saved page is a stored-XSS vector.
- Never fetch a user- or feed-supplied URL without the SSRF guard, a timeout, a redirect cap, and a max response size.
- Never store the database or snapshots on a network filesystem. Local disk only.
- Never back up the database by copying the file while the app runs. Use the online backup or VACUUM INTO.
- Never introduce a frontend build step, an SPA framework, or an npm dependency.
- Never add a dependency on an external system binary in v1. YouTube support is deferred precisely for this reason (needs yt-dlp). PDF is no longer deferred: URL-fetched PDFs are extracted with a pure-Go library (github.com/ledongthuc/pdf, internal/reader/save.go's extractPDF), not poppler, so the constraint this rule protects doesn't apply — a Go module dependency is fine, an external binary is not. There's still no upload UI, only URL-fetch through the existing add-a-link path.
- Auto-snapshot of feed items defaults to off. Do not change that default.

## Data model invariants

- items is the single source of truth for content. feed_id is nullable; a null feed_id means the item was saved manually.
- Deduplicate by canonical_url and a content hash before insert.
- The FTS5 table is kept in sync by triggers on items, covering title, extracted_text, and snapshot text. If you change those columns, update the triggers in the same migration. It is a contentless FTS5 table (`content=''`), so SQLite's `snippet()`/`highlight()` do not work; search excerpts are built in Go from the item's own content (`excerpt` in internal/server, which strips tags, escapes each word, and wraps matches in `<mark>`).
- Tags are flat and shared across all item sources. There are no folders or categories.

## Security invariants

- argon2id or bcrypt for passwords.
- Server-side sessions, cookies Secure, HttpOnly, SameSite.
- CSRF tokens on every state-changing HTMX post.
- API tokens stored hashed, named, revocable, and scoped.
- Login rate-limiting and lockout.

## Conventions

- Prefer the standard library and a small number of well-chosen dependencies over frameworks.
- Handlers stay thin; business logic lives in the internal packages, not in templates.
- Return meaningful errors to logs with structured logging; never leak internal detail to the client.
- Times are stored in UTC and displayed in the configured timezone (default Europe/Lisbon).
- Use straight quotes and plain ASCII in code, docs, and templates.

## Current phase

Phase 3, in progress. Phases 1 and 2 are implemented. The browser extension (extension/) and PWA (web/ manifest, service worker with offline reading cache, /share target) exist; finish and harden them. Importers, exports, and the token API from Phase 4 are already in place.

## When in doubt

Favor correctness, security, and data portability over adding features. If a choice trades any of those three for convenience, it is the wrong choice.
