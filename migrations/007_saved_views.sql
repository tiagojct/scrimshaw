-- Pinned filter views: a saved view is just a label for a URL the app
-- already generates (view/tag/sort/search encode fully into the querystring),
-- not a separate filter-picker UI or query representation.
CREATE TABLE saved_views (
    id INTEGER PRIMARY KEY,
    label TEXT NOT NULL,
    path TEXT NOT NULL,
    created_at TEXT NOT NULL
);
