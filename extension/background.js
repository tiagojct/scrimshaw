// Background service worker: context menu + keyboard shortcut saving.
// Reads the same origin/token the popup stores in chrome.storage.local.

async function saveURL(url, readLater) {
  const { origin, token } = await chrome.storage.local.get(["origin", "token"]);
  if (!origin || !token) {
    flash("Set the Scrimshaw URL and token in the extension popup first.");
    return;
  }
  try {
    const response = await fetch(origin.replace(/\/$/, "") + "/api/save", {
      method: "POST",
      headers: { "Authorization": "Bearer " + token, "Content-Type": "application/json" },
      body: JSON.stringify({ url, read_later: readLater }),
    });
    flash(response.ok ? (readLater ? "Saved to read later." : "Bookmarked.") : "Save failed.");
  } catch (_) {
    flash("Could not reach Scrimshaw.");
  }
}

// Brief feedback via the toolbar badge (no notifications permission needed).
function flash(message) {
  chrome.action.setTitle({ title: message });
  chrome.action.setBadgeBackgroundColor({ color: "#0b62cf" });
  chrome.action.setBadgeText({ text: "✓" });
  setTimeout(() => {
    chrome.action.setBadgeText({ text: "" });
    chrome.action.setTitle({ title: "Save to Scrimshaw" });
  }, 2000);
}

const MENUS = [
  { id: "later-page", title: "Save page to read later", contexts: ["page"], readLater: true },
  { id: "bookmark-page", title: "Bookmark this page", contexts: ["page"], readLater: false },
  { id: "later-link", title: "Save link to read later", contexts: ["link"], readLater: true },
  { id: "bookmark-link", title: "Bookmark this link", contexts: ["link"], readLater: false },
];

chrome.runtime.onInstalled.addListener(() => {
  for (const m of MENUS) {
    chrome.contextMenus.create({ id: m.id, title: m.title, contexts: m.contexts });
  }
});

chrome.contextMenus.onClicked.addListener((info, tab) => {
  const menu = MENUS.find(m => m.id === info.menuItemId);
  if (!menu) return;
  const url = info.linkUrl || info.pageUrl || (tab && tab.url);
  if (url) saveURL(url, menu.readLater);
});

chrome.commands.onCommand.addListener(async (command) => {
  if (command !== "save-read-later") return;
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab && tab.url) saveURL(tab.url, true);
});
