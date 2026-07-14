-- Per-feed keyword/regex rules evaluated on ingest (skip or auto-tag noisy
-- feed items), one rule per line: "skip <pattern>" or "tag:<name> <pattern>".
-- A pattern wrapped in /slashes/ is a regexp; otherwise a case-insensitive
-- substring match. Empty by default (no rules).
ALTER TABLE feeds ADD COLUMN rules TEXT NOT NULL DEFAULT '';
