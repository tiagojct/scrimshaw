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
- Plain CSS for theming, following the Glauca design system (~/github/glauca; Pruina light default, Profundum dark via prefers-color-scheme). CSS and JS live in internal/server/static/ (app.css, app.js), embedded and served same-origin so the CSP needs no inline exceptions.

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
```

## Architecture pointers

- Routing: std-lib http.ServeMux with Go 1.22 method+pattern syntax in `Routes()` (internal/server/server.go). No router dependency.
- HTML: one root `pageTemplate` shell in internal/server/server.go (a sticky `.topbar` with the brand + quick "Add a link", then a centered `.container` main) links /app.css and /app.js; page bodies are built as escaped strings and injected as template.HTML. Static CSS/JS are embedded from internal/server/static/ and served by the `asset` handler. There is no html/template templates directory. Item rows are two-line (title + a meta line of author, `relativeTime` age, and tonal badges); badges carry a type class (`feed`/`later`/`bookmark`/`star`/`shared`/`broken`) styled in app.css.
- Highlights: the reader marks saved highlights in place (app.js walks text nodes and wraps them in `<mark class="hl">`) and lets you select article text to create one (a popover posts to /items/{id}/highlights). Saved quotes are passed to the page as a `<script type="application/json" id="hl-data">` island; json.Marshal escapes `<` so it is XSS-safe. A manual add form remains as the no-JS fallback.
- Three workflows over one items table (Miniflux + Linkding + Readeck). `read_later` and `bookmarked` are orthogonal boolean flags that apply to ANY item, including feed articles: Feeds = `source=feed`, Read Later = `read_later=1`, Bookmarks = `bookmarked=1`. Adding a link (`/save`) with the "Read later" box ticked runs the Saver (fetch + extract + snapshot, sets read_later); unticked runs `Saver.SaveLink` (link + title only, sets bookmarked). Every item's reader has Read later and Bookmark toggle buttons (`/items/{id}/readlater`, `/items/{id}/bookmark`), so a feed article can be sent to either; promoting to read-later extracts in place if it has no content.
- Reading files an item away: `SetReadState('read')` also stamps `read_at` and archives; `'unread'` clears both and un-archives. So marking read removes an item from its active reading list and it shows up in Archived (still searchable). The reader's primary button is "Mark read" (returns to the item's list) or, when archived, "Move to inbox". Bookmarks and Starred are permanent collections, not queues: their views set `IncludeArchived` (and Starred filters `starred=1` directly), so a bookmarked or starred item stays listed even after it is read/archived.
- Image handling: `sanitize.HTML` routes every `<img>` through the `/images` proxy. It recovers a fetchable source before proxying — normalizing protocol-relative (`//host/...`) URLs to https, promoting lazy-load attributes (`data-src`/`data-original`/`data-lazy-src`) and `srcset` over placeholder/`data:` srcs — so CDN and lazy-loaded images are not dropped.
- Views are `?view=` tabs (`viewOrder`/`viewsByKey`): feeds, later, bookmarks, starred, archived, all; store.ListPage filters by Source/State/ReadLater/Bookmarked/Shared. Bare `/` renders the dashboard (`dashboard`), not a list. Star/share are reversible toggles posting an explicit `!= "0"` value. The main toolbar holds only frequent actions (Add a link, Add a feed, Highlights, Settings); import, exports, and API tokens live on `/settings`.
- Dates and the API: items carry `added_at`, `published_at`, `read_at` (stamped by SetReadState), `link_checked_at`; highlights carry `created_at`. The JSON API uses stable DTOs (`apiItem`/`apiHighlight`, ISO-8601) — never marshal raw store rows. Tokens have `read`/`write` scopes (`requireScope`); the Obsidian plugin uses read+write (retrieve items, mark read, add highlights), a website uses a read token against `/api/shared` (`?read_later=0` linklog, `?read_later=1` reading log). The dead-link checker (`internal/links`) runs hourly like the feed poller, recording `link_status` (HTTP code, or negative for a transport error).
- Auth/CSRF: `withSession` middleware in internal/server wraps authenticated routes; CSRF checked on non-GET with constant-time compare. `/api/*` routes use bearer tokens instead.
- Migrations: SQL files in migrations/ embedded with go:embed; `store.Open` applies unapplied versions in a transaction on startup.
- Feed polling: internal/feeds Service; main starts `Run(ctx, time.Minute)` as a goroutine, which polls due feeds with conditional GET (ETag/Last-Modified) and disables a feed after 5 consecutive failures.
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
- Never add a dependency on an external system binary in v1. YouTube and PDF support are deferred precisely for this reason.
- Auto-snapshot of feed items defaults to off. Do not change that default.

## Data model invariants

- items is the single source of truth for content. feed_id is nullable; a null feed_id means the item was saved manually.
- Deduplicate by canonical_url and a content hash before insert.
- The FTS5 table is kept in sync by triggers on items, covering title, extracted_text, and snapshot text. If you change those columns, update the triggers in the same migration.
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
