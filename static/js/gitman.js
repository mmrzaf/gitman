(() => {
  const terminalStatuses = new Set(["success", "failed", "skipped", "cancelled"]);
  const knownStatuses = ["pending", "running", "success", "failed", "skipped", "cancelled"];
  const statusLabels = {
    pending: "Pending",
    running: "Running",
    success: "Success",
    failed: "Failed",
    skipped: "Skipped",
    cancelled: "Cancelled",
  };
  const statusIcons = {
    pending: "○",
    running: "●",
    success: "✓",
    failed: "×",
    skipped: "○",
    cancelled: "−",
  };

  function installConfirmForms() {
    document.addEventListener("submit", (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) return;
      const message = form.getAttribute("data-confirm");
      if (message && !window.confirm(message)) event.preventDefault();
    });
  }

  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return;
    }
    const input = document.createElement("textarea");
    input.value = text;
    input.setAttribute("readonly", "");
    input.style.position = "fixed";
    input.style.opacity = "0";
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    input.remove();
  }

  function installCopyActions() {
    document.addEventListener("click", async (event) => {
      const button = event.target.closest("[data-copy-text], [data-copy-url]");
      if (!(button instanceof HTMLElement)) return;
      const rawText = button.dataset.copyText || button.dataset.copyUrl || "";
      if (!rawText) return;
      let text = rawText;
      if (button.dataset.copyUrl) {
        const copyURL = new URL(rawText, window.location.href);
        if (button.hasAttribute("data-copy-current-hash")) copyURL.hash = window.location.hash;
        text = copyURL.href;
      }
      const normal = button.dataset.copyLabel || button.textContent || "Copy";
      try {
        await copyText(text);
        button.textContent = "Copied";
        window.setTimeout(() => { button.textContent = normal; }, 1200);
      } catch (_) {
        button.textContent = "Copy failed";
        window.setTimeout(() => { button.textContent = normal; }, 1600);
      }
    });
  }

  const relativeFormatter = typeof Intl !== "undefined" && Intl.RelativeTimeFormat
    ? new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
    : null;

  function relativeTime(date, now) {
    if (!relativeFormatter) return date.toLocaleString();
    const seconds = Math.round((date.getTime() - now.getTime()) / 1000);
    const abs = Math.abs(seconds);
    if (abs < 45) return relativeFormatter.format(seconds, "second");
    if (abs < 45 * 60) return relativeFormatter.format(Math.round(seconds / 60), "minute");
    if (abs < 22 * 3600) return relativeFormatter.format(Math.round(seconds / 3600), "hour");
    if (abs < 26 * 86400) return relativeFormatter.format(Math.round(seconds / 86400), "day");
    if (abs < 11 * 30 * 86400) return relativeFormatter.format(Math.round(seconds / (30 * 86400)), "month");
    return relativeFormatter.format(Math.round(seconds / (365 * 86400)), "year");
  }

  function refreshRelativeTimes() {
    const now = new Date();
    document.querySelectorAll("time[data-relative-time][datetime]").forEach((el) => {
      const date = new Date(el.getAttribute("datetime"));
      if (Number.isNaN(date.getTime())) return;
      el.textContent = relativeTime(date, now);
      el.title = date.toLocaleString();
    });
  }

  function installRelativeTimes() {
    refreshRelativeTimes();
    window.setInterval(refreshRelativeTimes, 60_000);
  }

  function updateRunStatus(status) {
    if (!status) return;
    document.querySelectorAll("[data-ci-status]").forEach((el) => {
      el.textContent = statusLabels[status] || status;
      knownStatuses.forEach((known) => el.classList.remove(`badge-${known}`));
      el.classList.add(`badge-${status}`);
    });
  }

  function parseLogLine(line) {
    const match = line.replace(/\r$/, "").match(/^\[?(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)\]?\s+(.*)$/);
    if (!match) return { timestamp: "", message: line.replace(/\r$/, "") };
    return { timestamp: match[1], message: match[2] };
  }

  function escapeRegExp(value) {
    return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function stepStatus(node) {
    return node?.dataset.stepStatus || "pending";
  }

  function setStepStatus(node, status, options = {}) {
    if (!node) return;
    knownStatuses.forEach((known) => node.classList.remove(`ci-step-${known}`));
    node.classList.add(`ci-step-${status}`);
    node.dataset.stepStatus = status;
    const icon = node.querySelector(".ci-step-status-icon");
    if (icon) icon.textContent = statusIcons[status] || "○";
    const label = node.querySelector("[data-step-status-label]");
    if (label) label.textContent = statusLabels[status] || status;
    if (options.timestamp && status === "running") node.dataset.stepStart = options.timestamp;
    if (options.timestamp && status !== "running" && node.dataset.stepStart) {
      const start = new Date(node.dataset.stepStart);
      const end = new Date(options.timestamp);
      if (!Number.isNaN(start.getTime()) && !Number.isNaN(end.getTime())) {
        const seconds = Math.max(0, (end.getTime() - start.getTime()) / 1000);
        const duration = node.querySelector("[data-step-duration]");
        if (duration) duration.textContent = seconds < 60 ? `${seconds.toFixed(seconds % 1 ? 1 : 0)}s` : `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
      }
    }
    if (options.exitCode) {
      const exit = node.querySelector("[data-step-exit]");
      if (exit) exit.textContent = `exit ${options.exitCode}`;
    }
    if (["running", "failed", "cancelled"].includes(status)) node.open = true;
    if (status === "success" && !node.classList.contains("ci-step-search-hit")) node.open = false;
  }

  function clearEmptyOutput(pre) {
    if (!pre) return;
    if (pre.classList.contains("is-empty")) {
      pre.textContent = "";
      pre.classList.remove("is-empty");
    }
  }

  function appendStepLine(node, line, follow) {
    if (!node) return;
    const pre = node.querySelector("[data-step-output]");
    if (!pre) return;
    clearEmptyOutput(pre);
    if (pre.textContent) pre.textContent += "\n";
    pre.textContent += line;
    if (follow && stepStatus(node) === "running") pre.scrollTop = pre.scrollHeight;
  }

  function setupLogViewer(root) {
    const rawPre = root.querySelector("[data-raw-log]");
    const searchInput = root.querySelector("[data-log-search]");
    const searchCount = root.querySelector("[data-log-search-count]");
    const newOutput = root.querySelector("[data-log-new-output]");
    const followButton = root.querySelector("[data-log-follow]");
    const wrapButton = root.querySelector("[data-log-wrap]");
    const stepNodes = Array.from(root.querySelectorAll('[data-ci-step][data-step-kind="step"]'));
    const setupNode = root.querySelector('[data-ci-step][data-step-kind="setup"]');
    const finalizeNode = root.querySelector('[data-ci-step][data-step-kind="finalize"]');
    let offset = Number(root.dataset.logOffset || "0");
    let status = root.dataset.runStatus || "";
    let follow = true;
    let wrap = true;
    let currentView = "steps";
    let lineCarry = "";
    let newLines = 0;
    let pollTimer = 0;
    let searchIndex = 0;
    let hasRealLog = offset > 0;
    let rawText = hasRealLog && rawPre ? rawPre.textContent : "";
    let pipelineStopped = false;

    function activeStep() {
      return stepNodes.find((node) => stepStatus(node) === "running") || null;
    }

    function nextPendingStep() {
      return stepNodes.find((node) => stepStatus(node) === "pending") || null;
    }

    function allStepsSettled() {
      return stepNodes.length > 0 && stepNodes.every((node) => stepStatus(node) !== "pending" && stepStatus(node) !== "running");
    }

    function setFollow(next) {
      follow = next;
      if (followButton) {
        followButton.classList.toggle("active", follow);
        followButton.setAttribute("aria-pressed", String(follow));
      }
      if (follow) {
        newLines = 0;
        if (newOutput) newOutput.hidden = true;
        if (currentView === "raw" && rawPre) rawPre.scrollTop = rawPre.scrollHeight;
        const active = activeStep();
        const activeOutput = active?.querySelector("[data-step-output]");
        if (activeOutput) activeOutput.scrollTop = activeOutput.scrollHeight;
      }
    }

    function showNewOutput() {
      if (!newOutput || follow || newLines <= 0) return;
      newOutput.textContent = `↓ ${newLines} new ${newLines === 1 ? "line" : "lines"}`;
      newOutput.hidden = false;
    }

    function setView(view) {
      currentView = view;
      root.querySelectorAll("[data-log-view]").forEach((panel) => {
        panel.hidden = panel.dataset.logView !== view;
      });
      root.querySelectorAll("[data-log-view-button]").forEach((button) => {
        const active = button.dataset.logViewButton === view;
        button.classList.toggle("active", active);
        button.setAttribute("aria-pressed", String(active));
      });
      if (view === "raw" && follow && rawPre) rawPre.scrollTop = rawPre.scrollHeight;
      refreshSearch();
    }

    function applyWrap() {
      root.querySelectorAll(".log-box, .ci-step-output").forEach((pre) => pre.classList.toggle("log-nowrap", !wrap));
      if (wrapButton) {
        wrapButton.classList.toggle("active", wrap);
        wrapButton.setAttribute("aria-pressed", String(wrap));
      }
    }

    function appendRaw(text) {
      if (!text || !rawPre) return;
      if (!hasRealLog) {
        rawText = "";
        rawPre.textContent = "";
        hasRealLog = true;
      }
      rawText += text;
      if (searchInput?.value.trim()) {
        renderRawSearch();
      } else {
        rawPre.textContent = rawText;
      }
      if (follow && currentView === "raw") rawPre.scrollTop = rawPre.scrollHeight;
    }

    function appendStructuredLine(line) {
      const parsed = parseLogLine(line);
      let active = activeStep();
      if (active) {
        const name = active.dataset.stepName || "";
        if (parsed.message === `--- Step: ${name}: SUCCESS ---`) {
          setStepStatus(active, "success", { timestamp: parsed.timestamp });
          return;
        }
        const failed = parsed.message.match(new RegExp(`^--- Step: ${escapeRegExp(name)}: FAILED \\(exit ([0-9]+)\\) ---$`));
        if (failed) {
          setStepStatus(active, "failed", { timestamp: parsed.timestamp, exitCode: failed[1] });
          pipelineStopped = true;
          return;
        }
      }

      active = activeStep();
      if (!active) {
        const next = nextPendingStep();
        if (next) {
          const name = next.dataset.stepName || "";
          if (parsed.message === `--- Step: ${name} ---`) {
            setStepStatus(setupNode, "success");
            setStepStatus(next, "running", { timestamp: parsed.timestamp });
            next.open = true;
            if (follow && currentView === "steps") next.scrollIntoView({ block: "nearest" });
            return;
          }
        }
      }

      active = activeStep();
      if (active) {
        appendStepLine(active, line, follow);
      } else if (pipelineStopped || allStepsSettled()) {
        setStepStatus(finalizeNode, "running");
        appendStepLine(finalizeNode, line, follow);
      } else {
        appendStepLine(setupNode, line, follow);
      }
    }

    function consumeStructured(text) {
      if (!text) return;
      const combined = lineCarry + text;
      const parts = combined.split("\n");
      lineCarry = parts.pop() || "";
      parts.forEach(appendStructuredLine);
    }

    function countMatches(text, query) {
      if (!query) return 0;
      let count = 0;
      let index = 0;
      const haystack = text.toLocaleLowerCase();
      const needle = query.toLocaleLowerCase();
      while ((index = haystack.indexOf(needle, index)) !== -1) {
        count++;
        index += Math.max(needle.length, 1);
      }
      return count;
    }

    function renderRawSearch() {
      if (!rawPre) return;
      const query = searchInput?.value.trim() || "";
      if (!query) {
        rawPre.textContent = hasRealLog ? rawText : "Waiting for the worker…";
        return;
      }
      const lower = rawText.toLocaleLowerCase();
      const needle = query.toLocaleLowerCase();
      const fragment = document.createDocumentFragment();
      let cursor = 0;
      let found = 0;
      while (found < 300) {
        const index = lower.indexOf(needle, cursor);
        if (index === -1) break;
        fragment.appendChild(document.createTextNode(rawText.slice(cursor, index)));
        const mark = document.createElement("mark");
        mark.dataset.logMatch = String(found);
        mark.textContent = rawText.slice(index, index + query.length);
        fragment.appendChild(mark);
        cursor = index + query.length;
        found++;
      }
      fragment.appendChild(document.createTextNode(rawText.slice(cursor)));
      rawPre.replaceChildren(fragment);
    }

    function refreshSearch() {
      const query = searchInput?.value.trim() || "";
      const count = countMatches(rawText, query);
      if (searchCount) searchCount.textContent = query ? `${count} match${count === 1 ? "" : "es"}` : "";
      root.querySelectorAll("[data-ci-step]").forEach((node) => {
        const output = node.querySelector("[data-step-output]")?.textContent || "";
        const hit = query && output.toLocaleLowerCase().includes(query.toLocaleLowerCase());
        node.classList.toggle("ci-step-search-hit", Boolean(hit));
        if (hit) node.open = true;
      });
      searchIndex = Math.min(searchIndex, Math.max(0, count - 1));
      renderRawSearch();
    }

    function navigateSearch(direction) {
      const query = searchInput?.value.trim() || "";
      const count = countMatches(rawText, query);
      if (!query || count === 0) return;
      searchIndex = (searchIndex + direction + count) % count;
      setView("raw");
      renderRawSearch();
      const marks = rawPre?.querySelectorAll("mark[data-log-match]") || [];
      const cappedIndex = Math.min(searchIndex, marks.length - 1);
      if (cappedIndex >= 0 && marks[cappedIndex]) {
        setFollow(false);
        marks[cappedIndex].scrollIntoView({ block: "center" });
        marks[cappedIndex].classList.add("current");
        marks.forEach((mark, index) => { if (index !== cappedIndex) mark.classList.remove("current"); });
        if (searchCount) searchCount.textContent = `${searchIndex + 1} / ${count}`;
      }
    }

    function schedulePoll(delay) {
      window.clearTimeout(pollTimer);
      if (!terminalStatuses.has(status)) pollTimer = window.setTimeout(poll, delay);
    }

    async function poll() {
      const url = root.dataset.logUrl;
      if (!url || terminalStatuses.has(status)) return;
      try {
        const endpoint = new URL(url, window.location.href);
        endpoint.searchParams.set("offset", String(offset));
        const response = await fetch(endpoint, { credentials: "same-origin", cache: "no-store" });
        if (!response.ok) throw new Error(`log refresh failed: ${response.status}`);
        if (response.headers.get("X-Gitman-Log-Reset") === "true") {
          window.location.reload();
          return;
        }
        const nextOffset = Number(response.headers.get("X-Gitman-Log-Offset"));
        const nextStatus = response.headers.get("X-Gitman-CI-Status") || status;
        const text = await response.text();
        if (Number.isFinite(nextOffset) && nextOffset >= 0) offset = nextOffset;
        if (text) {
          appendRaw(text);
          consumeStructured(text);
          if (!follow) {
            newLines += Math.max(1, (text.match(/\n/g) || []).length);
            showNewOutput();
          }
          if (searchInput?.value.trim()) refreshSearch();
        }
        status = nextStatus;
        root.dataset.runStatus = status;
        updateRunStatus(status);
        if (terminalStatuses.has(status)) {
          setStepStatus(finalizeNode, status);
          const note = root.querySelector("[data-log-live-note]");
          if (note) note.textContent = "Run finished. Finalizing this view…";
          window.setTimeout(() => window.location.reload(), 450);
          return;
        }
        schedulePoll(Number(root.dataset.refreshMs || "1500"));
      } catch (_) {
        schedulePoll(2500);
      }
    }

    root.querySelectorAll("[data-log-view-button]").forEach((button) => {
      button.addEventListener("click", () => setView(button.dataset.logViewButton || "steps"));
    });
    followButton?.addEventListener("click", () => setFollow(!follow));
    wrapButton?.addEventListener("click", () => { wrap = !wrap; applyWrap(); });
    newOutput?.addEventListener("click", () => setFollow(true));
    searchInput?.addEventListener("input", refreshSearch);
    root.querySelector("[data-log-search-prev]")?.addEventListener("click", () => navigateSearch(-1));
    root.querySelector("[data-log-search-next]")?.addEventListener("click", () => navigateSearch(1));

    rawPre?.addEventListener("scroll", () => {
      if (currentView !== "raw") return;
      const nearBottom = rawPre.scrollHeight - rawPre.scrollTop - rawPre.clientHeight < 32;
      if (!nearBottom && follow) setFollow(false);
      if (nearBottom && !follow && newLines === 0) setFollow(true);
    });

    root.querySelectorAll("[data-step-output]").forEach((pre) => {
      pre.addEventListener("scroll", () => {
        if (stepStatus(pre.closest("[data-ci-step]")) !== "running") return;
        const nearBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
        if (!nearBottom && follow) setFollow(false);
      });
    });

    applyWrap();
    setFollow(true);
    if (!terminalStatuses.has(status)) schedulePoll(Number(root.dataset.refreshMs || "1500"));
  }

  function installLogViewers() {
    document.querySelectorAll("[data-ci-log-root]").forEach(setupLogViewer);
  }

  function installSourceViewer() {
    const root = document.querySelector("[data-source-view]");
    if (!root) return;
    let rangeAnchor = 0;

    function parseLineHash() {
      const match = window.location.hash.match(/^#L(\d+)(?:-L?(\d+))?$/);
      if (!match) return null;
      const start = Number(match[1]);
      const end = Number(match[2] || match[1]);
      if (!Number.isFinite(start) || !Number.isFinite(end)) return null;
      return { start: Math.min(start, end), end: Math.max(start, end) };
    }

    function applySelection(scroll = false) {
      const range = parseLineHash();
      root.querySelectorAll("[data-source-line].is-selected").forEach((row) => row.classList.remove("is-selected"));
      if (!range) return;
      for (let line = range.start; line <= range.end; line++) {
        root.querySelector(`[data-source-line="${line}"]`)?.classList.add("is-selected");
      }
      rangeAnchor = range.start;
      if (scroll) root.querySelector(`[data-source-line="${range.start}"]`)?.scrollIntoView({ block: "center" });
    }

    root.addEventListener("click", (event) => {
      const link = event.target.closest("[data-source-line-link]");
      if (!(link instanceof HTMLElement)) return;
      const line = Number(link.dataset.sourceLineLink || "0");
      if (!line) return;
      event.preventDefault();
      let hash = `#L${line}`;
      if (event.shiftKey && rangeAnchor) {
        const start = Math.min(rangeAnchor, line);
        const end = Math.max(rangeAnchor, line);
        hash = start === end ? `#L${start}` : `#L${start}-L${end}`;
      } else {
        rangeAnchor = line;
      }
      history.replaceState(null, "", hash);
      applySelection(false);
    });

    window.addEventListener("hashchange", () => applySelection(true));
    applySelection(Boolean(window.location.hash));
  }

  function installSourceCopy() {
    document.addEventListener("click", async (event) => {
      const button = event.target.closest("[data-copy-source-lines]");
      if (!(button instanceof HTMLElement)) return;
      const source = document.querySelector("[data-source-raw]");
      if (!(source instanceof HTMLTextAreaElement)) return;
      const normal = button.dataset.copyLabel || "Copy file";
      try {
        await copyText(source.value);
        button.textContent = "Copied";
        window.setTimeout(() => { button.textContent = normal; }, 1200);
      } catch (_) {
        button.textContent = "Copy failed";
        window.setTimeout(() => { button.textContent = normal; }, 1600);
      }
    });
  }

  function installDiffControls() {
    const files = Array.from(document.querySelectorAll("details.diff-file"));
    if (!files.length) return;
    document.querySelector("[data-diff-expand]")?.addEventListener("click", () => files.forEach((file) => { file.open = true; }));
    document.querySelector("[data-diff-collapse]")?.addEventListener("click", () => files.forEach((file) => { file.open = false; }));
  }

  function installFileFinder() {
    const dialog = document.querySelector("[data-file-finder]");
    if (!(dialog instanceof HTMLDialogElement)) return;
    const input = dialog.querySelector("[data-file-finder-input]");
    const results = dialog.querySelector("[data-file-finder-results]");
    const searchURL = dialog.dataset.searchUrl || "";
    let items = [];
    let activeIndex = 0;
    let timer = 0;
    let controller = null;

    function render(message = "") {
      if (!results) return;
      results.replaceChildren();
      if (message) {
        const empty = document.createElement("div");
        empty.className = "file-finder-empty";
        empty.textContent = message;
        results.appendChild(empty);
        return;
      }
      items.forEach((item, index) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = `file-finder-result${index === activeIndex ? " active" : ""}`;
        button.dataset.fileURL = item.url;
        button.dataset.fileIndex = String(index);
        button.setAttribute("role", "option");
        button.setAttribute("aria-selected", String(index === activeIndex));
        button.textContent = item.path;
        results.appendChild(button);
      });
      results.querySelector(".file-finder-result.active")?.scrollIntoView({ block: "nearest" });
    }

    function setActive(next) {
      if (!items.length) return;
      activeIndex = (next + items.length) % items.length;
      render();
    }

    async function search() {
      if (!searchURL || !(input instanceof HTMLInputElement)) return;
      controller?.abort();
      controller = new AbortController();
      try {
        const endpoint = new URL(searchURL, window.location.href);
        endpoint.searchParams.set("q", input.value.trim());
        const response = await fetch(endpoint, { credentials: "same-origin", cache: "no-store", signal: controller.signal });
        if (!response.ok) throw new Error(`file search failed: ${response.status}`);
        const payload = await response.json();
        items = Array.isArray(payload.results) ? payload.results : [];
        activeIndex = 0;
        render(items.length ? "" : "No matching files");
      } catch (error) {
        if (error?.name === "AbortError") return;
        items = [];
        render("Could not load repository files");
      }
    }

    function queueSearch() {
      window.clearTimeout(timer);
      timer = window.setTimeout(search, 110);
    }

    function openFinder() {
      const shortcuts = document.querySelector("[data-shortcuts-dialog][open]");
      if (shortcuts instanceof HTMLDialogElement) shortcuts.close();
      if (!dialog.open) dialog.showModal();
      if (input instanceof HTMLInputElement) {
        input.focus();
        input.select();
      }
      search();
    }

    document.querySelectorAll("[data-file-finder-open]").forEach((button) => button.addEventListener("click", openFinder));
    dialog.querySelector("[data-file-finder-close]")?.addEventListener("click", () => dialog.close());
    input?.addEventListener("input", queueSearch);
    input?.addEventListener("keydown", (event) => {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setActive(activeIndex + 1);
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        setActive(activeIndex - 1);
      } else if (event.key === "Enter" && items[activeIndex]) {
        event.preventDefault();
        window.location.href = items[activeIndex].url;
      } else if (event.key === "Escape") {
        dialog.close();
      }
    });
    results?.addEventListener("click", (event) => {
      const item = event.target.closest("[data-file-url]");
      if (!(item instanceof HTMLElement) || !item.dataset.fileUrl) return;
      window.location.href = item.dataset.fileUrl;
    });

    document.addEventListener("keydown", (event) => {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return;
      if (event.key.toLowerCase() !== "t") return;
      const target = event.target;
      if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement || target?.isContentEditable) return;
      event.preventDefault();
      openFinder();
    });
  }



  function installRepoShortcuts() {
    const root = document.querySelector("[data-repo-shortcuts]");
    const dialog = document.querySelector("[data-shortcuts-dialog]");
    if (!(root instanceof HTMLElement) || !(dialog instanceof HTMLDialogElement)) return;
    let chord = "";
    let chordTimer = 0;

    function isEditable(target) {
      return target instanceof HTMLInputElement
        || target instanceof HTMLTextAreaElement
        || target instanceof HTMLSelectElement
        || target?.isContentEditable;
    }

    function resetChord() {
      chord = "";
      window.clearTimeout(chordTimer);
    }

    function openHelp() {
      const finder = document.querySelector("[data-file-finder][open]");
      if (finder instanceof HTMLDialogElement) finder.close();
      if (!dialog.open) dialog.showModal();
    }

    document.querySelectorAll("[data-shortcuts-open]").forEach((button) => button.addEventListener("click", openHelp));
    dialog.querySelector("[data-shortcuts-close]")?.addEventListener("click", () => dialog.close());

    document.addEventListener("keydown", (event) => {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
      if (isEditable(event.target)) return;
      if (event.key === "?") {
        event.preventDefault();
        resetChord();
        openHelp();
        return;
      }
      if (dialog.open) return;

      const key = event.key.toLowerCase();
      if (key === "g" && !event.shiftKey) {
        event.preventDefault();
        chord = "g";
        window.clearTimeout(chordTimer);
        chordTimer = window.setTimeout(resetChord, 1200);
        return;
      }
      if (chord !== "g") return;
      resetChord();
      const targetURL = key === "f"
        ? root.dataset.filesUrl
        : key === "c"
          ? root.dataset.commitsUrl
          : key === "i"
            ? root.dataset.ciUrl
            : "";
      if (!targetURL) return;
      event.preventDefault();
      window.location.href = targetURL;
    });
  }



  function installDisclosureMenus() {
    const menus = Array.from(document.querySelectorAll("details.clone-menu, details.archive-menu"));
    if (!menus.length) return;
    menus.forEach((menu) => menu.addEventListener("toggle", () => {
      if (!menu.open) return;
      menus.forEach((other) => { if (other !== menu) other.open = false; });
    }));
    document.addEventListener("click", (event) => {
      menus.forEach((menu) => {
        if (menu.open && !menu.contains(event.target)) menu.open = false;
      });
    });
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      menus.forEach((menu) => { menu.open = false; });
    });
  }

  function start() {
    installConfirmForms();
    installCopyActions();
    installRelativeTimes();
    installLogViewers();
    installSourceViewer();
    installSourceCopy();
    installDiffControls();
    installFileFinder();
    installRepoShortcuts();
    installDisclosureMenus();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
