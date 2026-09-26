// The log on a Run page: one step's output, a numbered line at a time.
// It opens at the end for a step that is running or failed — that is
// where the news is — and follows new output while it stays scrolled to
// the end. Scrolling up stops following; a button then offers the way
// back to the newest line. Long lines can wrap, either end is a click
// away, and any line can be linked to by its number.
//
// New output arrives from live.js as "gitman:log" events. The page keeps
// at most maxLines lines, dropping the oldest, so a step that writes its
// full 16 MiB cannot slow the page down; the raw output always has all of
// it.
import { el, on } from "./dom.js";

const maxLines = 20000;
const log = document.querySelector("[data-log]");

if (log) {
  const errorLine = new RegExp(log.dataset.errorPattern, "i");
  const latest = document.querySelector("[data-log-latest]");
  const wrap = document.querySelector("[data-log-wrap]");
  const raw = document.querySelector("[data-log-raw]");
  const lines = log.getElementsByClassName("line");
  let tail = lines[lines.length - 1] || null;
  let last = tail ? Number(tail.id.slice(1)) : 0;
  // open: the last line has no newline yet, so output continues it.
  let open = log.dataset.logOpen === "true";

  const atEnd = () => log.scrollHeight - log.scrollTop - log.clientHeight < 24;
  const toEnd = () => { log.scrollTop = log.scrollHeight; };
  let following = false;

  function textOf(line) {
    return line.textContent.slice(line.firstChild.textContent.length);
  }

  function newLine() {
    last++;
    return el("div", { class: "line", id: `L${last}` }, el("a", { href: `#L${last}`, tabindex: "-1", "aria-hidden": "true" }, String(last)));
  }

  // trim drops the oldest lines past maxLines, and says so once.
  function trim() {
    const extra = lines.length - maxLines;
    if (extra <= 0) return;
    Array.from(lines).slice(0, extra).forEach((line) => line.remove());
    if (!log.querySelector("[data-log-trimmed]")) {
      log.prepend(el("p", { class: "log-note", "data-log-trimmed": true },
        "Older lines are dropped here to keep the page fast. ",
        el("a", { href: raw?.getAttribute("href") || "#" }, "Open the whole output"), "."));
    }
  }

  function append(text) {
    if (!text) return;
    log.querySelector("[data-log-empty]")?.remove();
    const parts = text.split("\n");
    const touched = new Set();
    parts.forEach((part, i) => {
      const final = i === parts.length - 1;
      if (i === 0 && open && tail) {
        tail.append(part);
        touched.add(tail);
      } else if (!(final && part === "")) {
        tail = newLine();
        tail.append(part);
        log.append(tail);
        touched.add(tail);
      }
    });
    open = parts[parts.length - 1] !== "";
    for (const line of touched) line.classList.toggle("is-error", errorLine.test(textOf(line)));
    trim();
    if (following) toEnd();
    else if (latest) latest.hidden = false;
  }

  function follow() {
    toEnd();
    following = true;
    if (latest) latest.hidden = true;
  }

  if (!location.hash.startsWith("#L") && ["running", "failed", "cancelled"].includes(log.dataset.logStatus)) follow();

  log.addEventListener("scroll", () => {
    following = atEnd();
    if (following && latest) latest.hidden = true;
  }, { passive: true });

  document.addEventListener("gitman:log", (event) => append(event.detail.content));

  latest?.addEventListener("click", () => {
    follow();
    log.focus({ preventScroll: true });
  });

  on("click", "[data-log-jump]", (event, button) => {
    if (button.dataset.logJump === "end") follow();
    else log.scrollTop = 0;
  });

  // Wrapping is a reader's preference, kept for their next visit.
  const setWrap = (wrapped) => {
    log.classList.toggle("is-wrapped", wrapped);
    wrap?.setAttribute("aria-pressed", String(wrapped));
    try { localStorage.setItem("gitman.log.wrap", wrapped ? "1" : ""); } catch { /* storage may be off */ }
  };
  let saved = false;
  try { saved = localStorage.getItem("gitman.log.wrap") === "1"; } catch { /* storage may be off */ }
  setWrap(saved);
  wrap?.addEventListener("click", () => setWrap(!log.classList.contains("is-wrapped")));
}
