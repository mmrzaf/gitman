// Dismiss Home attention items in this browser, without changing server
// state or hiding them for other people. New run/deployment IDs produce new
// attention keys and can be surfaced again.
import { on } from "./dom.js";

let showIgnored = false;
const fallback = new Map();

function storageKey(region) {
  return `gitman.attention.${region.dataset.attentionOwner}`;
}

function load(region) {
  try {
    const saved = JSON.parse(localStorage.getItem(storageKey(region)) || "[]");
    return new Set(Array.isArray(saved) ? saved : []);
  } catch { return new Set(fallback.get(storageKey(region)) || []); }
}

function save(region, keys) {
  fallback.set(storageKey(region), [...keys]);
  try { localStorage.setItem(storageKey(region), JSON.stringify([...keys])); }
  catch { /* Storage disabled: retain dismissals for this page session. */ }
}

function refresh() {
  const region = document.querySelector("[data-attention-owner]");
  if (!region) return;
  const dismissed = load(region);
  const items = [...region.querySelectorAll("[data-attention-key]")];
  let ignored = 0, visible = 0;
  for (const item of items) {
    const isIgnored = dismissed.has(item.dataset.attentionKey);
    if (isIgnored) ignored++;
    item.classList.toggle("is-ignored", isIgnored);
    item.hidden = isIgnored && !showIgnored;
    item.querySelector("[data-attention-dismiss]").textContent = isIgnored ? "Restore" : "Ignore";
    if (!item.hidden) visible++;
  }
  if (ignored === 0) showIgnored = false;
  const toggle = region.querySelector("[data-attention-toggle]");
  if (toggle) {
    toggle.hidden = ignored === 0;
    toggle.textContent = showIgnored ? "Hide ignored" : `Show ignored (${ignored})`;
  }
  const empty = region.querySelector("[data-attention-empty]");
  if (empty) empty.hidden = visible !== 0;
  region.classList.toggle("is-alert", items.length > ignored);
  region.classList.toggle("has-ignored", ignored > 0);
}

on("click", "[data-attention-dismiss]", (_event, button) => {
  const item = button.closest("[data-attention-key]");
  const region = item?.closest("[data-attention-owner]");
  if (!region) return;
  const dismissed = load(region);
  if (dismissed.has(item.dataset.attentionKey)) dismissed.delete(item.dataset.attentionKey);
  else dismissed.add(item.dataset.attentionKey);
  save(region, dismissed);
  refresh();
  // Hiding the focused item would drop focus to the document; keep the
  // keyboard on the next visible action instead.
  const next = [...region.querySelectorAll("[data-attention-dismiss]")]
    .find((candidate) => !candidate.closest("[data-attention-key]").hidden);
  (next || region.querySelector("[data-attention-toggle]"))?.focus();
});

on("click", "[data-attention-toggle]", () => {
  showIgnored = !showIgnored;
  refresh();
});

document.addEventListener("gitman:refreshed", refresh);
refresh();
