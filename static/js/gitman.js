(() => {
  const terminalStatuses = new Set(["success", "failed", "skipped", "cancelled"]);

  function installConfirmForms() {
    document.addEventListener("submit", (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) return;

      const message = form.getAttribute("data-confirm");
      if (message && !window.confirm(message)) {
        event.preventDefault();
      }
    });
  }

  function updateStatus(status) {
    document.querySelectorAll("[data-ci-status]").forEach((el) => {
      el.textContent = status.charAt(0).toUpperCase() + status.slice(1);
      el.className = `badge badge-${status}`;
    });
  }

  function refreshLog(el) {
    const url = el.dataset.logUrl;
    if (!url || el.dataset.finished === "true") return;
    const follow = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const previousScroll = el.scrollTop;

    fetch(url, { credentials: "same-origin", cache: "no-store" })
      .then((res) => {
        if (!res.ok) throw new Error(`log refresh failed: ${res.status}`);
        const status = res.headers.get("X-Gitman-CI-Status") || "";
        return res.text().then((text) => ({ text, status }));
      })
      .then(({ text, status }) => {
        el.textContent = text;
        el.scrollTop = follow ? el.scrollHeight : previousScroll;
        if (status) updateStatus(status);
        if (terminalStatuses.has(status)) {
          el.dataset.finished = "true";
          window.setTimeout(() => window.location.reload(), 350);
        }
      })
      .catch(() => {
        // Keep the visible log stable. The next poll may succeed.
      });
  }

  function startLogRefresh() {
    document.querySelectorAll("[data-log-url][data-refresh-ms]").forEach((el) => {
      const delay = Number(el.dataset.refreshMs || "2000");
      if (!Number.isFinite(delay) || delay <= 0) return;
      window.setInterval(() => refreshLog(el), delay);
    });
  }

  function start() {
    installConfirmForms();
    startLogRefresh();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
