// A picker: a dialog with a search box over a list of choices — the
// command palette and the file finder are both one. The search box is a
// combobox that owns a listbox: typing filters, the arrow keys move the
// active choice, Enter takes it, Escape closes. Choices are links, so a
// pointer can also open one in a new tab.
import { el, icon } from "./dom.js";
import { match, highlight } from "./fuzzy.js";

const shown = 60;
let count = 0;

export class Picker {
  // label names the picker for assistive technology; placeholder is the
  // search box's hint.
  constructor({ label, placeholder }) {
    const id = `picker-${++count}`;
    this.input = el("input", {
      class: "picker-input", type: "text", role: "combobox", autocomplete: "off", spellcheck: "false",
      "aria-label": label, "aria-expanded": "true", "aria-controls": `${id}-list`, "aria-autocomplete": "list", placeholder,
    });
    this.list = el("div", { class: "picker-list", role: "listbox", id: `${id}-list`, "aria-label": label });
    this.status = el("p", { class: "picker-message", role: "status" });
    this.dialog = el("dialog", { class: "dialog picker", "aria-label": label },
      el("div", { class: "picker-search" }, icon("search"), this.input),
      this.list,
      this.status,
      el("footer", { class: "picker-footer", "aria-hidden": "true" },
        el("span", {}, el("kbd", { class: "kbd" }, "↑"), el("kbd", { class: "kbd" }, "↓"), " to move"),
        el("span", {}, el("kbd", { class: "kbd" }, "Enter"), " to open"),
        el("span", {}, el("kbd", { class: "kbd" }, "Esc"), " to close")));
    document.body.append(this.dialog);
    this.id = id;
    this.items = [];
    this.options = [];
    this.active = 0;
    this.input.addEventListener("input", () => this.render());
    this.input.addEventListener("keydown", (event) => this.onKeydown(event));
    this.dialog.addEventListener("click", (event) => { if (event.target === this.dialog) this.close(); });
  }

  // open shows the picker over the page with an empty search.
  open() {
    this.input.value = "";
    if (!this.dialog.open) this.dialog.showModal();
    this.input.focus();
    this.render();
  }

  close() {
    this.dialog.close();
  }

  // setItems gives the picker its choices: each has a label, and either
  // an href or a run function, and may have a group, an icon and a hint.
  setItems(items) {
    this.items = items;
    this.message = "";
    this.render();
  }

  // setMessage shows text instead of the choices: loading, or a problem.
  setMessage(text) {
    this.items = [];
    this.message = text;
    this.render();
  }

  render() {
    const query = this.input.value.trim();
    let found = [];
    for (const item of this.items) {
      const m = match(query, item.label);
      if (m) found.push({ item, ...m });
    }
    if (query) found.sort((a, b) => a.score - b.score);
    found = found.slice(0, shown);

    this.list.replaceChildren();
    this.options = [];
    let group = null;
    found.forEach(({ item, at }, i) => {
      // Groups head the unfiltered list; once filtered, it is in order of
      // how well each choice matches, and a choice's group is its hint.
      if (!query && item.group && item.group !== group) {
        group = item.group;
        this.list.append(el("div", { class: "picker-group", role: "presentation" }, group));
      }
      const option = el(item.href ? "a" : "div", {
        class: "picker-option", role: "option", id: `${this.id}-${i}`, "aria-selected": "false", href: item.href || false, tabindex: "-1",
      },
      icon(item.icon || "chevron-right"),
      el("span", { class: "picker-option-label" }, ...highlight(item.label, at)),
      ...(item.hint || (query && item.group) ? [el("span", { class: "picker-option-hint" }, item.hint || item.group)] : []));
      option.addEventListener("pointermove", () => this.select(i));
      option.addEventListener("click", (event) => {
        if (item.href && (event.metaKey || event.ctrlKey || event.shiftKey)) return;
        event.preventDefault();
        this.take(item);
      });
      this.list.append(option);
      this.options.push({ option, item });
    });

    if (this.message) this.status.textContent = this.message;
    else if (!found.length) this.status.textContent = this.items.length ? "Nothing matches." : "Nothing to show.";
    else this.status.textContent = "";
    this.status.hidden = !this.status.textContent;
    this.select(0);
  }

  select(i) {
    this.active = i;
    this.input.removeAttribute("aria-activedescendant");
    this.options.forEach(({ option }, j) => {
      const on = i === j;
      option.setAttribute("aria-selected", String(on));
      if (on) {
        this.input.setAttribute("aria-activedescendant", option.id);
        option.scrollIntoView({ block: "nearest" });
      }
    });
  }

  take(item) {
    if (item.href) {
      location.href = item.href;
      return;
    }
    this.close();
    item.run();
  }

  onKeydown(event) {
    const n = this.options.length;
    switch (event.key) {
      case "ArrowDown": event.preventDefault(); if (n) this.select((this.active + 1) % n); break;
      case "ArrowUp": event.preventDefault(); if (n) this.select((this.active - 1 + n) % n); break;
      case "Home": if (event.ctrlKey) { event.preventDefault(); this.select(0); } break;
      case "End": if (event.ctrlKey) { event.preventDefault(); this.select(n - 1); } break;
      case "Enter": {
        event.preventDefault();
        const chosen = this.options[this.active];
        if (chosen) this.take(chosen.item);
        break;
      }
    }
  }
}
