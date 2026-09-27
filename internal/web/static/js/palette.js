// The command palette: Ctrl+K (⌘K on a Mac), or the "Jump to…" link,
// opens a picker over what the current page can do — any link or button
// marked data-command="<what it does>" — and everywhere Gitman can go,
// which it reads from the Jump to page (/jump), once per page. Without
// scripting, that link simply leads to the Jump to page.
import { on } from "./dom.js";
import { Picker } from "./picker.js";

const mac = /Mac|iPhone|iPad/.test(navigator.platform);
let picker = null;
let destinations = null;

for (const label of document.querySelectorAll("[data-shortcut-label]")) {
  label.textContent = mac ? "⌘K" : "Ctrl K";
}

function iconOf(node) {
  const name = [...(node.querySelector(".icon")?.classList || [])].find((c) => c.startsWith("icon-"));
  return name ? name.slice(5) : null;
}

function commands() {
  return [...document.querySelectorAll("[data-command]")].map((node) => ({
    label: node.dataset.command,
    group: "On this page",
    icon: iconOf(node),
    run: () => node.click(),
  }));
}

function linksIn(root) {
  return [...root.querySelectorAll("[data-jump] a[data-group]")].map((a) => ({
    label: a.dataset.label || a.textContent.trim(),
    href: a.getAttribute("href"),
    group: a.dataset.group,
    icon: a.dataset.icon,
    hint: a.dataset.hint,
  }));
}

// load reads the Jump to page's links. A failure is not kept, so the next
// opening tries again; meanwhile the page's own commands still work.
function load() {
  destinations ||= fetch("/jump", { headers: { Accept: "text/html" } })
    .then((response) => {
      if (!response.ok || response.redirected) throw new Error("unavailable");
      return response.text();
    })
    .then((html) => linksIn(new DOMParser().parseFromString(html, "text/html")))
    .catch(() => {
      destinations = null;
      return [];
    });
  return destinations;
}

async function open() {
  picker ||= new Picker({ label: "Jump to", placeholder: "Go to a page, repository or action…" });
  const own = commands();
  if (document.querySelector("[data-jump]")) {
    picker.setItems([...own, ...linksIn(document)]);
    picker.open();
    return;
  }
  picker.setItems(own);
  picker.open();
  const places = await load();
  if (picker.dialog.open) picker.setItems([...own, ...places]);
}

on("click", "[data-palette-open]", (event) => {
  if (event.metaKey || event.ctrlKey || event.shiftKey) return;
  event.preventDefault();
  open();
});

document.addEventListener("keydown", (event) => {
  if (event.key.toLowerCase() !== "k" || !(mac ? event.metaKey : event.ctrlKey) || event.altKey || event.shiftKey) return;
  event.preventDefault();
  if (picker?.dialog.open) picker.close();
  else if (!document.querySelector("dialog:modal")) open();
});
