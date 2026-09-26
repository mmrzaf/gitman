// Toasts: the outcome of an action, in the corner of the page. The server
// renders the last action's outcome as one; here it is announced, can be
// dismissed, and — unless it is an error — goes away after a while, but
// never while the pointer or keyboard focus is on it.
import { el, icon, on } from "./dom.js";

const region = document.querySelector("[data-toasts]");
const lifetime = { success: 6000, info: 8000 };

function dismiss(toast) {
  toast.dataset.leaving = "";
  setTimeout(() => toast.remove(), 200);
}

function schedule(toast) {
  const kind = [...toast.classList].find((c) => c.startsWith("toast-"))?.slice(6);
  const ms = lifetime[kind];
  if (!ms) return;
  let timer = setTimeout(() => dismiss(toast), ms);
  const pause = () => clearTimeout(timer);
  const resume = () => {
    if (toast.matches(":hover, :focus-within")) return;
    timer = setTimeout(() => dismiss(toast), ms / 2);
  };
  toast.addEventListener("pointerenter", pause);
  toast.addEventListener("focusin", pause);
  toast.addEventListener("pointerleave", resume);
  toast.addEventListener("focusout", resume);
}

// showToast shows message as a toast of kind "success", "info" or "error".
export function showToast(message, kind = "info") {
  const names = { success: "check", error: "warning", info: "info" };
  const toast = el("div", { class: `toast toast-${kind}`, role: kind === "error" ? "alert" : "status", "data-toast": true },
    icon(names[kind]),
    el("p", { class: "toast-message" }, message),
    el("button", { type: "button", class: "btn btn-ghost btn-icon btn-sm", "data-toast-close": true, "aria-label": "Dismiss" }, icon("close")));
  region.append(toast);
  schedule(toast);
}

on("click", "[data-toast-close]", (event, button) => dismiss(button.closest("[data-toast]")));

// The server's toasts were there before anything could listen: they are
// taken out and put back, so the live region announces them.
if (region) {
  const initial = [...region.querySelectorAll("[data-toast]")];
  region.setAttribute("aria-live", "polite");
  initial.forEach((toast) => toast.remove());
  setTimeout(() => {
    for (const toast of initial) {
      region.append(toast);
      schedule(toast);
    }
  }, 100);
}
