# API

Create a save token from **API tokens** in the web interface. Tokens are shown once,
stored only as hashes, and currently have the `save` scope.

## Save a URL

```sh
curl -X POST https://scrimshaw.example.com/api/save \
  -H "Authorization: Bearer $SCRIMSHAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/article","tags":["reading"]}'
```

The response is `{"id":123}`. The endpoint accepts cross-origin requests so the
included browser extension can use it; a valid bearer token is always required.

## Read endpoints

The same bearer token authenticates `GET /api/items`, `GET /api/feeds`,
`GET /api/highlights`, and `GET /api/search?q=term`. Each returns JSON.
