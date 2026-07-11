# Scrimshaw build specification

This is the authoritative build prompt for Scrimshaw. Hand it to a coding agent to build the application. README.md is the human-facing overview and CLAUDE.md holds durable guidance for ongoing work in the repository.

## Project

Build Scrimshaw, a single-user, self-hosted web application that unifies three functions into one datastore and one interface: an RSS/Atom/JSON feed reader, a bookmarks and links archive, and a read-it-later reader with offline snapshots. No single function is primary; the interface must serve all three equally. The name comes from the sailors' craft of etching lasting things from whalebone; a restrained whaling aesthetic (bone and ink tones, a serif reading face, fine-line detailing) is welcome but must never compromise legibility. The project lives in a public GitHub repository and deploys to a personal VPS.

## Technology

- Language: Go. Ship as a single static binary.
- Storage: SQLite with the FTS5 extension, in WAL mode. Snapshots stored as single self-contained HTML files on disk, not as database blobs; store their paths in SQLite.
- Feed parsing: mmcdole/gofeed.
- Content extraction: go-shiori/go-readability for readable text, go-shiori/obelisk for single-file HTML snapshots.
- HTML sanitization: microcosm-cc/bluemonday or equivalent.
- Frontend: Go html/template rendered server-side, with HTMX for interactions. No SPA framework, no bundler, no npm build step. Plain CSS for theming.
- Background work: goroutines driven by a ticker for feed polling, fetching, extraction, and snapshotting. A simple job queue with concurrency limits and per-host rate limiting.

## Security

Build this in from phase one, not later.

- Password login with argon2id or bcrypt hashing. First-run setup creates the admin account; no default password ships.
- Server-side sessions with Secure, HttpOnly, SameSite cookies. CSRF protection on all HTMX form posts.
- Login rate-limiting and lockout after repeated failures.
- API tokens stored hashed, individually named and revocable, scoped to what the extension, PWA, and automation each need.
- Sanitize all fetched third-party HTML with bluemonday before rendering; treat every saved page as a stored-XSS vector.

## Fetch safety

The fetcher pulls arbitrary user-supplied and feed-supplied URLs. Enforce request timeouts, a maximum response size, a redirect cap, and an SSRF guard that refuses private, loopback, and link-local addresses. Send an identifying User-Agent. Proxy and cache remote images in the reader to avoid mixed content and to keep the reader's IP from leaking to origin servers; fold this into the sanitization pass.

## Core data model

A single items table is the spine. An item is a URL with fetched content, regardless of origin.

Item fields: id, url, canonical_url, title, author, site_name, item_type, source (feed or manual), feed_id (nullable), published_at, added_at, extracted_text, snapshot_path (nullable), read_state (unread, read), archived (bool), starred (bool), reading_progress.

Related tables: feeds (url, title, tags, refresh_interval, last_fetched, etag, last_modified, fetch_full_content bool, auto_snapshot bool, consecutive_failures, last_error, disabled bool), tags (flat, no hierarchy) with an item_tags join, highlights (item_id, quote, note, position), and api_tokens.

An FTS5 virtual table indexes title, extracted_text, and snapshot text, kept in sync by triggers. Use a versioned, ordered, embedded migration mechanism from the first commit so the schema can evolve across phases without manual intervention.

## Organization

Flat tags only. No folders, no categories. Feeds carry tags; items carry tags; the same tag namespace applies to feed items, bookmarks, and saved articles alike. Filtering is by tag, state, type, and full-text query, combinable.

## Ingestion

- Feed subscriptions: add by URL with autodiscovery from a page URL; OPML import and export.
- Browser extension (Chrome and Firefox, Manifest V3): save current page, choose tags, authenticated by API token. This is the primary save path.
- Bookmarklet: a minimal fallback that posts the current URL to the save endpoint. Optional, since the extension subsumes it.
- Mobile: an installable PWA registered as a share target, so the OS share sheet can send URLs to Scrimshaw.
- Importers so you can actually migrate off existing tools: Pocket and Instapaper exports, linkding, readeck, and Netscape bookmarks HTML. OPML covers only feeds, so these cover saved articles and bookmarks.

## Reader

- Reader view: extracted, cleaned content with adjustable typography (at least a serif and a sans reading face, adjustable font size and column width) and light/dark themes.
- Toggle between reader view and the original page or its stored snapshot.
- Keyboard-driven navigation in the miniflux idiom: j/k next and previous, o open, m toggle read, s star, v open original, a archive, / focus search, g then a letter to jump between views. Document the full map in-app.
- Track and restore reading progress per item. Show a reading-time estimate.

## List and browsing UX

Unread counts per feed and per tag. Sort by newest, oldest, or unread-first. Pagination for large lists. Bulk actions: mark-all-read, bulk tag, bulk archive. Feed favicons. A highlights view that browses annotations across all items.

## Search

Full-text search across titles, extracted article text, and archived snapshot text via FTS5, with snippet highlighting. Combinable with tag, state, and type filters.

## States and annotation

Every item supports unread/read, starred, and archived (archived items leave the main list but remain searchable). Highlights on article text with optional attached notes, listed per item and browsable across all items.

## Content types

Web articles only in v1. Extract readable text and snapshot on save; snapshot feed items only when enabled (see feed handling).

## Feed handling

- Per-feed configurable refresh interval; use ETag and Last-Modified conditional requests; add jitter so feeds do not all poll at once.
- Deduplicate cross-posted items by canonical URL and content hash, so the same story from multiple feeds collapses to one item.
- When a feed delivers only an excerpt, fetch the full page and extract the complete article.
- Failure lifecycle: exponential backoff on errors, the last error surfaced per feed in the UI, auto-disable after a configurable number of consecutive failures.
- Auto-snapshot every feed item is supported but defaults to off globally, with a per-feed override, because it is costly in bandwidth and disk. Share the single page fetch between full-article extraction and snapshotting (fetch once, use twice), rate-limit per host, and show disk usage in settings.

## Authentication

Single user. Password login for the web interface; issued API tokens for the extension, PWA, and automation. No multi-user accounts.

## Integrations and delivery

- REST API covering items, feeds, tags, search, highlights, and save-a-URL, authenticated by API token; document it.
- Export saved items to Markdown files in a configured folder, one file per item with YAML frontmatter (title, url, tags, dates), the extracted content, and any highlights, suitable for Obsidian and plain-text workflows. Optional Zotero connector via the Zotero web API. Markdown export is the baseline; Zotero is optional.
- Installable PWA with a service worker. Offline reading is scoped to the reader view and snapshots of unread and starred items, not the entire archive, to keep the cache bounded.

## Deployment and CI

- Primary route: a Dockerfile producing a small image, and a docker-compose example. GitHub Actions builds and publishes the image to GitHub Container Registry (ghcr.io/tiagojct/scrimshaw) on tagged releases; deploy on the VPS by pulling the image.
- Persistence: the SQLite database and the snapshots directory must live on a persistent local volume (bind mount or local named volume, never a network filesystem). Document this prominently.
- Escape hatch: also ship the bare static binary and a systemd unit example, so the app can run without Docker.
- Configuration via a single file and environment variables. Timezone configurable, default Europe/Lisbon, so published dates read correctly.
- Provide a health endpoint, structured logging, and graceful shutdown that lets in-flight jobs finish.

## Backups and portability

- Back up SQLite with the online backup API or VACUUM INTO against the live database, never by copying the file. Keep the snapshots directory in sync in the same routine. Document a restore procedure.
- All user data must be portable: OPML for feeds, JSON for a full export, Markdown for articles, readable HTML snapshots on disk.

## Testing

Cover the fragile parts: content extraction, deduplication, FTS synchronization triggers, the SSRF guard, and import round-trips.

## Build order

Deliver in phases, each independently runnable.

Phase 1: security foundation (auth, sessions, CSRF, sanitization, fetch safety, SSRF guard), versioned migrations, unified items schema, feed subscription and polling with the failure lifecycle, reader view with keyboard navigation, tags, read state.

Phase 2: manual save endpoint, full-article fetch, obelisk snapshots to disk, image proxy and cache, FTS5 search including snapshots, star, archive, highlights and notes, list UX (counts, sort, pagination, bulk actions).

Phase 3: browser extension, PWA with share target and scoped offline reading, bookmarklet fallback.

Phase 4: REST API documentation, Markdown export, optional Zotero connector, importers (Pocket, Instapaper, linkding, readeck, Netscape), OPML and JSON import/export.

## Deferred, not in v1

YouTube links (embed, metadata, transcript) and PDFs (upload, store, text extraction). Both need external binaries (yt-dlp, poppler) that break the single-binary model, and both are high effort for their value. Design the item_type field and the save pipeline so they can be added later without schema change, but build neither now.

## Acceptance

A phase is done when its features work end to end, data survives a restart, the binary and image build clean in CI, and the export and import formats round-trip. Prioritize correctness, security, and data portability over feature breadth.
