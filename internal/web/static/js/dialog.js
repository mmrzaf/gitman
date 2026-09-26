// Dialogs: forms that float over the page. Each is a <dialog data-dialog>
// that a link with data-dialog-open="<id>" opens. The link's own address,
// the page with ?dialog=<id>, is what opens it without scripting: the
// server renders it open, as part of the page. Here it opens as a modal
// instead, the address following along so that Back closes it; and a
// dialog the server rendered open — a form that failed and is shown again
// with its errors — becomes a modal as the page loads.
//
// A dialog whose address carries more than ?dialog= (the rule being
// edited, the secret being replaced) names those parameters in
// data-dialog-params, so closing it clears them too.
import { on } from "./dom.js";
import { push, replace, withParams } from "./address.js";

function paramsOf(dialog) {
  const cleared = { dialog: null };
  for (const name of (dialog.dataset.dialogParams || "").split(/\s+/).filter(Boolean)) cleared[name] = null;
  return cleared;
}

function showModal(dialog) {
  if (dialog.open) dialog.close();
  dialog.showModal();
  const target = dialog.querySelector("[aria-invalid=true], [autofocus]");
  target?.focus();
}

function openDialog(dialog) {
  showModal(dialog);
  push(withParams({ dialog: dialog.id }), { dialog: dialog.id });
}

on("click", "[data-dialog-open]", (event, opener) => {
  const dialog = document.getElementById(opener.dataset.dialogOpen);
  if (!dialog || event.metaKey || event.ctrlKey || event.shiftKey) return;
  event.preventDefault();
  openDialog(dialog);
});

on("click", "[data-dialog-close]", (event, button) => {
  button.closest("dialog")?.close();
});

// A click on the backdrop — outside the dialog's box — closes it.
on("click", "dialog[data-dialog]", (event, dialog) => {
  if (event.target !== dialog) return;
  const box = dialog.getBoundingClientRect();
  const inside = event.clientX >= box.left && event.clientX <= box.right && event.clientY >= box.top && event.clientY <= box.bottom;
  if (!inside) dialog.close();
});

// Whatever closed it, the address stops naming it: Back, if this page
// added the entry that opened it, or else by replacing the address.
document.addEventListener("close", (event) => {
  const dialog = event.target;
  if (!(dialog instanceof HTMLDialogElement) || !dialog.matches("[data-dialog]")) return;
  if (new URL(location.href).searchParams.get("dialog") !== dialog.id) return;
  if (history.state?.dialog === dialog.id) history.back();
  else replace(withParams(paramsOf(dialog)));
}, true);

window.addEventListener("popstate", () => {
  const wanted = new URL(location.href).searchParams.get("dialog");
  for (const dialog of document.querySelectorAll("dialog[data-dialog]")) {
    if (dialog.id === wanted && !dialog.open) showModal(dialog);
    if (dialog.id !== wanted && dialog.open) dialog.close();
  }
});

for (const dialog of document.querySelectorAll("dialog[data-dialog][open]")) showModal(dialog);
