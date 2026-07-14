"use strict";

// Scrimshaw plugin for Obsidian. Plain CommonJS, no build step — Obsidian
// loads this main.js directly, the same plain-JS ethos as the browser
// extension. It talks to Scrimshaw's bearer-token JSON API (see API.md):
// GET /api/items, GET /api/highlights, POST /api/save,
// POST /api/items/{id}/read, POST /api/items/{id}/highlights.

const {
  Plugin,
  PluginSettingTab,
  Setting,
  Notice,
  Modal,
  requestUrl,
  normalizePath,
  stringifyYaml,
} = require("obsidian");

const DEFAULT_SETTINGS = {
  origin: "",
  token: "",
  folder: "Scrimshaw",
  syncReadLater: true,
  syncBookmarks: true,
  syncStarred: true,
  syncFeed: false,
  includeContent: true,
};

module.exports = class ScrimshawPlugin extends Plugin {
  async onload() {
    await this.loadSettings();
    this.addSettingTab(new ScrimshawSettingTab(this.app, this));

    this.addCommand({ id: "sync", name: "Sync", callback: () => this.sync() });

    this.addCommand({
      id: "mark-read",
      name: "Mark current note read",
      checkCallback: (checking) => {
        const ctx = this.activeItemId();
        if (!ctx) return false;
        if (!checking) this.markRead(ctx.file, ctx.id);
        return true;
      },
    });

    this.addCommand({
      id: "add-highlight",
      name: "Send selection as highlight",
      editorCallback: (editor, view) => {
        const file = view.file;
        const id = file && this.idOf(file);
        if (!id) {
          new Notice("Scrimshaw: this note has no scrimshaw_id.");
          return;
        }
        const quote = editor.getSelection().trim();
        if (!quote) {
          new Notice("Scrimshaw: select the passage to highlight first.");
          return;
        }
        new PromptModal(this.app, "Add a note (optional)", "", async (note) => {
          await this.addHighlight(id, quote, note || "");
        }).open();
      },
    });

    this.addCommand({
      id: "save-url",
      name: "Save URL to read later",
      callback: async () => {
        let prefill = "";
        try {
          prefill = (await navigator.clipboard.readText()) || "";
        } catch (_) {
          prefill = "";
        }
        if (!/^https?:\/\//i.test(prefill)) prefill = "";
        new PromptModal(this.app, "URL to save to Scrimshaw", prefill, async (url) => {
          await this.saveURL((url || "").trim());
        }).open();
      },
    });
  }

  async loadSettings() {
    this.settings = Object.assign({}, DEFAULT_SETTINGS, await this.loadData());
  }

  async saveSettings() {
    await this.saveData(this.settings);
  }

  // --- API helper -----------------------------------------------------------

  originBase() {
    return this.settings.origin.replace(/\/$/, "");
  }

  async api(method, path, body) {
    if (!this.settings.origin || !this.settings.token) {
      throw new Error("Set the Scrimshaw URL and token in settings first.");
    }
    const opts = {
      url: this.originBase() + path,
      method,
      headers: { Authorization: "Bearer " + this.settings.token },
      throw: false,
    };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return requestUrl(opts);
  }

  // --- Commands -------------------------------------------------------------

  async sync() {
    let itemsResp;
    let hlResp;
    try {
      itemsResp = await this.api("GET", "/api/items");
      hlResp = await this.api("GET", "/api/highlights");
    } catch (e) {
      new Notice("Scrimshaw: " + e.message);
      return;
    }
    if (itemsResp.status === 401) {
      new Notice("Scrimshaw: token rejected (needs a read scope).");
      return;
    }
    if (itemsResp.status !== 200) {
      new Notice("Scrimshaw: sync failed (" + itemsResp.status + ").");
      return;
    }

    const items = itemsResp.json || [];
    const highlights = (hlResp.status === 200 && hlResp.json) || [];
    const hlByItem = {};
    for (const h of highlights) {
      (hlByItem[h.item_id] || (hlByItem[h.item_id] = [])).push(h);
    }

    const selected = items.filter((it) => this.wanted(it));
    await this.ensureFolder();
    const index = this.buildIdIndex();

    let created = 0;
    let updated = 0;
    for (const it of selected) {
      const existing = index[it.id];
      if (existing) {
        // Preserve any annotations/links the user added in the note body;
        // a re-sync only refreshes the metadata frontmatter.
        await this.app.fileManager.processFrontMatter(existing, (fm) =>
          this.applyFrontmatter(fm, it)
        );
        updated++;
      } else {
        const md = this.renderNote(it, hlByItem[it.id] || []);
        const path = await this.uniquePath(
          normalizePath(this.settings.folder + "/" + this.slug(it) + ".md")
        );
        await this.app.vault.create(path, md);
        created++;
      }
    }
    const skipped = items.length - selected.length;
    new Notice(
      `Scrimshaw: ${created} new, ${updated} updated, ${skipped} skipped.`
    );
  }

  async markRead(file, id) {
    let resp;
    try {
      resp = await this.api("POST", "/api/items/" + id + "/read", { read: true });
    } catch (e) {
      new Notice("Scrimshaw: " + e.message);
      return;
    }
    if (resp.status !== 200) {
      new Notice("Scrimshaw: mark read failed (" + resp.status + ").");
      return;
    }
    const item = resp.json;
    await this.app.fileManager.processFrontMatter(file, (fm) => {
      fm.read = !!item.read;
      if (item.read_at) fm.read_at = item.read_at;
    });
    new Notice("Scrimshaw: marked read.");
  }

  async addHighlight(id, quote, note) {
    let resp;
    try {
      resp = await this.api("POST", "/api/items/" + id + "/highlights", { quote, note });
    } catch (e) {
      new Notice("Scrimshaw: " + e.message);
      return;
    }
    new Notice(
      resp.status === 201
        ? "Scrimshaw: highlight saved."
        : "Scrimshaw: highlight failed (" + resp.status + ")."
    );
  }

  async saveURL(url) {
    if (!url) return;
    let resp;
    try {
      resp = await this.api("POST", "/api/save", { url, read_later: true });
    } catch (e) {
      new Notice("Scrimshaw: " + e.message);
      return;
    }
    new Notice(
      resp.status === 200
        ? "Scrimshaw: saved to read later."
        : "Scrimshaw: save failed (" + resp.status + ")."
    );
  }

  // --- Note building --------------------------------------------------------

  wanted(it) {
    const s = this.settings;
    if (s.syncReadLater && it.read_later) return true;
    if (s.syncBookmarks && it.bookmarked) return true;
    if (s.syncStarred && it.starred) return true;
    if (s.syncFeed && it.source === "feed") return true;
    return false;
  }

  applyFrontmatter(fm, it) {
    fm.scrimshaw_id = it.id;
    fm.url = it.url;
    fm.title = it.title || "";
    fm.source = it.source;
    fm.kind = it.kind;
    fm.tags = it.tags || [];
    fm.read_later = !!it.read_later;
    fm.bookmarked = !!it.bookmarked;
    fm.read = !!it.read;
    fm.starred = !!it.starred;
    fm.added_at = it.added_at;
    if (it.published_at) fm.published_at = it.published_at;
    if (it.read_at) fm.read_at = it.read_at;
  }

  renderNote(it, hls) {
    const fm = {};
    this.applyFrontmatter(fm, it);

    const origin = this.originBase();
    let body = "# " + (it.title || it.url) + "\n\n";
    body +=
      "[Open original](" +
      it.url +
      ") · [Open in Scrimshaw](" +
      origin +
      "/items/" +
      it.id +
      ")\n\n";

    if (this.settings.includeContent && it.content) {
      body += htmlToMarkdown(it.content, origin) + "\n";
    }

    if (hls.length) {
      body += "\n## Highlights\n\n";
      for (const h of hls) {
        body += "> " + String(h.quote || "").replace(/\n/g, "\n> ") + "\n";
        if (h.note) body += "\n" + h.note + "\n";
        body += "\n";
      }
    }

    return "---\n" + stringifyYaml(fm) + "---\n\n" + body;
  }

  // --- Vault helpers --------------------------------------------------------

  idOf(file) {
    const fm = this.app.metadataCache.getFileCache(file);
    return fm && fm.frontmatter ? fm.frontmatter.scrimshaw_id : undefined;
  }

  activeItemId() {
    const file = this.app.workspace.getActiveFile();
    const id = file && this.idOf(file);
    return id ? { file, id } : null;
  }

  buildIdIndex() {
    const idx = {};
    const folder = normalizePath(this.settings.folder);
    for (const file of this.app.vault.getMarkdownFiles()) {
      if (file.path !== folder && !file.path.startsWith(folder + "/")) continue;
      const cache = this.app.metadataCache.getFileCache(file);
      const id = cache && cache.frontmatter ? cache.frontmatter.scrimshaw_id : null;
      if (id != null) idx[id] = file;
    }
    return idx;
  }

  async ensureFolder() {
    const folder = normalizePath(this.settings.folder);
    if (!this.app.vault.getAbstractFileByPath(folder)) {
      await this.app.vault.createFolder(folder).catch(() => {});
    }
  }

  async uniquePath(path) {
    if (!this.app.vault.getAbstractFileByPath(path)) return path;
    const dot = path.lastIndexOf(".");
    const base = path.slice(0, dot);
    const ext = path.slice(dot);
    let i = 2;
    while (this.app.vault.getAbstractFileByPath(base + "-" + i + ext)) i++;
    return base + "-" + i + ext;
  }

  slug(it) {
    const base = (it.title || it.url || "item-" + it.id)
      .toLowerCase()
      .replace(/[^\w\s-]/g, "")
      .trim()
      .replace(/\s+/g, "-")
      .slice(0, 80);
    return base || "item-" + it.id;
  }
};

// --- HTML -> Markdown (zero-dependency; runs in Obsidian's renderer, so
// DOMParser is available). Handles the tags Scrimshaw's extracted content
// actually uses; unknown tags fall through to their text. Relative URLs
// (Scrimshaw's /images proxy) are resolved against the instance origin. ---

function htmlToMarkdown(html, origin) {
  const doc = new DOMParser().parseFromString(html, "text/html");
  const out = renderChildren(doc.body, origin);
  return out.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
}

function renderChildren(node, origin) {
  let out = "";
  node.childNodes.forEach((n) => {
    out += renderNode(n, origin);
  });
  return out;
}

function resolveURL(u, origin) {
  if (!u) return "";
  if (u.startsWith("//")) return "https:" + u;
  if (u.startsWith("/")) return origin + u;
  return u;
}

function renderNode(n, origin) {
  if (n.nodeType === 3) return n.textContent.replace(/\s+/g, " ");
  if (n.nodeType !== 1) return "";
  const tag = n.tagName.toLowerCase();
  const inner = renderChildren(n, origin);
  switch (tag) {
    case "h1":
      return "\n\n# " + inner.trim() + "\n\n";
    case "h2":
      return "\n\n## " + inner.trim() + "\n\n";
    case "h3":
      return "\n\n### " + inner.trim() + "\n\n";
    case "h4":
    case "h5":
    case "h6":
      return "\n\n#### " + inner.trim() + "\n\n";
    case "p":
      return "\n\n" + inner.trim() + "\n\n";
    case "br":
      return "\n";
    case "strong":
    case "b":
      return inner.trim() ? "**" + inner.trim() + "**" : "";
    case "em":
    case "i":
      return inner.trim() ? "*" + inner.trim() + "*" : "";
    case "a": {
      const href = resolveURL(n.getAttribute("href"), origin);
      return href ? "[" + inner.trim() + "](" + href + ")" : inner;
    }
    case "img": {
      const src = resolveURL(n.getAttribute("src"), origin);
      const alt = n.getAttribute("alt") || "";
      return src ? "\n\n![" + alt + "](" + src + ")\n\n" : "";
    }
    case "ul":
      return "\n\n" + listItems(n, origin, () => "- ") + "\n";
    case "ol":
      return "\n\n" + listItems(n, origin, (i) => i + 1 + ". ") + "\n";
    case "blockquote":
      return "\n\n> " + inner.trim().replace(/\n+/g, "\n> ") + "\n\n";
    case "pre":
      return "\n\n```\n" + n.textContent.replace(/\n+$/, "") + "\n```\n\n";
    case "code":
      return n.parentElement && n.parentElement.tagName.toLowerCase() === "pre"
        ? inner
        : "`" + inner.trim() + "`";
    case "hr":
      return "\n\n---\n\n";
    case "figcaption":
      return inner.trim() ? "\n\n*" + inner.trim() + "*\n\n" : "";
    case "script":
    case "style":
      return "";
    default:
      return inner;
  }
}

function listItems(listNode, origin, marker) {
  let out = "";
  let i = 0;
  listNode.childNodes.forEach((n) => {
    if (n.nodeType === 1 && n.tagName.toLowerCase() === "li") {
      const text = renderChildren(n, origin).trim().replace(/\n+/g, " ");
      out += marker(i) + text + "\n";
      i++;
    }
  });
  return out;
}

// --- A one-line text prompt modal ------------------------------------------

class PromptModal extends Modal {
  constructor(app, title, prefill, onSubmit) {
    super(app);
    this.titleText = title;
    this.prefill = prefill || "";
    this.onSubmit = onSubmit;
  }

  onOpen() {
    const { contentEl } = this;
    contentEl.createEl("h3", { text: this.titleText });
    const input = contentEl.createEl("input", {
      type: "text",
      cls: "scrimshaw-prompt-input",
    });
    input.value = this.prefill;
    input.focus();
    input.select();
    const submit = () => {
      const value = input.value;
      this.close();
      this.onSubmit(value);
    };
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submit();
      }
    });
    const btn = contentEl.createEl("button", {
      text: "OK",
      cls: "mod-cta scrimshaw-prompt-ok",
    });
    btn.addEventListener("click", submit);
  }

  onClose() {
    this.contentEl.empty();
  }
}

// --- Settings tab ----------------------------------------------------------

class ScrimshawSettingTab extends PluginSettingTab {
  constructor(app, plugin) {
    super(app, plugin);
    this.plugin = plugin;
  }

  display() {
    const { containerEl } = this;
    containerEl.empty();

    new Setting(containerEl)
      .setName("Scrimshaw URL")
      .setDesc("Base URL of your instance, e.g. https://scrimshaw.example.com")
      .addText((t) =>
        t
          .setPlaceholder("https://...")
          .setValue(this.plugin.settings.origin)
          .onChange(async (v) => {
            this.plugin.settings.origin = v.trim();
            await this.plugin.saveSettings();
          })
      );

    new Setting(containerEl)
      .setName("API token")
      .setDesc("A read+write token from Scrimshaw's Settings → API tokens.")
      .addText((t) => {
        t.inputEl.type = "password";
        t.setValue(this.plugin.settings.token).onChange(async (v) => {
          this.plugin.settings.token = v.trim();
          await this.plugin.saveSettings();
        });
      });

    new Setting(containerEl)
      .setName("Sync folder")
      .setDesc("Vault folder where item notes are written.")
      .addText((t) =>
        t.setValue(this.plugin.settings.folder).onChange(async (v) => {
          this.plugin.settings.folder =
            v.trim().replace(/^\/+|\/+$/g, "") || "Scrimshaw";
          await this.plugin.saveSettings();
        })
      );

    containerEl.createEl("h3", { text: "What to sync" });

    const toggle = (name, key, desc) =>
      new Setting(containerEl)
        .setName(name)
        .setDesc(desc || "")
        .addToggle((t) =>
          t.setValue(this.plugin.settings[key]).onChange(async (v) => {
            this.plugin.settings[key] = v;
            await this.plugin.saveSettings();
          })
        );

    toggle("Read Later", "syncReadLater");
    toggle("Bookmarks", "syncBookmarks");
    toggle("Starred", "syncStarred");
    toggle(
      "All feed items",
      "syncFeed",
      "Off by default — this can be a lot of notes."
    );
    toggle(
      "Include article body",
      "includeContent",
      "Convert the extracted article to Markdown in the note body."
    );

    new Setting(containerEl)
      .setName("Test connection")
      .setDesc("Checks the URL and token against GET /api/items.")
      .addButton((b) =>
        b.setButtonText("Test").onClick(async () => {
          try {
            const r = await this.plugin.api("GET", "/api/items");
            if (r.status === 200) {
              new Notice("Scrimshaw: connected, " + (r.json || []).length + " items.");
            } else {
              new Notice("Scrimshaw: " + r.status + " (check token / scope).");
            }
          } catch (e) {
            new Notice("Scrimshaw: " + e.message);
          }
        })
      );
  }
}
