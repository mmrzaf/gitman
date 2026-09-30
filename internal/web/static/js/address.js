// A page's address carries its view — which tab is open, which dialog —
// so a link, a reload and Back all show the same thing. These change it
// without reloading the page.

// withParams is the current address with each of params set, or removed
// where its value is null or "".
export function withParams(params) {
  const url = new URL(location.href);
  for (const [name, value] of Object.entries(params)) {
    if (value == null || value === "") url.searchParams.delete(name);
    else url.searchParams.set(name, value);
  }
  return url;
}

// push makes url the address as a new history entry, which Back leaves.
// state marks the entry, so a module can tell an entry it made.
export function push(url, state = {}) {
  if (String(url) !== location.href) history.pushState(state, "", url);
}

// replace makes url the address in place of the current one.
export function replace(url) {
  if (String(url) !== location.href) history.replaceState(null, "", url);
}

// filesURL is the address of path at ref in repo — the Files page — each
// part encoded, so a name with "#", "?" or "%" in it still leads there.
export function filesURL(repo, ref, path = "") {
  const encoded = (s) => s.split("/").map(encodeURIComponent).join("/");
  return `/${encodeURIComponent(repo)}@${encoded(ref)}${path ? `/${encoded(path)}` : ""}`;
}

// historyURL is the address of History at ref in repo, filtered to path.
export function historyURL(repo, ref, path = "") {
  const query = new URLSearchParams({ ref });
  if (path) query.set("path", path);
  return `/${encodeURIComponent(repo)}/history?${query}`;
}
