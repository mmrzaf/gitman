// A long diff opens only its first files. Following a link to a file in the
// list of changed files opens that file before going to it.
import { on } from "./dom.js";

function reveal(id) {
  const file = id ? document.getElementById(id) : null;
  if (file instanceof HTMLDetailsElement && file.matches(".diff-file")) file.open = true;
}

on("click", "a[href^='#diff-']", (event, link) => reveal(link.getAttribute("href").slice(1)));
window.addEventListener("hashchange", () => reveal(location.hash.slice(1)));
reveal(location.hash.slice(1));
