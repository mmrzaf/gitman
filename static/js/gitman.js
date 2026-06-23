(() => {
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

  function refreshLog(el) {
    const url = el.dataset.logUrl;
    if (!url) return;
    const follow = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const previousScroll = el.scrollTop;

    fetch(url, { credentials: "same-origin", cache: "no-store" })
      .then((res) => {
        if (!res.ok) throw new Error(`log refresh failed: ${res.status}`);
        return res.text();
      })
      .then((text) => {
        el.textContent = text;
        el.scrollTop = follow ? el.scrollHeight : previousScroll;
      })
      .catch(() => {
        // Keep the visible log stable. The next poll may succeed.
      });
  }

  function startLogRefresh() {
    document.querySelectorAll("[data-log-url][data-refresh-ms]").forEach((el) => {
      const delay = Number(el.dataset.refreshMs || "3000");
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
