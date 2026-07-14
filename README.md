# Scrimshaw

A single-user, self-hosted reader that unifies an **RSS/Atom/JSON feed reader**, a **bookmarks archive**, and a **read-it-later reader** into one SQLite datastore and one server-rendered interface. All three are equal citizens: a feed entry, a saved bookmark, and a read-later article are the same underlying thing — a URL with fetched content, tags, states, highlights, and an optional offline snapshot.

The name comes from the sailors' craft of etching lasting things from whalebone: a small, private archive you build over time.

## Why it exists

Miniflux, linkding, and readeck each do one of these jobs well, but they are three apps and three datastores. Scrimshaw keeps all of it in one place, with the same tags, the same full-text search, and the same reader across feeds, bookmarks, and saved articles. One store, one interface, one backup.

## How it works

Everything lives in a single `items` table. What distinguishes an item is a small set of flags, not a separate table:

- **Source** — `feed` (from a subscription) or `manual` (you added it).
- **Read later** — the reading queue. Ticking it fetches and extracts the full article for a clean reader view. A PDF URL is extracted the same way (pure-Go text extraction, no upload UI, no external binary) — a scanned/image PDF with no text layer falls back to a plain link, same as a bot-blocked page would.
- **Bookmarked** — the linklog. A link-only save, stored for reference.

These are independent, so any item can be several things at once — you can bookmark a feed article *and* send it to read-later. On top of that each item carries **read/unread** (with a `read_at` timestamp), **starred**, **archived**, and **shared** states, flat **tags**, **highlights and notes**, and an optional **snapshot** (a self-contained HTML copy on disk).

The interface is a set of views over that one table:

| View | Shows |
| --- | --- |
| **Dashboard** | The home screen: counts (unread feeds, to-read, bookmarks, starred, highlights, broken links) and recent items per section |
| **Today** | Everything published or added today, across every source (a NetNewsWire-style smart view) |
| **Feeds** | Your subscription firehose |
| **Read Later** | The reading queue |
| **Bookmarks** | Your linklog (kept even after reading) |
| **Starred** | Favourites, across everything |
| **Read** | Things you're done with (still searchable, still counted in Bookmarks/Starred if they were one) |
| **All** | Everything |
| **Highlights** | Every passage and note, across all items |

**Reading files things away.** Marking an item read also archives it and stamps the time, so it leaves the active list and moves to Archived. Starred and Bookmarks are permanent collections — an item stays in them even after it's read. Opening a plain feed item marks it read automatically (like any feed reader) and it drops out of Feeds — unless you've already sent it to Read Later or Bookmarks, in which case opening it to peek doesn't dismiss it from that queue. Sending a feed item to Read Later always gives it a fresh unread state there, regardless of whatever happened to it in Feeds, and fetches the full article; a separate **Fetch full text** button gets you the full article without committing it to Read Later at all.

**Triage.** `/triage` is a fast, one-item-at-a-time way to burn down the Read Later queue instead of scrolling the list: Keep moves on without changing anything, Skip marks it read, Bookmark keeps the link but takes it out of the queue. Keyboard: `k` keep, `x` skip, `b` bookmark.

**Reading habits.** `/habits` (linked from the dashboard) charts items read and Read Later backlog size over the last 12 weeks, plus your most-used tags — a motivational glance, not a workflow.

**Saved views.** Any list has a "Pin this view" field — it saves a label for the exact URL you're looking at (view, tag, sort, and search all already live in the querystring), listed on the dashboard for one-click access later.

## Features

Reading
- RSS, Atom, and JSON feeds
- A clean reader view (serif reading face, light and dark themes that follow your OS)
- Toggle between the reader view, the original page, and the offline snapshot
- Highlights: select any passage in an article to save it; add free-text notes
- Keyboard-driven navigation in the miniflux idiom

Saving
- Add a link and choose **Read later** (fetch the article) or **Bookmark** (store the link); the title is fetched automatically
- Save from a bookmarklet, a browser extension, the iOS Share Sheet, the PWA, or the API (see below)
- Newsletter ingestion (optional): point Scrimshaw at an IMAP mailbox you already control and it polls for unread mail, converting each into a read-later item — a "kill-the-newsletter"-style bridge for the reading that arrives by email, not RSS. Off unless configured (see [Configuration](#configuration))
- Offline snapshots stored as single, self-contained HTML files on disk
- Full-text search across titles, article text, and snapshots, with the match highlighted in an excerpt

Organization
- Flat tags shared across feeds, bookmarks, and saved articles; edit an item's tags in the reader
- Filter by view, tag, and state; sort by newest, oldest, or unread-first
- Bulk actions: mark selected read, archive selected
- Delete an item permanently (removes its highlights, tags, and snapshot)

Feeds that behave
- Per-feed refresh interval (15 minutes to daily), configurable in the UI
- Conditional requests (ETag / Last-Modified) and cross-post deduplication
- Optional full-article fetch and auto-snapshot per feed
- Content rules: `skip <keyword or /regex/>` to drop noisy entries, `tag:<name> <keyword or /regex/>` to auto-tag matches, one per line in the feed's settings
- Failure handling with auto-disable after repeated errors; manual **Refresh** (single or all) that also revives a disabled feed. A feed URL that permanently moves (301/308) is followed and updated automatically
- Favicons, fetched once at subscribe time; a feed without one gets a generated monogram instead of a blank space
- A dead-link checker that flags bookmarks whose URL no longer resolves

Publishing and portability
- Share flag + a read-only API so you can drive a public linklog and a "read articles" page on your own website
- Export everything as JSON, feeds as OPML, and articles as Markdown (one file per item with YAML frontmatter — title, URL, date — and the extracted content)
- Import from Pocket, Instapaper, linkding, Readeck, and Netscape/browser bookmarks; OPML feed imports show a preview where you can tag each feed before importing
- Snapshots are plain HTML files you can read without the app

## Saving from anywhere

Four ways to save a page, all listed under **Settings → Save from anywhere**:

- **Bookmarklet** — drag "Save to Scrimshaw" to your bookmarks bar. It opens the pre-filled save form using your logged-in session, so **no token is embedded** in the bookmark. Works on desktop and in iOS Safari (bookmark any page, then edit the bookmark's address to the snippet). Set `SCRIMSHAW_BASE_URL` so the snippet points at your real public origin.
- **Browser extension** — `extension/` is an unpacked Manifest V3 extension for Chromium and Firefox. Create a **write** API token, load the folder as an unpacked extension, and set the server URL and token in its popup. It adds a toolbar button, a right-click **"Save to Scrimshaw"** menu (page or link, read-later or bookmark), and an `Alt+Shift+S` shortcut.
- **Obsidian plugin** — `obsidian/` is an unpacked plugin (plain JS, no build step). Copy it to `<vault>/.obsidian/plugins/scrimshaw/`, enable it, and set your server URL and a **read+write** token. It syncs your saved items into the vault as Markdown notes and pushes reading state and highlights back (Sync, Mark read, Send highlight, Save URL commands). See `obsidian/README.md`.
- **iOS Shortcut** — a Shortcut that accepts URLs and POSTs to `/api/save` with a write token puts Scrimshaw in the native Share Sheet. Settings shows the exact request to build.
- **PWA share target** — the app ships a web manifest and service worker; installed from a supported browser, a share opens the authenticated save form. (iOS Safari doesn't support share targets — use the Shortcut or bookmarklet there.)

## Keyboard shortcuts

In list views: `j` / `k` next / previous, `o` open the focused item, `/` focus search. `g` then `a` goes home, `g` then `f` opens the feed form.

In the reader: `m` mark read, `s` star, `v` open the original. Select text to highlight it.

Press `?` anywhere to open an in-app shortcut reference.

## Install

### Docker (recommended)

```yaml
# docker-compose.yml (ships in the repo; builds the image locally)
services:
  scrimshaw:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    environment:
      SCRIMSHAW_BASE_URL: ${SCRIMSHAW_BASE_URL:-http://localhost:8080}
    restart: unless-stopped
```

```sh
docker compose up -d
```

Open the app, complete the first-run setup to create your admin account (there is no default password), and add a feed or save your first link. The database and snapshots live under the mounted `./data` volume. Put Scrimshaw behind a TLS-terminating reverse proxy and set `SCRIMSHAW_BASE_URL` to its public HTTPS URL.

The compose file above **builds the image locally**. Once the repo is pushed to GitHub, the `Docker publish` workflow builds a multi-arch image and pushes it to `ghcr.io/<owner>/scrimshaw` on every push to `main` and on version tags; you can then replace `build: .` with `image: ghcr.io/<owner>/scrimshaw:latest` and just `docker compose pull`.

### Binary

```sh
go build -o scrimshaw ./cmd/scrimshaw
SCRIMSHAW_DATA_DIR=./data ./scrimshaw
```

### systemd

`scrimshaw.service` ships as a hardened example unit (`NoNewPrivileges`, `ProtectSystem=strict`, a dedicated user, and a `ReadWritePaths` data directory). Point `SCRIMSHAW_DATA_DIR` at a local directory and enable it.

## Configuration

Configuration is by environment variable:

| Variable | Default | Purpose |
| --- | --- | --- |
| `SCRIMSHAW_ADDR` | `:8080` | Listen address and port |
| `SCRIMSHAW_DATA_DIR` | `./data` (`/data` in Docker) | Holds the SQLite database, snapshots, image cache, and exports |
| `SCRIMSHAW_BASE_URL` | (none) | Public origin. Used to build the bookmarklet and iOS snippet, and to decide the session cookie's `Secure` flag (off only for an `http://` value) |
| `SCRIMSHAW_SESSION_SECRET` | (generated and persisted) | Base64url, at least 32 bytes. Set it to keep sessions valid across restarts on ephemeral filesystems |
| `SCRIMSHAW_FETCH_TIMEOUT` | `30s` | Timeout for every outbound fetch (feeds, saves, images, link checks) |
| `SCRIMSHAW_IMAP_HOST` | (none) | `host:port` of an IMAP mailbox to poll for newsletters, e.g. `imap.example.com:993`. Newsletter ingestion is off unless this is set |
| `SCRIMSHAW_IMAP_USER` / `SCRIMSHAW_IMAP_PASSWORD` | (none) | Required once `SCRIMSHAW_IMAP_HOST` is set (the app exits at startup if either is missing). Most providers need an app-specific password, not your normal account password |
| `SCRIMSHAW_IMAP_FOLDER` | `INBOX` | Mailbox folder to poll |
| `SCRIMSHAW_IMAP_INTERVAL` | `15m` | How often to poll |

Per-feed settings (refresh interval, fetch-full-content, auto-snapshot) are configured in the UI under **Feeds**, not by environment variable. Auto-snapshot is off by default.

Point `SCRIMSHAW_IMAP_HOST` at a mailbox, alias, or filtered label you already control — not your main inbox. Every message the poller looks at gets marked `\Seen`, whether or not it imported cleanly, so nothing is retried forever; connects over implicit TLS (IMAPS) only.

## Data and backups

The SQLite database and the snapshots directory must sit on **persistent local disk** — a bind mount or a local named volume, never a network filesystem, or you risk corruption and locking failures. Scrimshaw is the only process that should write to the database; don't run the `sqlite3` CLI or another tool against it while the app is running.

Scrimshaw takes its own daily backup automatically: a `VACUUM INTO` snapshot into `data/backups/`, keeping the 7 most recent, with no cron job needed. That covers the database against corruption, but not the snapshots directory and not an off-box copy — for those, back up the whole data directory with SQLite's **online backup**/`VACUUM INTO` (never by copying the live file) on your own schedule. To restore: stop Scrimshaw, restore the database and snapshots together into the data directory, then start it again. Migrations run automatically on startup and are versioned and append-only.

Every 7 days, a Markdown digest of the past week's highlights and your starred collection is written to `data/exports/digests/` — nothing is emailed, it's a file you can read whenever.

## Security

- First run creates the admin account; there is no default password. Passwords are bcrypt-hashed with a 12-character minimum, and can be changed later from Settings (this signs out every session, including the current one). Login is rate-limited with per-IP lockout.
- Server-side sessions; cookies are `HttpOnly`, `SameSite=Lax`, and `Secure` (unless the base URL is `http://` for local dev). CSRF tokens on every state-changing form.
- Every fetched third-party page is sanitized (bluemonday) before it is rendered or snapshotted. Remote images are proxied and cached same-origin.
- Every user- or feed-supplied URL goes through an SSRF guard (blocks loopback, private, link-local, and CGNAT ranges, dials the validated IP, caps redirects and response size) with a timeout.
- API tokens are stored hashed, named, revocable, and scoped (`read` / `write`).

## API

A token-authenticated JSON API drives the extension, an Obsidian workflow, and a personal website. Create tokens under **API tokens** and choose scopes:

- `read` — `GET /api/items`, `/api/feeds`, `/api/highlights`, `/api/search`, and `/api/shared` (the public linklog / reading log split by `?bookmarked=1` or `?read_later=1`).
- `write` — `POST /api/save`, `/api/items/{id}/read` (marks read, stamps `read_at`), and `/api/items/{id}/highlights`.

Your shared linklog/reading log is also available as an Atom feed at `GET /feed.xml?token=<a read-scoped token>`, so it can be subscribed to in any feed reader instead of polled as JSON. Feed readers can't send an `Authorization` header, so the token travels in the URL — treat that URL as a secret, the same as the token itself. The full URL is shown once when you create a read-scoped token.

Items carry ISO-8601 dates (`added_at`, `published_at`, `read_at`) and a `kind` of `article` or `link`. Full details and examples are in [API.md](API.md).

## Technology

Go, compiled to a single static binary (`CGO_ENABLED=0`, scratch image). SQLite with FTS5 and WAL mode via the pure-Go `modernc.org/sqlite` driver. Server-rendered HTML with a little HTMX and vanilla JavaScript — no SPA, no bundler, no npm, no frontend build step. `mmcdole/gofeed` parses feeds, `go-shiori/go-readability` extracts articles, `microcosm-cc/bluemonday` sanitizes. The interface follows the [Glauca](https://github.com/tiagojct/glauca) design system (light-first, one restrained accent), served as embedded CSS/JS.

## For contributors and agents

- `SPEC.md` — the full feature specification.
- `CLAUDE.md` — durable conventions, invariants, and the hard rules for working in this codebase.

```sh
go build -o scrimshaw ./cmd/scrimshaw   # build
go run ./cmd/scrimshaw                   # run locally
go test ./...                            # test
```

## License

Code is MIT. See `LICENSE-MIT`.
