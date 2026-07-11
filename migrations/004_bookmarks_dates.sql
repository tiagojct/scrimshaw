-- Distinguish read-later articles from link-only bookmarks, add the dates the
-- Obsidian plugin needs, a share flag for publishing, and dead-link tracking.
ALTER TABLE items ADD COLUMN read_at TEXT;
ALTER TABLE items ADD COLUMN read_later INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN shared INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN link_checked_at TEXT;
ALTER TABLE items ADD COLUMN link_status INTEGER NOT NULL DEFAULT 0;

-- Existing manually saved articles were read-later saves; feed items are not.
UPDATE items SET read_later = 1 WHERE source = 'manual';

CREATE INDEX items_read_later_idx ON items(read_later, archived);
CREATE INDEX items_shared_idx ON items(shared);
