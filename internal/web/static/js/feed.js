// Use the entry's rendered text for its native tooltip so summaries and
// tooltips stay in sync, including after live updates.
function refresh() {
  for (const entry of document.querySelectorAll(".feed-text")) {
    entry.title = entry.textContent.replace(/\s+/g, " ").trim();
  }
}

refresh();
document.addEventListener("gitman:refreshed", refresh);
