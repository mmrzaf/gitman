// The Files page's branch and tag picker goes to the chosen ref as soon
// as it changes, keeping the current path. Without scripting, its Switch
// button submits the same choice and the server redirects.
import { on } from "./dom.js";
import { filesURL } from "./address.js";

on("change", "[data-ref-picker]", (event, select) => {
  location.href = filesURL(select.dataset.repo, select.value, select.dataset.path);
});
