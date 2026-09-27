// Relative times ("5 min ago", "in 3 d") stay current while a page is
// open, in exactly the words the server rendered them with; hovering one
// shows the exact time in the reader's own time zone.
const MINUTE = 60e3, HOUR = 60 * MINUTE, DAY = 24 * HOUR;
const exact = new Intl.DateTimeFormat(undefined, {
  year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZoneName: "short",
});

function relative(date) {
  const d = Date.now() - date.getTime();
  const span = Math.abs(d);
  let amount;
  if (span < MINUTE) return "just now";
  if (span < HOUR) amount = `${Math.floor(span / MINUTE)} min`;
  else if (span < DAY) amount = `${Math.floor(span / HOUR)} h`;
  else if (span < 30 * DAY) amount = `${Math.floor(span / DAY)} d`;
  else return date.toISOString().slice(0, 10);
  return d < 0 ? `in ${amount}` : `${amount} ago`;
}

function refresh() {
  for (const node of document.querySelectorAll("time[data-relative]")) {
    const date = new Date(node.getAttribute("datetime"));
    if (Number.isNaN(date.getTime())) continue;
    node.textContent = relative(date);
    node.title = exact.format(date);
  }
}

refresh();
setInterval(refresh, 30e3);
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
document.addEventListener("gitman:refreshed", refresh);
