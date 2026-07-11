CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE login_attempts (
    address TEXT PRIMARY KEY,
    failures INTEGER NOT NULL DEFAULT 0,
    locked_until TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE feeds (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    refresh_interval_seconds INTEGER NOT NULL DEFAULT 3600 CHECK(refresh_interval_seconds >= 60),
    last_fetched TEXT,
    etag TEXT,
    last_modified TEXT,
    fetch_full_content INTEGER NOT NULL DEFAULT 0,
    auto_snapshot INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    disabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE tags (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE
);

CREATE TABLE feed_tags (
    feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY(feed_id, tag_id)
);

CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL,
    canonical_url TEXT NOT NULL UNIQUE,
    content_hash TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    site_name TEXT NOT NULL DEFAULT '',
    item_type TEXT NOT NULL DEFAULT 'article',
    source TEXT NOT NULL CHECK(source IN ('feed', 'manual')),
    feed_id INTEGER REFERENCES feeds(id) ON DELETE SET NULL,
    published_at TEXT,
    added_at TEXT NOT NULL,
    extracted_text TEXT NOT NULL DEFAULT '',
    snapshot_path TEXT,
    read_state TEXT NOT NULL DEFAULT 'unread' CHECK(read_state IN ('unread', 'read')),
    archived INTEGER NOT NULL DEFAULT 0,
    starred INTEGER NOT NULL DEFAULT 0,
    reading_progress REAL NOT NULL DEFAULT 0 CHECK(reading_progress >= 0 AND reading_progress <= 1)
);
CREATE INDEX items_feed_id_idx ON items(feed_id);
CREATE INDEX items_read_state_idx ON items(read_state, archived);

CREATE TABLE item_tags (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY(item_id, tag_id)
);

CREATE TABLE highlights (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    quote TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE api_tokens (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    scopes TEXT NOT NULL,
    created_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE VIRTUAL TABLE items_fts USING fts5(title, extracted_text, snapshot_text, content='');
CREATE TRIGGER items_fts_insert AFTER INSERT ON items BEGIN
    INSERT INTO items_fts(rowid, title, extracted_text, snapshot_text)
    VALUES (new.id, new.title, new.extracted_text, '');
END;
CREATE TRIGGER items_fts_update AFTER UPDATE OF title, extracted_text ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, extracted_text, snapshot_text)
    VALUES ('delete', old.id, old.title, old.extracted_text, '');
    INSERT INTO items_fts(rowid, title, extracted_text, snapshot_text)
    VALUES (new.id, new.title, new.extracted_text, '');
END;
CREATE TRIGGER items_fts_delete AFTER DELETE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, extracted_text, snapshot_text)
    VALUES ('delete', old.id, old.title, old.extracted_text, '');
END;
