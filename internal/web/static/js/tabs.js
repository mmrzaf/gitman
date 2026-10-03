// Tabs. A [data-tabs] list holds links, each [data-tab="name"] leading to
// the same page with that tab open; [data-tab-panel="name"] is its panel.
// The server renders whichever tab the address names, so every tab has a
// real address. Here the links become an ARIA tab list: choosing a tab
// switches in place and records the address, arrow keys move between
// tabs, and Back returns to the tab before.
import { on } from "./dom.js";
import { push } from "./address.js";

function tabsOf(list) {
  return [...list.querySelectorAll("[data-tab]")];
}

function panelOf(tab) {
  return document.querySelector(`[data-tab-panel="${CSS.escape(tab.dataset.tab)}"]`);
}

function select(list, chosen) {
  for (const tab of tabsOf(list)) {
    const selected = tab === chosen;
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
    const panel = panelOf(tab);
    if (panel) panel.hidden = !selected;
  }
}

// fromAddress is the tab the current address names: its ?tab=, or the
// first tab, which is every list's default.
function fromAddress(list) {
  const name = new URL(location.href).searchParams.get("tab");
  const tabs = tabsOf(list);
  return tabs.find((tab) => tab.dataset.tab === name) || tabs[0];
}

// Tab lists are set up once each, and again for any that a page swap or a
// live refresh brings in.
function setup() {
  for (const list of document.querySelectorAll("[data-tabs]:not([data-tabs-ready])")) {
    list.dataset.tabsReady = "";
    list.setAttribute("role", "tablist");
    for (const tab of tabsOf(list)) {
      tab.setAttribute("role", "tab");
      tab.removeAttribute("aria-current");
      const panel = panelOf(tab);
      if (panel) {
        panel.setAttribute("role", "tabpanel");
        panel.setAttribute("aria-labelledby", tab.id);
        tab.setAttribute("aria-controls", panel.id);
      }
    }
    select(list, fromAddress(list));
  }
}

setup();
document.addEventListener("gitman:refreshed", setup);

function activate(tab) {
  const list = tab.closest("[data-tabs]");
  select(list, tab);
  push(new URL(tab.href));
}

on("click", "[data-tabs] [data-tab]", (event, tab) => {
  // A modified click still opens the tab's address in a new tab or window.
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  activate(tab);
});

on("keydown", "[data-tabs] [data-tab]", (event, tab) => {
  const list = tab.closest("[data-tabs]");
  const tabs = tabsOf(list);
  const vertical = list.getAttribute("aria-orientation") === "vertical";
  const i = tabs.indexOf(tab);
  let next = null;
  switch (event.key) {
    case vertical ? "ArrowDown" : "ArrowRight": next = tabs[(i + 1) % tabs.length]; break;
    case vertical ? "ArrowUp" : "ArrowLeft": next = tabs[(i - 1 + tabs.length) % tabs.length]; break;
    case "Home": next = tabs[0]; break;
    case "End": next = tabs[tabs.length - 1]; break;
    case " ": next = tab; break;
    default: return;
  }
  event.preventDefault();
  activate(next);
  next.focus();
});

window.addEventListener("popstate", () => {
  for (const list of document.querySelectorAll("[data-tabs]")) select(list, fromAddress(list));
});
