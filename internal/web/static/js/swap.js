// In-place navigation. A form or link marked data-swap loads its address and
// puts what comes back where the page's [data-swap-region] is, instead of
// reloading the whole page, so choosing another branch in a list changes the
// list and nothing else. The address still changes, Back and Forward still
// work, and anything that cannot be swapped — an error, a sign-in, a network
// failure — is simply opened as the page it is.
import { on } from "./dom.js";
import { push } from "./address.js";

const region = () => document.querySelector("[data-swap-region]");
let latest = 0;

// keyOf names the view an address shows: everything but the tab, which
// switches in place without a request.
function keyOf() {
  const url = new URL(location.href);
  url.searchParams.delete("tab");
  return url.pathname + url.search;
}
let shown = keyOf();

async function swap(url, record) {
  const current = region();
  if (!current) {
    location.href = url;
    return;
  }
  // Only the newest request may change the page.
  const mine = ++latest;
  current.setAttribute("aria-busy", "true");
  const focusId = current.contains(document.activeElement) ? document.activeElement.id : "";
  let response, text;
  try {
    response = await fetch(url, { headers: { Accept: "text/html" } });
    text = await response.text();
  } catch {
    location.href = url;
    return;
  }
  if (mine !== latest) return;
  const fresh = new DOMParser().parseFromString(text, "text/html");
  const replacement = fresh.querySelector("[data-swap-region]");
  if (!response.ok || response.redirected || !replacement) {
    location.href = url;
    return;
  }
  region().replaceWith(document.importNode(replacement, true));
  document.title = fresh.title;
  if (record) push(new URL(url, location.href), { swap: true });
  shown = keyOf();
  document.dispatchEvent(new CustomEvent("gitman:refreshed"));
  if (focusId) document.getElementById(focusId)?.focus({ preventScroll: true });
}

// addressOf is where a form sends: its action, and the fields that have a
// value.
function addressOf(form) {
  const query = new URLSearchParams();
  for (const [name, value] of new FormData(form)) if (String(value) !== "") query.append(name, value);
  const url = new URL(form.getAttribute("action") || location.pathname, location.href);
  url.search = query.toString();
  return url.pathname + url.search;
}

on("submit", "form[data-swap]", (event, form) => {
  if (form.method !== "get") return;
  event.preventDefault();
  swap(addressOf(form), true);
});

on("click", "a[data-swap]", (event, link) => {
  // A modified click still opens the address in a new tab or window.
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  swap(link.getAttribute("href"), true);
});

// Back and Forward between swapped views show the view the address names.
window.addEventListener("popstate", () => {
  if (keyOf() !== shown) swap(location.pathname + location.search, false);
});
