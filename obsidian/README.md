# Scrimshaw for Obsidian

An unofficial Obsidian plugin for [Scrimshaw](https://github.com/tiagojct/scrimshaw).
It pulls your saved items into the vault as Markdown notes, and pushes reading
state and highlights back to Scrimshaw. Like the browser extension, it is plain
JavaScript with no build step.

## Install

1. Copy this `obsidian/` folder into your vault at
   `<vault>/.obsidian/plugins/scrimshaw/` (so the folder contains
   `manifest.json`, `main.js`, `styles.css`).
2. In Obsidian: Settings → Community plugins → enable **Scrimshaw**. (Turn off
   Restricted/Safe mode if it's on.)
3. In Scrimshaw: Settings → API tokens → create a token with **read + write**
   scopes. Tokens are shown once.
4. In the plugin's settings: set the **Scrimshaw URL** (e.g.
   `https://scrimshaw.example.com`) and paste the **token**, then click
   **Test connection**.

## Commands

Run these from the command palette:

- **Scrimshaw: Sync** — pulls items + highlights and writes one note per item
  into the sync folder (default `Scrimshaw/`). By default it syncs your Read
  Later, Bookmarks, and Starred items; toggle what to include (and whether to
  sync all feed items) in settings.
- **Scrimshaw: Mark current note read** — marks the item read in Scrimshaw and
  updates the note's frontmatter. Available when the active note has a
  `scrimshaw_id`.
- **Scrimshaw: Send selection as highlight** — sends the selected text as a
  highlight on the item (with an optional note).
- **Scrimshaw: Save URL to read later** — saves a URL to Scrimshaw (pre-filled
  from the clipboard if it's a link).

## Notes

- Each note carries a `scrimshaw_id` in its frontmatter; that is how re-syncs
  find and update the note instead of duplicating it.
- **Re-syncing an existing note only refreshes its frontmatter** (read state,
  tags, flags), leaving the body untouched so your own annotations and links
  survive. To pull a fresh body, delete the note and sync again.
- The article body is converted from Scrimshaw's stored HTML to Markdown by a
  small built-in converter. Turn off "Include article body" in settings for
  metadata-only notes.
