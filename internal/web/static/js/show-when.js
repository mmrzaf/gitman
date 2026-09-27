// Parts of a form that only matter for one choice: an element with
// data-show-when="<field>=<value>" shows only while the form's <field> is
// <value> — the people to name, only for a "Named people" policy. Without
// scripting every part is shown.
function sync(form) {
  for (const part of form.querySelectorAll("[data-show-when]")) {
    const [name, value] = part.dataset.showWhen.split("=");
    const field = form.elements.namedItem(name);
    part.hidden = !field || field.value !== value;
  }
}

for (const form of document.querySelectorAll("form:has([data-show-when])")) sync(form);

document.addEventListener("change", (event) => {
  const form = event.target.form;
  if (form?.querySelector("[data-show-when]")) sync(form);
});
