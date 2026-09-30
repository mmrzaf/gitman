// A feed scrolls inside its panel and leaves the page itself still: a panel
// marked data-fit is given the height that is left of the window below where
// it starts, however tall what is above it is, and again whenever that
// changes. Without scripting, or on a narrow screen where the page is one
// column and scrolls as a whole, its stylesheet height applies instead.
const bottomGap = 24;
const minimum = 240;
const fitted = () => document.querySelectorAll("[data-fit]");
const narrow = window.matchMedia("(max-width: 900px)");

function fit() {
  for (const el of fitted()) {
    if (narrow.matches) {
      el.style.removeProperty("max-height");
      continue;
    }
    const top = el.getBoundingClientRect().top + window.scrollY;
    el.style.maxHeight = `${Math.max(minimum, Math.floor(window.innerHeight - top - bottomGap - 2))}px`;
  }
}

// What sits above a fitted panel can change size — a panel refreshed live,
// a font arriving — so every panel beside it is watched.
const watcher = new ResizeObserver(fit);
function watch() {
  for (const el of fitted()) {
    for (const sibling of el.closest("aside, .split-side, main")?.children || []) watcher.observe(sibling);
  }
  fit();
}

watch();
window.addEventListener("resize", fit);
narrow.addEventListener("change", fit);
document.addEventListener("gitman:refreshed", watch);
