// The theme switch in the account menu: the system's theme, or light or
// dark whatever the system says. The choice is kept in this browser only;
// head.js applies it before the page paints.
import { on } from "./dom.js";

const KEY = "gitman-theme";

function chosen() {
  try {
    const value = localStorage.getItem(KEY);
    return value === "light" || value === "dark" ? value : "system";
  } catch {
    return "system";
  }
}

function show(choice) {
  const root = document.documentElement;
  if (choice === "system") delete root.dataset.theme;
  else root.dataset.theme = choice;
  for (const button of document.querySelectorAll("[data-theme-choice]")) {
    button.setAttribute("aria-checked", String(button.dataset.themeChoice === choice));
  }
}

show(chosen());

on("click", "[data-theme-choice]", (event, button) => {
  const choice = button.dataset.themeChoice;
  try {
    if (choice === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, choice);
  } catch { /* the choice lasts until the page is left */ }
  show(choice);
});
