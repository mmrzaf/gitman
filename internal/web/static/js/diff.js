// The changes of a commit or comparison are a list of files that each open to
// their diff. Expand all and Collapse all open or close every one of a list,
// and following a link to one file opens it before going to it.
import { on } from "./dom.js";

function reveal(id) {
  const file = id ? document.getElementById(id) : null;
  if (file instanceof HTMLDetailsElement && file.matches(".diff-file")) file.open = true;
}

on("click", "[data-diff-all]", (event, button) => {
  const open = button.dataset.diffAll === "open";
  for (const file of button.closest("[data-diff]").querySelectorAll(".diff-file")) file.open = open;
});
on("click", "a[href^='#diff-']", (event, link) => reveal(link.getAttribute("href").slice(1)));
window.addEventListener("hashchange", () => reveal(location.hash.slice(1)));
reveal(location.hash.slice(1));
