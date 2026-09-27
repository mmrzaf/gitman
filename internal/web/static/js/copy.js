// Copy buttons: a button with data-copy="<text>" copies the text — as an
// absolute address, with data-copy-url — and says so, briefly, in its own
// label and to screen readers.
import { announce, icon, on } from "./dom.js";
import { showToast } from "./toast.js";

const originals = new WeakMap();
const timers = new WeakMap();

on("click", "[data-copy]", async (event, button) => {
  let text = button.dataset.copy;
  if (button.hasAttribute("data-copy-url")) text = new URL(text, location.href).href;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
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
