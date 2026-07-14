-- Discovered once at subscribe time (see feeds.DiscoverFavicon); empty means
-- discovery failed or hasn't run, and the UI falls back to a generated
-- monogram rather than leaving a blank space.
ALTER TABLE feeds ADD COLUMN favicon_url TEXT NOT NULL DEFAULT '';
