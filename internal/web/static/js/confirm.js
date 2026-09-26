// Confirmation before an action that is hard to take back. A form with
// data-confirm="<question>" asks first, in a dialog that says what will
// happen (data-confirm-detail) and names the action on its button
// (data-confirm-action). Focus starts on the safe choice. The dialog is
// danger-toned unless data-confirm-tone="primary". Without scripting the
// form submits straight away: nothing depends on this to work.
import { el, on } from "./dom.js";

let dialog = null;

function build() {
  const title = el("h2", { class: "dialog-title", id: "confirm-title" });
  const detail = el("p", { id: "confirm-detail" });
  const cancel = el("button", { type: "submit", class: "btn", value: "cancel" }, "Cancel");
  const confirm = el("button", { type: "submit", class: "btn", value: "confirm" });
  dialog = el("dialog", { class: "dialog", role: "alertdialog", "aria-labelledby": "confirm-title", "aria-describedby": "confirm-detail" },
    el("form", { method: "dialog" },
      el("header", { class: "dialog-header" }, title),
      el("div", { class: "dialog-body" }, detail),
      el("footer", { class: "dialog-footer" }, cancel, confirm)));
  document.body.append(dialog);
  return { title, detail, cancel, confirm };
}

let parts = null;

on("submit", "form[data-confirm]", (event, form) => {
  if (form.dataset.confirmed) {
    delete form.dataset.confirmed;
    return;
  }
  event.preventDefault();
  parts ||= build();
  const submitter = event.submitter;
  const menu = form.closest("[data-menu]");
  parts.title.textContent = form.dataset.confirm;
  parts.detail.textContent = form.dataset.confirmDetail || "";
  parts.detail.hidden = !form.dataset.confirmDetail;
  parts.confirm.textContent = form.dataset.confirmAction || "Confirm";
  parts.confirm.className = form.dataset.confirmTone === "primary" ? "btn btn-primary" : "btn btn-danger-solid";
  dialog.returnValue = "";
  if (menu) menu.open = false;
  dialog.showModal();
  parts.cancel.focus();
  dialog.addEventListener("close", () => {
    // Focus goes back where the action started: its menu's button when it
    // came from a menu, which is closed by now.
    if (menu) {
      menu.open = false;
      menu.querySelector("summary").focus();
    } else {
      submitter?.focus();
    }
    if (dialog.returnValue !== "confirm") return;
    form.dataset.confirmed = "yes";
    form.requestSubmit(submitter && form.contains(submitter) ? submitter : undefined);
  }, { once: true });
});
