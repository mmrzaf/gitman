(() => {
  function closestTarget(selector) {
    return selector ? document.querySelector(selector) : null;
  }

  function installHxForms() {
    document.addEventListener("submit", (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) return;
      const hxPost = form.getAttribute("hx-post");
      if (!hxPost) return;

      const message = form.getAttribute("hx-confirm");
      if (message && !window.confirm(message)) {
        event.preventDefault();
        return;
      }

      event.preventDefault();
      const target = closestTarget(form.getAttribute("hx-target"));
      const swap = form.getAttribute("hx-swap") || "innerHTML";
      const button = form.querySelector("button[type='submit']");
      if (button) button.disabled = true;

      fetch(hxPost, {
        method: "POST",
        body: new FormData(form),
        credentials: "same-origin",
        headers: { "HX-Request": "true" },
      })
        .then((res) => res.text().then((text) => ({ ok: res.ok, text })))
        .then(({ ok, text }) => {
          if (!target) {
            window.location.reload();
            return;
          }
          if (swap === "outerHTML") {
            target.outerHTML = text;
          } else {
            target.innerHTML = text;
          }
          if (!ok) console.warn("Gitman form request failed");
        })
        .catch(() => {
          window.location.reload();
        })
        .finally(() => {
          if (button) button.disabled = false;
        });
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
    installHxForms();
    startLogRefresh();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
