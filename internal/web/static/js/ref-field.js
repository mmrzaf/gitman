// A ref field chooses a branch, a tag or a commit. Without scripting it is
// a text box that lists the repository's refs, or a plain select. Here it
// becomes a button that opens a searchable list, the picker the command
// palette uses: type to narrow it, or, where a commit can be chosen, type
// its hash. A field with data-ref-submit sends its form once chosen, so what
// a page shows is always what is chosen.
import { el, icon } from "./dom.js";
import { Picker } from "./picker.js";

const hashLike = /^[0-9a-f]{7,40}$/i;

// choicesOf is what a field offers: from the options of a select, grouped
// as its optgroups are, or from the datalist beside a text box.
function choicesOf(control) {
  if (control.tagName === "SELECT") {
    return [...control.options].map((option) => ({
      value: option.value, label: option.textContent.trim(),
      kind: option.parentElement.label === "Tags" ? "tag" : "branch",
    }));
  }
  const list = document.getElementById(control.getAttribute("list"));
  return [...(list?.options || [])].map((option) => ({ value: option.value, label: option.value, kind: option.textContent }));
}

function upgrade(field) {
  const control = field.querySelector("input, select");
  const label = field.querySelector("label");
  const isSelect = control.tagName === "SELECT";
  const choices = choicesOf(control);
  const clear = field.dataset.refClear;
  const find = (value) => choices.find((choice) => choice.value === value);

  const name = el("span", { class: "ref-button-name", id: `${control.id}-value` });
  const glyph = el("span", { class: "ref-button-icon" });
  const button = el("button", {
    type: "button", class: "btn ref-button", id: `${control.id}-button`,
    "aria-haspopup": "dialog", "aria-labelledby": `${label.id ||= `${control.id}-label`} ${name.id}`,
    "aria-describedby": control.getAttribute("aria-describedby"), autofocus: control.hasAttribute("autofocus"),
  }, glyph, name, icon("chevron-down"));
  const show = () => {
    const value = control.value.trim();
    const chosen = find(value);
    name.textContent = chosen?.label || value || clear || "Choose…";
    name.classList.toggle("muted", !value);
    glyph.replaceChildren(value ? icon(chosen?.kind || "commit") : "");
  };
  show();
  label.htmlFor = button.id;
  if (isSelect) {
    control.hidden = true;
    (control.closest(".select-wrap") || control).after(button);
    control.closest(".select-wrap")?.setAttribute("hidden", "");
  } else {
    control.type = "hidden";
    control.after(button);
  }

  let picker = null;
  let items = [];
  let hashShown = false;
  const choose = (value) => {
    control.value = value;
    show();
    if (field.hasAttribute("data-ref-submit")) control.form.requestSubmit();
  };
  const build = () => {
    items = [];
    if (clear) items.push({ label: clear, icon: "close", run: () => choose("") });
    for (const [kind, group] of [["branch", "Branches"], ["tag", "Tags"]]) {
      for (const choice of choices.filter((c) => c.kind === kind)) {
        items.push({ label: choice.label, group, icon: kind, run: () => choose(choice.value) });
      }
    }
  };

  button.addEventListener("click", () => {
    if (!picker) {
      picker = new Picker({
        label: label.textContent.trim(),
        placeholder: isSelect ? "Find a branch or tag…" : "Find a branch or tag, or type a commit…",
      });
      if (!isSelect) {
        // A typed hash is offered as a choice of its own.
        picker.input.addEventListener("input", () => {
          const typed = picker.input.value.trim();
          const offer = hashLike.test(typed);
          if (offer === hashShown && !offer) return;
          hashShown = offer;
          picker.setItems(offer ? [{ label: `Commit ${typed}`, icon: "commit", run: () => choose(typed) }, ...items] : items);
        });
      }
    }
    build();
    hashShown = false;
    picker.setItems(items);
    picker.open();
  });
}

for (const field of document.querySelectorAll("[data-ref-field]")) upgrade(field);
