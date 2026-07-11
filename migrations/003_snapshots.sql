ALTER TABLE items ADD COLUMN snapshot_text TEXT NOT NULL DEFAULT '';

DROP TRIGGER items_fts_insert;
DROP TRIGGER items_fts_update;
DROP TRIGGER items_fts_delete;

CREATE TRIGGER items_fts_insert AFTER INSERT ON items BEGIN
    INSERT INTO items_fts(rowid, title, extracted_text, snapshot_text)
    VALUES (new.id, new.title, new.extracted_text, new.snapshot_text);
END;
CREATE TRIGGER items_fts_update AFTER UPDATE OF title, extracted_text, snapshot_text ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, extracted_text, snapshot_text)
    VALUES ('delete', old.id, old.title, old.extracted_text, old.snapshot_text);
    INSERT INTO items_fts(rowid, title, extracted_text, snapshot_text)
    VALUES (new.id, new.title, new.extracted_text, new.snapshot_text);
END;
CREATE TRIGGER items_fts_delete AFTER DELETE ON items BEGIN
    INSERT INTO items_fts(items_fts, rowid, title, extracted_text, snapshot_text)
    VALUES ('delete', old.id, old.title, old.extracted_text, old.snapshot_text);
END;
