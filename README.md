# Scrimshaw

A single-user, self-hosted reader that keeps feeds, bookmarks, and read-it-later articles in one place and one datastore. It reads RSS, Atom, and JSON feeds, saves and archives links with an offline snapshot, and gives all three the same tags, the same search, and the same reader.

The name comes from the sailors' craft of etching lasting things from whalebone: a small, private archive you build over time.

## Status

In development, built in phases against the specification in SPEC.md. Not yet feature complete.

## Why it exists

Miniflux, linkding, and readeck each do one of these jobs well, but they are three apps and three datastores. Scrimshaw treats a feed entry, a saved bookmark, and a read-it-later article as the same underlying thing, a URL with fetched content, a read state, tags, and an optional archived snapshot. One store, one interface.

## Features

Reading
- RSS, Atom, and JSON feeds with autodiscovery from a page URL
- Clean reader view with adjustable typography, light and dark themes
- Toggle between reader view, the original page, and the stored snapshot
- Keyboard-driven navigation in the miniflux idiom
- Reading progress and reading-time estimate

Saving and archiving
- Save any page from a browser extension, a bookmarklet, or the mobile share sheet
- Offline snapshot stored as a single self-contained HTML file on disk
- Full-text search across titles, article text, and snapshots
- Highlights and notes on articles

Organization
- Flat tags across feeds, bookmarks, and saved articles alike
- Unread, starred, and archived states
- Filter by tag, state, type, and search, combined

Feeds that behave
- Per-feed refresh intervals with conditional requests
- Cross-post deduplication
- Full-article fetch when a feed only sends an excerpt
- Per-feed failure handling with backoff and auto-disable

Portability
- OPML for feeds, JSON for a full export, Markdown for articles
- Snapshots are plain HTML files you can read without the app

## Technology

Go, compiled to a single static binary. SQLite with FTS5 for storage and search. Server-rendered HTML with HTMX; no SPA, no build step. gofeed for parsing, go-readability and obelisk for extraction and snapshots.

## Quick start with Docker

```yaml
# docker-compose.yml
services:
  scrimshaw:
    image: ghcr.io/tiagojct/scrimshaw:latest
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    environment:
      SCRIMSHAW_BASE_URL: "https://scrimshaw.example.com"
      SCRIMSHAW_TIMEZONE: "Europe/Lisbon"
    restart: unless-stopped
```

```sh
docker compose up -d
```

Open the app, complete the first-run setup to create your account, and add a feed or save your first link. The database and snapshots live under the mounted data volume.

## Configuration

Configuration is by environment variable or a single config file. Common settings:

| Variable | Default | Purpose |
| --- | --- | --- |
| SCRIMSHAW_ADDR | :8080 | Listen address and port |
| SCRIMSHAW_BASE_URL | (none) | Public URL, used for the PWA and links |
| SCRIMSHAW_DATA_DIR | /data | Holds the SQLite database and snapshots |
| SCRIMSHAW_TIMEZONE | Europe/Lisbon | Timezone for displayed dates |
| SCRIMSHAW_SESSION_SECRET | (generated) | Session cookie signing key |
| SCRIMSHAW_AUTO_SNAPSHOT | off | Snapshot every feed item, not just saved ones |
| SCRIMSHAW_DEFAULT_REFRESH | 60m | Default per-feed refresh interval |
| SCRIMSHAW_FETCH_TIMEOUT | 30s | Per-request fetch timeout |
| SCRIMSHAW_LOG_LEVEL | info | Logging verbosity |

## Data and backups

The SQLite database and the snapshots directory must sit on a persistent local disk, a bind mount or a local named volume, never a network filesystem, or you risk corruption and locking failures. Back up the database with SQLite's online backup rather than copying the file while it runs, and keep the snapshots directory in the same backup routine.

To restore, stop Scrimshaw, restore the database and snapshots directory together to the data directory, then start it again. Do not replace a live SQLite file by copying over it.

## Running without Docker

Scrimshaw is a single binary, so you can also run it directly under systemd. A unit file example ships in the repository. Point SCRIMSHAW_DATA_DIR at a local directory and run.

## Browser and mobile

There are four ways to save a page, all listed under Settings:

- **Bookmarklet** — drag "Save to Scrimshaw" to your bookmarks bar. It opens the pre-filled save form using your logged-in session, so no token is embedded. Works in desktop and iOS Safari (bookmark a page, then edit its address to the snippet).
- **Browser extension** — `extension/` is an unpacked Manifest V3 extension for Chromium and Firefox. Create a write token, load the folder unpacked, and set the server URL and token in its popup. It adds a toolbar button, a right-click "Save to Scrimshaw" menu (page or link, read-later or bookmark), and an `Alt+Shift+S` shortcut.
- **iOS Shortcut** — a Shortcut that accepts URLs and POSTs to `/api/save` with a write token puts Scrimshaw in the iOS Share Sheet. Settings shows the exact request.
- **PWA share target** — the web app ships a manifest and service worker; installed from a supported browser, a share opens the authenticated save form.

## Exports and API

The web interface provides JSON and OPML downloads. Markdown files are exported to `data/exports/` by posting to `/export/markdown`. The token-authenticated save API is documented in [API.md](API.md).

Imports currently support OPML feed subscriptions and Netscape bookmarks HTML. Bookmark folders are imported as flat tags.

## Building from source

```sh
go build -o scrimshaw ./cmd/scrimshaw
./scrimshaw
```

## For contributors and agents

SPEC.md is the full build specification. CLAUDE.md holds the durable conventions and hard rules for working in this codebase.

## License

MIT.
