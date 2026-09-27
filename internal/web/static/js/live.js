// Live pages. A page that can change while it is open names an event
// stream on <main data-live-events>. When something on it changes, the
// page fetches its own address again and swaps in the regions marked
// data-live-region, so the server-rendered page stays the only
// description of what is going on. A Run page also gets its shown step's
// output as "log" events, handed to log.js as "gitman:log" events.
//
// Keyboard focus survives a swap: the focused element's counterpart in
// the new region is focused again. What changed is announced to screen
// readers, and [data-live-status] shows whether the page is connected.
import { announce, focusable } from "./dom.js";

const main = document.querySelector("main[data-live-events]");
const statuses = [...document.querySelectorAll("[data-live-status]")];
const labels = { connecting: "Connecting…", open: "Live", retrying: "Reconnecting…" };

function setStatus(state) {
  for (const node of statuses) {
    node.dataset.state = state;
    node.hidden = state === "done";
    const label = node.querySelector("[data-live-label]");
    if (label && labels[state]) label.textContent = labels[state];
  }
}

// focusKey finds an element's counterpart in a freshly rendered region:
// by id, by link address, by its form's action, or else by its place
// among the region's focusable elements.
function focusKey(region, node) {
  if (node.id) return (fresh) => fresh.querySelector(`#${CSS.escape(node.id)}`);
  if (node.matches("a[href]")) return (fresh) => fresh.querySelector(`a[href="${CSS.escape(node.getAttribute("href"))}"]`);
  const form = node.closest("form[action]");
  if (form) return (fresh) => fresh.querySelector(`form[action="${CSS.escape(form.getAttribute("action"))}"] button`);
  const i = focusable(region).indexOf(node);
  return (fresh) => focusable(fresh)[i] || null;
}

function announcements(root) {
  return [...root.querySelectorAll("[data-announce]")].map((node) => node.dataset.announce);
}

if (!main || !("EventSource" in window)) {
  setStatus("done");
} else {
  const log = document.querySelector("[data-log]");
  const url = new URL(main.dataset.liveEvents, location.href);
  if (log) {
    url.searchParams.set("step", log.dataset.logStep);
    url.searchParams.set("after", log.dataset.logAfter);
  }
  let source = null;
  let pending = null;
  let latest = 0;
  let retryDelay = 3000;

  const scheduleRefresh = () => {
    clearTimeout(pending);
    pending = setTimeout(refresh, 300);
  };

  // An EventSource retries by itself after a dropped connection, but an
  // answer that is not a stream — a proxy's 502 during a restart, an
  // error page, a redirect to sign in — ends it for good. Then a new one
  // is opened, backing off, resuming the log after the last line shown,
  // and the page is refreshed for whatever changed in between.
  const connect = () => {
    setStatus("connecting");
    source = new EventSource(url);
    source.addEventListener("open", () => {
      retryDelay = 3000;
      setStatus("open");
    });
    source.addEventListener("change", scheduleRefresh);
    source.addEventListener("log", (event) => {
      if (event.lastEventId) url.searchParams.set("after", event.lastEventId);
      document.dispatchEvent(new CustomEvent("gitman:log", { detail: JSON.parse(event.data) }));
    });
    source.addEventListener("error", () => {
      setStatus("retrying");
      if (source.readyState !== EventSource.CLOSED) return;
      setTimeout(() => { connect(); scheduleRefresh(); }, retryDelay);
      retryDelay = Math.min(retryDelay * 2, 60000);
    });
  };
  connect();

  async function refresh() {
    // Only the newest refresh may change the page: an older one that
    // answers late must not put back what a newer one already replaced.
    const mine = ++latest;
    let response, text;
    try {
      response = await fetch(location.href, { headers: { Accept: "text/html" } });
      text = await response.text();
    } catch {
      return;
    }
    if (mine !== latest || !response.ok) return;
    // Sent to sign in: this page's session has ended, so show that.
    if (response.redirected) {
      location.reload();
      return;
    }
    const fresh = new DOMParser().parseFromString(text, "text/html");

    // Following the run rather than a step picked by hand: when the run
    // starts its first step, or moves on to the next, show that step's
    // output instead.
    const freshLog = fresh.querySelector("[data-log]");
    const picked = new URL(location.href).searchParams.has("step");
    if (freshLog && !picked && (!log || freshLog.dataset.logStep !== log.dataset.logStep)) {
      location.reload();
      return;
    }
    const before = announcements(document);
    for (const region of document.querySelectorAll("[data-live-region]")) {
      const replacement = fresh.querySelector(`[data-live-region="${region.dataset.liveRegion}"]`);
      if (!replacement) continue;
      const focused = region.contains(document.activeElement) ? focusKey(region, document.activeElement) : null;
      const imported = document.importNode(replacement, true);
      region.replaceWith(imported);
      if (focused) focused(imported)?.focus({ preventScroll: true });
    }
    document.dispatchEvent(new CustomEvent("gitman:refreshed"));
    const changed = announcements(document).filter((words) => !before.includes(words));
    if (changed.length) announce(changed.join(". "));
    // A finished run has nothing left to update.
    if (!fresh.querySelector("main[data-live-events]")) {
      source.close();
      setStatus("done");
    }
  }
}
