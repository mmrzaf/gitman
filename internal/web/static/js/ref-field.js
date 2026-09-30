// A ref field chooses a branch, a tag or a commit. Without scripting it is
// a text box that lists the repository's refs. Here it becomes a button
// that opens a searchable list, the picker the command palette uses: type
// to narrow it, or type a commit's hash to use that commit. Choosing
// sends the form, so what the page shows is always what is chosen.
import { el, icon, on } from "./dom.js";
import { Picker } from "./picker.js";

const hashLike = /^[0-9a-f]{7,40}$/i;

function refsOf(input) {
  const list = document.getElementById(input.getAttribute("list"));
  return [...(list?.options || [])].map((option) => ({ name: option.value, kind: option.textContent }));
}

function upgrade(field) {
  const input = field.querySelector("input");
  const label = field.querySelector("label");
  const refs = refsOf(input);
  const clear = field.dataset.refClear;
  const kindOf = (value) => refs.find((ref) => ref.name === value)?.kind || "commit";

  const name = el("span", { class: "ref-button-name", id: `${input.id}-value` });
  const glyph = el("span", { class: "ref-button-icon" });
  const button = el("button", {
    type: "button", class: "btn ref-button", id: `${input.id}-button`,
    "aria-haspopup": "dialog", "aria-labelledby": `${label.id ||= `${input.id}-label`} ${name.id}`,
  }, glyph, name, icon("chevron-down"));
  const show = () => {
    const value = input.value.trim();
    name.textContent = value || clear || "Choose…";
    name.classList.toggle("muted", !value);
    glyph.replaceChildren(value ? icon(kindOf(value)) : "");
  };
  show();
  input.type = "hidden";
  label.htmlFor = button.id;
  input.after(button);

  let picker = null;
  let items = [];
  let hashShown = false;
  const choose = (value) => {
    input.value = value;
    show();
    input.form.requestSubmit();
  };
  const build = () => {
    items = [];
    if (clear) items.push({ label: clear, icon: "close", run: () => choose("") });
    for (const [kind, group] of [["branch", "Branches"], ["tag", "Tags"]]) {
      for (const ref of refs.filter((r) => r.kind === kind)) {
        items.push({ label: ref.name, group, icon: kind, run: () => choose(ref.name) });
      }
    }
  };

  button.addEventListener("click", () => {
    if (!picker) {
      picker = new Picker({ label: label.textContent.trim(), placeholder: "Find a branch or tag, or type a commit…" });
      // A typed hash is offered as a choice of its own.
      picker.input.addEventListener("input", () => {
        const typed = picker.input.value.trim();
        const offer = hashLike.test(typed);
        if (offer === hashShown && !offer) return;
        hashShown = offer;
        picker.setItems(offer ? [{ label: `Commit ${typed}`, icon: "commit", run: () => choose(typed) }, ...items] : items);
      });
    }
    build();
    hashShown = false;
    picker.setItems(items);
    picker.open();
  });
}

for (const field of document.querySelectorAll("[data-ref-field]")) upgrade(field);
