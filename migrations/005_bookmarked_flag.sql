-- Make "bookmarked" an orthogonal flag so any item (including a feed article)
-- can be saved to the linklog, alongside the existing read_later flag.
ALTER TABLE items ADD COLUMN bookmarked INTEGER NOT NULL DEFAULT 0;

-- Existing link-only manual items were bookmarks.
UPDATE items SET bookmarked = 1 WHERE source = 'manual' AND read_later = 0;

CREATE INDEX items_bookmarked_idx ON items(bookmarked, archived);
