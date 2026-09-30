// The ref pickers of Files, History and Compare go to the chosen ref as
// soon as it changes. Files and History keep the current path; Compare
// goes when both sides are chosen. Without scripting, their Switch and
// Compare buttons submit the same choice.
import { on } from "./dom.js";
import { filesURL, historyURL } from "./address.js";

on("change", "[data-ref-picker]", (event, picker) => {
  const compare = picker.closest("form[data-compare]");
  if (compare) {
    if (compare.elements.base.value.trim() && compare.elements.head.value.trim()) compare.requestSubmit();
    return;
  }
  const { repo, path, target } = picker.dataset;
  location.href = target === "history" ? historyURL(repo, picker.value, path) : filesURL(repo, picker.value, path);
});
