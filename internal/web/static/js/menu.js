// Menus. A <details data-menu> whose <summary> opens a list of links and
// buttons. Without scripting it is a plain disclosure, which works well
// enough. Here it becomes a menu button: opening it moves focus to the
// first item, arrow keys move between items, and Escape, Tab or a click
// anywhere else closes it — only one menu is open at a time.
import { on } from "./dom.js";

function itemsOf(menu) {
  return [...menu.querySelectorAll(".menu-list a[href], .menu-list button")];
}

function upgrade(menu) {
  const summary = menu.querySelector("summary");
  const list = menu.querySelector(".menu-list");
  summary.setAttribute("aria-haspopup", "menu");
  summary.setAttribute("aria-expanded", String(menu.open));
  list.setAttribute("role", "menu");
  for (const child of list.querySelectorAll("form, .menu-heading")) child.setAttribute("role", "none");
  for (const item of itemsOf(menu)) {
    item.setAttribute("role", "menuitem");
    item.tabIndex = -1;
  }
}

// place pins an open menu's list to the viewport, below its button — or
// above it when there is no room below — so a menu in a scrolling table
// is not cut off by the table's edge.
function place(menu) {
  const list = menu.querySelector(".menu-list");
  const button = menu.querySelector("summary").getBoundingClientRect();
  list.classList.add("menu-list-floating");
  const { width, height } = list.getBoundingClientRect();
  const alignStart = menu.classList.contains("menu-start");
  let left = alignStart ? button.left : button.right - width;
  left = Math.min(Math.max(left, 8), window.innerWidth - width - 8);
  const below = button.bottom + 4;
  const top = below + height > window.innerHeight - 8 && button.top - height - 4 > 8 ? button.top - height - 4 : below;
  list.style.setProperty("--menu-left", `${left}px`);
  list.style.setProperty("--menu-top", `${top}px`);
}

function close(menu, { restoreFocus = false } = {}) {
  if (!menu.open) return;
  menu.open = false;
  if (restoreFocus) menu.querySelector("summary").focus();
}

for (const menu of document.querySelectorAll("[data-menu]")) upgrade(menu);

// Menus in a region a live update replaced are new elements.
document.addEventListener("gitman:refreshed", () => {
  for (const menu of document.querySelectorAll("[data-menu]:not(:has([aria-haspopup]))")) upgrade(menu);
});

document.addEventListener("toggle", (event) => {
  const menu = event.target;
  if (!(menu instanceof HTMLDetailsElement) || !menu.matches("[data-menu]")) return;
  menu.querySelector("summary").setAttribute("aria-expanded", String(menu.open));
  if (!menu.open) return;
  for (const other of document.querySelectorAll("[data-menu][open]")) if (other !== menu) close(other);
  place(menu);
  itemsOf(menu)[0]?.focus({ preventScroll: true });
}, true);

on("keydown", "[data-menu]", (event, menu) => {
  const items = itemsOf(menu);
  const i = items.indexOf(document.activeElement);
  let next = null;
  switch (event.key) {
    case "Escape": event.preventDefault(); close(menu, { restoreFocus: true }); return;
    case "Tab": close(menu); return;
    case "ArrowDown":
      if (!menu.open) { menu.open = true; event.preventDefault(); return; }
      next = items[(i + 1) % items.length]; break;
    case "ArrowUp": next = items[(i - 1 + items.length) % items.length]; break;
    case "Home": next = items[0]; break;
    case "End": next = items[items.length - 1]; break;
    default: return;
  }
  if (!menu.open) return;
  event.preventDefault();
  next?.focus();
});

// A pinned menu would drift from its button once whatever holds the
// button scrolls — the page, or a table — so it closes then. Scrolling
// elsewhere, such as a log following its output, leaves it open.
window.addEventListener("resize", () => document.querySelectorAll("[data-menu][open]").forEach((menu) => close(menu)));
document.addEventListener("scroll", (event) => {
  const scroller = event.target instanceof Element ? event.target : document.documentElement;
  for (const menu of document.querySelectorAll("[data-menu][open]")) {
    if (scroller.contains(menu) && !menu.contains(scroller)) close(menu);
  }
}, { capture: true, passive: true });

document.addEventListener("click", (event) => {
  for (const menu of document.querySelectorAll("[data-menu][open]")) {
    if (!menu.contains(event.target)) close(menu);
  }
});
