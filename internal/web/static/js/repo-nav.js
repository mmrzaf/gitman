// Keep the active repository tab visible when the tab bar overflows on
// narrow screens. Only the bar scrolls; the page keeps its own position.
const bar = document.querySelector("[data-repo-nav]");

function showActive() {
  if (!bar) return;
  const current = bar.querySelector('[aria-current="page"]');
  if (!current) return;
  const left = current.offsetLeft - bar.offsetLeft;
  const right = left + current.offsetWidth;
  const start = bar.scrollLeft;
  const end = start + bar.clientWidth;
  if (left < start) bar.scrollLeft = left;
  else if (right > end) bar.scrollLeft = right - bar.clientWidth;
}

showActive();
window.addEventListener("pageshow", (event) => { if (event.persisted) showActive(); });
