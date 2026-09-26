// While a form submits, its submit buttons are disabled and show that
// they are busy, so a double click cannot send the same action twice.
document.addEventListener("submit", (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || form.method === "dialog") return;
  // Decided once every submit handler has run — one may cancel the
  // submission, as asking for confirmation does — and once the browser
  // has read the form, so the clicked button's name and value are sent.
  setTimeout(() => {
    if (event.defaultPrevented) return;
    for (const button of form.querySelectorAll('button[type="submit"], button:not([type])')) {
      button.setAttribute("aria-busy", "true");
      button.disabled = true;
    }
  }, 0);
});

// A page restored by Back or Forward comes back exactly as it was left,
// including buttons disabled mid-submit; re-enable them.
window.addEventListener("pageshow", (event) => {
  if (!event.persisted) return;
  for (const button of document.querySelectorAll('button[aria-busy="true"]')) {
    button.removeAttribute("aria-busy");
    button.disabled = false;
  }
});
