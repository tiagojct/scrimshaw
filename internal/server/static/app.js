"use strict";

// Offline support for reader pages.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/service-worker.js").catch(() => {});
}

// Keyboard navigation for the item list and reader.
(function keyboardNav() {
  let awaitingG = false, cursor = 0;
  addEventListener("keydown", e => {
    if (e.target.matches("input, textarea, select")) return;
    const links = [...document.querySelectorAll('.items li a[href^="/items/"]')];
    if (awaitingG) {
      const dest = { a: "/", f: "/feeds/new" }[e.key];
      if (dest) location.href = dest;
      awaitingG = false;
      return;
    }
    if (e.key === "g") { awaitingG = true; }
    else if (e.key === "/") { document.querySelector('input[type="search"], input')?.focus(); e.preventDefault(); }
    else if (e.key === "j" && links.length) { cursor = Math.min(cursor + 1, links.length - 1); links[cursor].focus(); }
    else if (e.key === "k" && links.length) { cursor = Math.max(cursor - 1, 0); links[cursor].focus(); }
    else if (e.key === "o" && document.activeElement?.matches(".items li a")) { location.href = document.activeElement.href; }
    else if (e.key === "m") { document.querySelector(".read-form")?.requestSubmit(); }
    else if (e.key === "v") { document.querySelector(".original-link")?.click(); }
    else if (e.key === "a") { document.querySelector(".archive-form")?.requestSubmit(); }
  });
})();

// Highlights: render saved highlights inside the article, and let the reader
// create new ones by selecting text.
document.addEventListener("DOMContentLoaded", () => {
  const reader = document.querySelector(".reader");
  if (!reader) return;

  renderSavedHighlights(reader);
  wireSelectionPopover(reader);
});

function renderSavedHighlights(reader) {
  const dataNode = document.getElementById("hl-data");
  if (!dataNode) return;
  let quotes = [];
  try { quotes = JSON.parse(dataNode.textContent) || []; } catch { return; }
  for (const quote of quotes) {
    markQuote(reader, quote) || markQuote(reader, quote.trim());
  }
}

// markQuote wraps the first unmarked occurrence of quote (within a single text
// node) in <mark class="hl">. Returns true when a match was wrapped.
function markQuote(root, quote) {
  if (!quote) return false;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (node.parentNode && node.parentNode.nodeName === "MARK") return NodeFilter.FILTER_REJECT;
      return node.textContent.includes(quote) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP;
    },
  });
  const node = walker.nextNode();
  if (!node) return false;
  const index = node.textContent.indexOf(quote);
  const range = document.createRange();
  range.setStart(node, index);
  range.setEnd(node, index + quote.length);
  const mark = document.createElement("mark");
  mark.className = "hl";
  try { range.surroundContents(mark); } catch { return false; }
  return true;
}

function wireSelectionPopover(reader) {
  const pop = document.getElementById("hl-pop");
  const form = document.getElementById("hl-form");
  const quoteInput = document.getElementById("hl-quote");
  if (!pop || !form || !quoteInput) return;
  const button = pop.querySelector("button");

  function selectedInReader() {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
    const range = sel.getRangeAt(0);
    if (!reader.contains(range.commonAncestorContainer)) return null;
    const text = sel.toString().trim();
    if (text.length < 2) return null;
    return { text, rect: range.getBoundingClientRect() };
  }

  function showPopover() {
    const found = selectedInReader();
    if (!found) { pop.classList.remove("show"); return; }
    pop.style.left = window.scrollX + found.rect.left + found.rect.width / 2 + "px";
    pop.style.top = window.scrollY + found.rect.top + "px";
    pop.dataset.quote = found.text;
    pop.classList.add("show");
  }

  document.addEventListener("mouseup", () => setTimeout(showPopover, 0));
  document.addEventListener("keyup", e => { if (e.shiftKey) showPopover(); });
  document.addEventListener("selectionchange", () => {
    if (window.getSelection().isCollapsed) pop.classList.remove("show");
  });
  // Keep the selection alive while pressing the popover button.
  pop.addEventListener("mousedown", e => e.preventDefault());
  button.addEventListener("click", () => {
    const quote = pop.dataset.quote;
    if (!quote) return;
    quoteInput.value = quote;
    form.requestSubmit();
  });
}
