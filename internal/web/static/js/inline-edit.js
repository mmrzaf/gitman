// Inline editing: a value shown as text with an Edit button that turns it
// into its form, in place ([data-inline-edit], holding [data-inline-view]
// and [data-inline-form]). Cancel or Escape turns it back. Without
// scripting the form is simply shown; after a failed save the server
// marks it data-inline-editing, so it opens as the form.
import { on } from "./dom.js";

function start(box) {
  box.setAttribute("data-inline-editing", "");
  box.querySelector("[data-inline-form] input, [data-inline-form] textarea")?.focus();
}

function stop(box) {
  box.removeAttribute("data-inline-editing");
  box.querySelector("[data-inline-form]").reset();
  box.querySelector("[data-inline-start]").focus();
}

on("click", "[data-inline-start]", (event, button) => start(button.closest("[data-inline-edit]")));
on("click", "[data-inline-cancel]", (event, button) => stop(button.closest("[data-inline-edit]")));
on("keydown", "[data-inline-form]", (event, form) => {
  if (event.key === "Escape") stop(form.closest("[data-inline-edit]"));
});
