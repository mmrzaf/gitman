// The branch and tag picker of Files and History goes to the chosen ref
// as soon as it changes, keeping the current path. Without scripting, its
// Switch button submits the same choice: Files redirects, History reads it.
import { on } from "./dom.js";
import { filesURL, historyURL } from "./address.js";

on("change", "[data-ref-picker]", (event, select) => {
  const { repo, path, target } = select.dataset;
  location.href = target === "history" ? historyURL(repo, select.value, path) : filesURL(repo, select.value, path);
});
