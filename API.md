# API

Create a token under **API tokens** in the web interface and choose its scopes:

- **read** — the `GET` endpoints (retrieve items, highlights, the shared linklog).
- **write** — the `POST` endpoints (save pages, mark read, add highlights).

Give an Obsidian plugin a read+write token; give a website a read-only token for
the shared linklog. Tokens are shown once, stored only as hashes, and revocable.
Send the token as `Authorization: Bearer <token>`.

## Save a URL (write)

```sh
curl -X POST https://scrimshaw.example.com/api/save \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/article","tags":["reading"],"read_later":true}'
```

`read_later` defaults to `true` (fetch and extract the article for reading). Pass
`false` to store a link-only bookmark. The response is `{"id":123}`.

## Retrieve items (read)

`GET /api/items` returns every item as JSON. Each item carries stable, ISO-8601
dates and a `kind` of `article` or `link`:

```json
{
  "id": 2, "url": "https://example.com/article", "title": "...",
  "source": "manual", "kind": "article",
  "read_later": true, "bookmarked": false, "read": true, "starred": false, "archived": false,
  "shared": false, "link_status": 200, "tags": ["reading"],
  "content": "<p>...</p>",
  "added_at": "2026-07-11T09:00:00Z",
  "published_at": "2026-07-10T00:00:00Z",
  "read_at": "2026-07-11T18:22:00Z"
}
```

Also read-scoped: `GET /api/feeds`, `GET /api/highlights` (each highlight has a
`created_at`), and `GET /api/search?q=term`.

## Publish to a website (read)

`GET /api/shared` returns only shared items, for building a public page:

- `GET /api/shared?bookmarked=1` — shared bookmarks (a linklog).
- `GET /api/shared?read_later=1` — shared read-later articles (a reading log).

## Mark read and add highlights (write)

For an Obsidian plugin to push reading state and annotations back:

```sh
curl -X POST https://scrimshaw.example.com/api/items/2/read \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"read":true}'          # stamps read_at; returns the updated item

curl -X POST https://scrimshaw.example.com/api/items/2/highlights \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"quote":"a passage","note":"why it matters"}'   # 201 Created
```
