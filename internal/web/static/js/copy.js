// Copy buttons: a button with data-copy="<text>" copies the text — as an
// absolute address, with data-copy-url — and says so, briefly, in its own
// label and to screen readers.
import { announce, icon, on } from "./dom.js";
import { showToast } from "./toast.js";

const originals = new WeakMap();
const timers = new WeakMap();

// copyText uses the Clipboard API where the browser allows it, and falls
// back to selecting a temporary textarea and execCommand("copy") on plain
// HTTP, where navigator.clipboard is undefined outside localhost.
async function copyText(text) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch { /* fall through */ }
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

on("click", "[data-copy]", async (event, button) => {
  let text = button.dataset.copy;
  if (button.hasAttribute("data-copy-url")) text = new URL(text, location.href).href;
  if (!(await copyText(text))) {
    showToast("Couldn't copy. Select the text and copy it yourself.", "error");
    return;
  }
  if (!originals.has(button)) originals.set(button, [...button.childNodes]);
  clearTimeout(timers.get(button));
  // An icon-only button keeps its size: only the icon changes.
  const iconOnly = button.classList.contains("btn-icon");
  button.replaceChildren(icon("check"), ...(iconOnly ? [] : ["Copied"]));
  announce("Copied to the clipboard.");
  timers.set(button, setTimeout(() => button.replaceChildren(...originals.get(button)), 1500));
});
