// Drives a real Chromium against a running Gitman that browser.sh has
// seeded, and checks what a person using it would notice:
//
//   1. live pages — Home and a repository updating by themselves, a Run
//      page streaming output and following the run, focus kept across a
//      live update, cancelling through the confirmation dialog;
//   2. every interactive component, by keyboard — tabs and the address
//      they keep, menus, dialogs, confirmations, toasts, the command
//      palette, the file finder, copy buttons, inline editing, and the
//      log view;
//   3. an accessibility scan (axe-core, WCAG 2.1 A and AA) of every
//      screen, and of its open dialogs and menus, in both themes;
//   4. every page with JavaScript disabled;
//   5. a screenshot of every screen in both themes, in $SHOTS.
import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";
import { execSync } from "node:child_process";
import { mkdirSync } from "node:fs";

const { BASE: base, PASSWORD: password, COMMIT: commit, FEATURE: feature, SHOTS: shots } = process.env;
const sh = (cmd) => execSync(cmd, { stdio: ["ignore", "pipe", "pipe"], shell: "/bin/bash" }).toString();
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const results = [];
function check(name, ok, detail = "") {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? " — " + detail : ""}`);
}
// until polls fn until it returns something truthy, for at most timeout.
async function until(fn, timeout = 20000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    try {
      const value = await fn();
      if (value) return value;
    } catch { /* not there yet */ }
    await sleep(250);
  }
  return null;
}

const browser = await chromium.launch(
  process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {},
);
const scriptErrors = [];

// signedIn is a fresh browser context with darius signed in, and a page.
async function signedIn(options = {}) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, ...options });
  const page = await context.newPage();
  await signIn(page);
  return { context, page };
}
async function signIn(page) {
  await page.goto(`${base}/login`);
  await page.fill("#f-username", "darius");
  await page.fill("#f-password", password);
  await Promise.all([page.waitForURL(`${base}/`), page.click("button[type=submit]")]);
}
function watch(page) {
  // A page answering 404 or 422 is logged as a failed load; that is the
  // page doing its job, not a script failing.
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) scriptErrors.push(`${page.url()}: ${m.text()}`);
  });
  page.on("pageerror", (e) => scriptErrors.push(`${page.url()}: ${e}`));
  return page;
}
const focused = (page) => page.evaluate(() => {
  const el = document.activeElement;
  return el ? { id: el.id, role: el.getAttribute("role"), href: el.getAttribute("href"), text: el.textContent.trim(), tag: el.tagName } : null;
});
const query = (page) => new URL(page.url()).searchParams;

// ---- 1. Live pages ----------------------------------------------------
{
  const { context, page: home } = await signedIn();
  watch(home);
  const repo = watch(await context.newPage());
  await repo.goto(`${base}/demo`);

  sh(process.env.STOP_WORKER);
  const homeBefore = await home.locator("[data-live-region=progress]").innerText();
  sh(process.env.PUSH_SLOW);

  const homeUpdated = await until(async () => /Run #6/.test(await home.locator("[data-live-region=progress]").innerText()));
  check("Home shows a new run live", homeUpdated && !/Run #6/.test(homeBefore));
  check("Home says it is live", (await home.locator("[data-live-status]").getAttribute("data-state")) === "open");
  const repoUpdated = await until(async () => /slow\/one/.test(await repo.locator("[data-live-region=branches]").innerText()));
  check("the Repository page shows the new branch and its run live", repoUpdated);
  await repo.close();

  // Focus on Home, inside a region that is about to be replaced.
  const runLink = home.locator("[data-live-region=progress] a[href$='/runs/6']");
  await runLink.focus();
  const regionBefore = await home.locator("[data-live-region=progress]").elementHandle();

  const run = watch(await context.newPage());
  await run.goto(`${base}/demo/runs/6`);
  check("a run page opened while queued says so", /queued/i.test(await run.locator(".run-heading .status").innerText()));

  sh(process.env.START_WORKER);

  const swapped = await until(async () => !(await regionBefore.evaluate((el) => el.isConnected))
    && /running/i.test(await home.locator("[data-live-region=progress]").innerText()));
  const after = await focused(home);
  check("focus survives a live update", swapped && after?.href === "/demo/runs/6", JSON.stringify(after));

  let buildStep = null;
  const sawBuild = await until(async () => {
    const log = run.locator("[data-log]");
    if ((await log.count()) && /building demo/.test(await log.innerText())) {
      buildStep = await log.getAttribute("data-log-step");
      return true;
    }
    return false;
  }, 30000);
  check("output streams to a page opened before the first step", sawBuild);

  const followed = await until(async () => {
    const log = run.locator("[data-log]");
    return (await log.count()) && (await log.getAttribute("data-log-step")) !== buildStep && /tick \d/.test(await log.innerText());
  }, 30000);
  check("the page follows the run onto its next step by itself", sawBuild && followed);
  check("the run page says it is live", (await run.locator("[data-live-status]").innerText()).includes("Live"));

  const url = run.url();
  const lines = () => run.locator("[data-log] .line").count();
  const n0 = await lines();
  await run.waitForTimeout(3000);
  const n1 = await lines();
  check("output appends within a step without a reload", n1 > n0 && run.url() === url, `${n0} → ${n1} lines`);
  const atEnd = () => run.locator("[data-log]").evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 30);
  check("the log follows new output while at its end", await atEnd());

  // Once there is more output than fits, scrolling up stops following.
  await until(() => run.locator("[data-log]").evaluate((el) => el.scrollHeight > el.clientHeight + 200), 90000);
  await run.locator("[data-log]").evaluate((el) => { el.scrollTop = 0; });
  const offered = await until(() => run.locator("[data-log-latest]").isVisible(), 8000);
  check("scrolled up, the log stops following and offers the newest output", offered && !(await atEnd()));
  if (offered) await run.click("[data-log-latest]");
  check("the new-output button goes back to following", (await atEnd()) && !(await run.locator("[data-log-latest]").isVisible()));

  // Cancel, through the confirmation: Escape keeps the run going, and
  // the confirmed action stops it.
  await run.click("form[action$='/cancel'] button");
  const confirm = run.locator("dialog[role=alertdialog]");
  check("cancelling asks first", (await confirm.isVisible()) && /Cancel run #6\?/.test(await confirm.innerText()));
  check("the confirmation starts on the safe choice", (await focused(run))?.text === "Cancel");
  await run.keyboard.press("Escape");
  check("Escape declines, and focus returns to the button", !(await confirm.isVisible()) && (await focused(run))?.text.includes("Cancel")
    && /running/i.test(await run.locator(".run-heading .status").innerText()));
  await run.click("form[action$='/cancel'] button");
  await confirm.getByRole("button", { name: "Cancel run" }).click();
  const cancelled = await until(async () => /cancelled|stopping/i.test(await run.locator(".run-heading .status").innerText()), 20000);
  check("the confirmed cancel stops the run", cancelled);
  await context.close();
}

// ---- 2. Components, by keyboard --------------------------------------
{
  const { context, page } = await signedIn();
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base });
  watch(page);

  await page.goto(`${base}/`);
  await page.keyboard.press("Tab");
  check("the first Tab reaches the skip link", (await focused(page))?.text === "Skip to content");
  await page.keyboard.press("Tab");
  const ring = await page.evaluate(() => getComputedStyle(document.activeElement).outlineStyle);
  check("keyboard focus is visible", ring !== "none", `outline ${ring}`);

  // Tabs keep the address.
  await page.goto(`${base}/demo`);
  await page.locator("[role=tab][data-tab=branches]").focus();
  await page.keyboard.press("ArrowRight");
  check("arrow keys move between tabs and the address follows",
    (await page.locator("[data-tab=tags]").getAttribute("aria-selected")) === "true" && query(page).get("tab") === "tags"
    && (await page.locator("#panel-tags").isVisible()) && !(await page.locator("#panel-branches").isVisible())
    && (await focused(page))?.role === "tab");
  await page.goBack();
  check("Back returns to the tab before", (await page.locator("[data-tab=branches]").getAttribute("aria-selected")) === "true"
    && !query(page).has("tab") && (await page.locator("#panel-branches").isVisible()));
  await page.goto(`${base}/demo?tab=tags`);
  check("a tab's address opens that tab", (await page.locator("[data-tab=tags]").getAttribute("aria-selected")) === "true"
    && (await page.locator("#panel-tags").isVisible()));
  await page.goto(`${base}/demo/settings`);
  await page.locator("[role=tab][data-tab=general]").focus();
  await page.keyboard.press("ArrowDown");
  check("vertical tabs move with the up and down keys", query(page).get("tab") === "rules" && (await page.locator("#panel-rules").isVisible()));

  // Menus.
  await page.goto(`${base}/people`);
  const summary = page.locator("summary[aria-label='Actions for mina']");
  await summary.focus();
  await page.keyboard.press("Enter");
  const first = await until(async () => {
    const now = await focused(page);
    return now?.role === "menuitem" ? now : null;
  }, 3000);
  check("a menu opens on Enter with its first item focused", !!first && (await summary.getAttribute("aria-expanded")) === "true",
    JSON.stringify(first ?? (await focused(page))));
  const clear = await page.locator("details[open] .menu-list").evaluate((el) => {
    const r = el.getBoundingClientRect();
    return el.contains(document.elementFromPoint(r.left + r.width / 2, r.bottom - 6));
  });
  check("a menu in a table is not cut off by it", clear);
  await page.keyboard.press("ArrowDown");
  const second = await focused(page);
  check("arrow keys move through a menu", second?.role === "menuitem" && second.text !== first?.text);
  await page.keyboard.press("Escape");
  check("Escape closes a menu and returns focus to its button", !(await page.locator("details[data-menu][open]").count())
    && (await summary.evaluate((el) => el === document.activeElement)));

  // Dialogs keep the address too.
  await page.goto(`${base}/`);
  const opener = page.locator("[data-dialog-open=new-repo]");
  await opener.focus();
  await page.keyboard.press("Enter");
  const dialog = page.locator("#new-repo");
  check("a dialog opens as a modal, with the address naming it",
    (await dialog.evaluate((el) => el.matches(":modal"))) && query(page).get("dialog") === "new-repo" && (await focused(page))?.id === "f-name");
  await page.keyboard.press("Escape");
  const closed = await until(async () => !(await dialog.isVisible()) && !query(page).has("dialog"), 3000);
  check("Escape closes a dialog, clears the address and returns focus",
    closed && (await opener.evaluate((el) => el === document.activeElement)), JSON.stringify(await focused(page)));
  await opener.click();
  await page.goBack();
  check("Back closes a dialog", !(await dialog.isVisible()) && !query(page).has("dialog"));
  await opener.click();
  await page.fill("#f-name", "has space");
  await Promise.all([page.waitForLoadState("load"), page.keyboard.press("Enter")]);
  await page.waitForSelector("#new-repo[open]");
  check("a form that fails comes back open, with the problem focused",
    (await page.locator("#new-repo").evaluate((el) => el.matches(":modal"))) && (await focused(page))?.id === "f-name"
    && (await page.locator("#f-name").getAttribute("aria-invalid")) === "true");

  // Rule form parts that only matter for one choice.
  await page.goto(`${base}/demo/settings?tab=rules`);
  await page.click("[data-dialog-open=rule-new]");
  const named = page.locator("#rule-new [data-show-when]");
  const hiddenFirst = !(await named.isVisible());
  await page.locator("#rule-new input[name=push_policy][value=people]").check();
  check("the people to name show only for a named-people rule", hiddenFirst && (await named.isVisible()));
  await page.keyboard.press("Escape");
  // Closing goes back in history; the next page load must not race it.
  await page.waitForFunction(() => !new URL(location.href).searchParams.has("dialog"));

  // Confirmation, then a toast.
  await page.goto(`${base}/me`);
  const revoke = page.locator("tr", { hasText: "ci-readonly" }).getByRole("button", { name: "Revoke" });
  await revoke.click();
  const alert = page.locator("dialog[role=alertdialog]");
  check("revoking a token asks first", (await alert.isVisible()) && /ci-readonly/.test(await alert.innerText()));
  await alert.getByRole("button", { name: "Cancel" }).click();
  check("declining keeps the token", await page.locator("tr", { hasText: "ci-readonly" }).isVisible());
  await revoke.click();
  await Promise.all([page.waitForURL(`${base}/me`), alert.getByRole("button", { name: "Revoke token" }).click()]);
  const toast = page.locator("[data-toast][role=status]");
  check("the outcome shows as a toast", await until(() => toast.isVisible(), 5000) && /Revoked/.test(await toast.innerText()));
  check("the confirmed revoke removed the token", !(await page.locator("tr", { hasText: "ci-readonly" }).count()));
  await toast.getByRole("button", { name: "Dismiss" }).click();
  check("a toast can be dismissed", await until(async () => !(await toast.count()), 3000));

  // The command palette.
  await page.goto(`${base}/demo`);
  await page.keyboard.press("Control+K");
  const input = page.locator("dialog[open] input[role=combobox]");
  check("Ctrl+K opens the palette with the search focused", await until(() => input.evaluate((el) => el === document.activeElement), 3000));
  await input.fill("waiote");
  await until(async () => (await page.locator("dialog[open] [role=option]").count()) > 0, 5000);
  check("the palette's search marks the active choice", !!(await input.getAttribute("aria-activedescendant")));
  await Promise.all([page.waitForURL(`${base}/waiotech`), page.keyboard.press("Enter")]);
  check("Enter goes to the palette's choice", page.url() === `${base}/waiotech`);
  await page.goto(`${base}/demo/runs/3`);
  await page.keyboard.press("Control+K");
  await page.locator("dialog[open] input[role=combobox]").fill("raw output");
  check("the palette offers what the page can do", await until(() => page.locator("dialog[open] [role=option]", { hasText: "raw output of test" }).count(), 3000));
  await page.keyboard.press("Escape");

  // The log view.
  const log = page.locator("[data-log]");
  check("a failed step's log opens at its end", await log.evaluate((el) => el.scrollTop > 0 && el.scrollHeight - el.scrollTop - el.clientHeight < 30));
  check("the lines that failed are marked", /FAIL/.test(await page.locator("[data-log] .line.is-error").first().innerText()));
  await page.click("[data-log-wrap]");
  check("long lines wrap on request", (await page.locator("[data-log-wrap]").getAttribute("aria-pressed")) === "true"
    && (await log.evaluate((el) => el.classList.contains("is-wrapped"))));
  await page.click("[data-log-wrap]");
  await page.click("[data-log-jump=start]");
  check("the log jumps to its first line", await log.evaluate((el) => el.scrollTop === 0));
  await page.goto(`${base}/demo/runs/3#L41`);
  check("a line's address highlights that line", await page.locator("#L41").evaluate((el) => el.matches(":target")));
  await page.click("a[href='?step=0']");
  check("choosing a step shows its output, and the address says which", query(page).get("step") === "0"
    && (await page.locator(".step[aria-current=step]").innerText()).includes("build"));

  // The file finder and the ref picker.
  await page.goto(`${base}/demo@main`);
  await page.click("[data-file-finder-open]");
  await page.locator("dialog[open] input[role=combobox]").fill("chgo");
  await until(() => page.locator("dialog[open] [role=option]").count(), 5000);
  await Promise.all([page.waitForURL(/charge\.go$/), page.keyboard.press("Enter")]);
  check("the file finder finds a file by a fuzzy name", page.url().endsWith("/demo@main/internal/pay/charge.go"));
  await Promise.all([page.waitForURL(/\/demo\/history\?/), page.click(".page-actions a:has-text('History')")]);
  check("a file's History button leads to History, filtered to its path",
    query(page).get("path") === "internal/pay/charge.go" && query(page).get("ref") === "main");
  await Promise.all([page.waitForURL(/ref=broken/), page.selectOption("[data-ref-picker]", "broken")]);
  check("choosing a branch in History keeps the path", query(page).get("path") === "internal/pay/charge.go");
  await page.goto(`${base}/demo@main/internal/pay/charge.go`);
  await Promise.all([page.waitForURL(/@broken/), page.selectOption("[data-ref-picker]", "broken")]);
  check("choosing a branch goes to the same file there", page.url() === `${base}/demo@broken/internal/pay/charge.go`);

  // Copying, inline editing, times.
  await page.goto(`${base}/demo`);
  await page.click("button[aria-label='Copy HTTPS']");
  check("a copy button copies", (await page.evaluate(() => navigator.clipboard.readText())) === `${base}/demo.git`);
  check("hovering a time shows it in the reader's time zone", !/UTC$/.test(await page.locator("time").first().getAttribute("title")));
  await page.goto(`${base}/demo/settings`);
  await page.click("[data-inline-start]");
  check("Edit turns the description into its form, focused", (await focused(page))?.id === "f-description");
  await page.keyboard.press("Escape");
  check("Escape turns it back", (await page.locator("[data-inline-start]").evaluate((el) => el === document.activeElement)));
  await page.click("[data-inline-start]");
  const original = await page.inputValue("#f-description");
  await page.fill("#f-description", "Edited in place");
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  check("an inline edit saves", (await page.locator(".inline-edit-value").innerText()) === "Edited in place"
    && /Saved/.test(await page.locator("[data-toast]").innerText()));
  await page.click("[data-inline-start]");
  await page.fill("#f-description", original);
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  await context.close();
}

// ---- 3. Accessibility, both themes ----------------------------------
const screens = [
  ["home", "/", "#home-title"],
  ["repository", "/demo", "#repo-title"],
  ["repository-tags", "/demo?tab=tags", "#repo-title"],
  ["files", "/demo@main", "#files-title"],
  ["directory", "/demo@main/internal/pay", "#files-title"],
  ["file", "/demo@main/internal/pay/charge.go", "#files-title"],
  ["history", "/demo/history", "#history-title"],
  ["history-path", "/demo/history?ref=main&path=internal/pay/charge.go", "#history-title"],
  ["history-activity", "/demo/history?tab=activity", "#history-title"],
  ["commit", `/demo/commit/${commit}`, "#commit-title"],
  ["compare", `/demo/compare/main...${feature}`, "#compare-title"],
  ["compare-commits", `/demo/compare/main...${feature}?tab=commits`, "#compare-title"],
  ["run-failed", "/demo/runs/3", "#run-title"],
  ["run-passed", "/demo/runs/4", "#run-title"],
  ["run-cancelled", "/demo/runs/6", "#run-title"],
  ["run-long-branch", "/demo/runs/5", "#run-title"],
  ["settings", "/demo/settings", "#settings-title"],
  ["settings-rules", "/demo/settings?tab=rules", "#settings-title"],
  ["settings-rule-edit", "/demo/settings?tab=rules&dialog=rule-edit&kind=branch&pattern=main", "#settings-title"],
  ["settings-secrets", "/demo/settings?tab=secrets", "#settings-title"],
  ["settings-danger", "/demo/settings?tab=danger", "#settings-title"],
  ["people", "/people", "#people-title"],
  ["account", "/me", "#account-title"],
  ["account-password", "/me?tab=password", "#account-title"],
  ["new-token", "/me?dialog=token-new", "#account-title"],
  ["jump", "/jump", "#jump-title"],
  ["not-found", "/nope/at/all", "#error-title"],
];
// Open states: what a page looks like mid-interaction.
const states = [
  ["palette", "/demo", async (p) => { await p.keyboard.press("Control+K"); await p.locator("dialog[open] input").fill("pay"); }],
  ["file-finder", "/demo@main", async (p) => { await p.click("[data-file-finder-open]"); await p.locator("dialog[open] input").fill("chgo"); }],
  ["menu", "/people", async (p) => { await p.click("summary[aria-label='Actions for mina']"); }],
  ["confirm", "/people", async (p) => {
    await p.click("summary[aria-label='Actions for mina']");
    await p.locator("details[open] .menu-list").getByRole("menuitem", { name: "Disable" }).click();
  }],
  ["delete-confirm", "/demo", async (p) => { await p.click("button[aria-label^='Delete branch feature']"); }],
  ["new-repo", "/", async (p) => { await p.click("[data-dialog-open=new-repo]"); }],
  ["account-menu", "/", async (p) => { await p.click("summary[aria-label^='Account menu']"); }],
];

async function scan(page, label) {
  const report = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const problems = report.violations.map((v) => `${v.id} (${v.nodes.map((n) => n.target.join(" ")).slice(0, 3).join(", ")})`);
  check(`no accessibility violations: ${label}`, problems.length === 0, problems.join("; "));
}

// Scanned with reduced motion, so a dialog is measured as it rests, not
// halfway through fading in.
for (const scheme of ["light", "dark"]) {
  const { context, page } = await signedIn({ colorScheme: scheme, reducedMotion: "reduce" });
  watch(page);
  for (const [name, path] of screens) {
    await page.goto(base + path);
    await scan(page, `${name}, ${scheme}`);
  }
  for (const [name, path, act] of states) {
    await page.goto(base + path);
    await act(page);
    await page.waitForTimeout(400);
    await scan(page, `${name} open, ${scheme}`);
  }
  await context.close();
  const out = await browser.newContext({ colorScheme: scheme, reducedMotion: "reduce" });
  const signin = await out.newPage();
  await signin.goto(`${base}/login`);
  await scan(signin, `sign in, ${scheme}`);
  await out.close();
}

// ---- Phone width: nothing scrolls sideways but what is meant to --------
{
  const { context, page } = await signedIn({ viewport: { width: 390, height: 844 } });
  const wide = [];
  for (const [name, path] of screens) {
    await page.goto(base + path);
    const width = await page.evaluate(() => document.documentElement.scrollWidth);
    if (width > 390) wide.push(`${name} ${width}px`);
  }
  check("no screen scrolls sideways at phone width", wide.length === 0, wide.join(", "));
  await context.close();
}

// ---- 4. Every page, with JavaScript disabled -----------------------
{
  const context = await browser.newContext({ javaScriptEnabled: false });
  const page = await context.newPage();
  await page.goto(`${base}/login`);
  await page.fill("#f-username", "darius");
  await page.fill("#f-password", password);
  await page.click("button[type=submit]");
  await page.waitForURL(`${base}/`).catch(() => {});
  check("signing in works without JavaScript", page.url() === `${base}/`);
  for (const [name, path, heading] of screens) {
    const resp = await page.goto(base + path);
    const ok = (resp.ok() || name === "not-found") && (await page.locator(heading).count()) > 0;
    check(`${name} reads without JavaScript`, ok, `status ${resp.status()}`);
  }
  await page.goto(`${base}/demo@main`);
  check("without JavaScript, script-only controls are hidden and fallbacks shown",
    (await page.locator("html").getAttribute("class")) === "no-js" && !(await page.locator("[data-file-finder-open]").isVisible())
    && (await page.locator("button", { hasText: "Switch" }).isVisible()));
  await page.selectOption("[data-ref-picker]", "broken");
  await Promise.all([page.waitForURL(`${base}/demo@broken`), page.click("button:has-text('Switch')")]);
  check("switching branches works without JavaScript", page.url() === `${base}/demo@broken`);
  await page.goto(`${base}/demo/history?path=internal/pay/charge.go`);
  await page.selectOption("[data-ref-picker]", "broken");
  await Promise.all([page.waitForURL(/ref=broken/), page.click("button:has-text('Switch')")]);
  check("switching branches in History works without JavaScript",
    query(page).get("ref") === "broken" && query(page).get("path") === "internal/pay/charge.go");
  await page.goto(`${base}/demo/settings?tab=secrets`);
  check("a tab's address works without JavaScript", (await page.locator("#panel-secrets").isVisible()) && !(await page.locator("#panel-general").isVisible()));
  await page.click("a:has-text('New secret')");
  check("a dialog's link shows its form without JavaScript", await page.locator("#secret-new form").isVisible());
  await page.goto(`${base}/demo/settings`);
  const description = await page.inputValue("#f-description");
  await page.fill("#f-description", "Saved without JavaScript");
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  check("saving a form works without JavaScript", (await page.inputValue("#f-description")) === "Saved without JavaScript");
  await page.fill("#f-description", description);
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  await page.click("a.palette-button");
  check("Jump to is a page without JavaScript", page.url() === `${base}/jump` && (await page.locator("a[href='/waiotech']").count()) > 0);
  await context.close();
}

// ---- 5. Screenshots ---------------------------------------------------
{
  mkdirSync(shots, { recursive: true });
  // A run in progress, for its screenshot.
  sh(process.env.PUSH_SLOW_AGAIN);
  const { context, page } = await signedIn();
  await page.goto(`${base}/demo/runs/7`);
  await until(async () => /tick \d+/.test(await page.locator("[data-log]").innerText()), 60000);
  await context.close();
  const all = [...screens, ["run-running", "/demo/runs/7"]];
  for (const scheme of ["light", "dark"]) {
    const signedOut = await browser.newContext({ colorScheme: scheme, viewport: { width: 1440, height: 900 } });
    const login = await signedOut.newPage();
    await login.goto(`${base}/login`);
    await login.screenshot({ path: `${shots}/login-${scheme}.png` });
    await signedOut.close();
    for (const [width, height, suffix] of [[1440, 900, ""], [390, 844, "-mobile"]]) {
      const { context: shotContext, page: shot } = await signedIn({ colorScheme: scheme, viewport: { width, height }, reducedMotion: "reduce" });
      for (const [name, path] of all) {
        if (suffix && !["home", "repository", "file", "run-failed", "run-running"].includes(name)) continue;
        await shot.goto(base + path);
        await shot.mouse.move(0, 0);
        await shot.waitForTimeout(name === "run-running" ? 1500 : 300);
        await shot.screenshot({ path: `${shots}/${name}${suffix}-${scheme}.png`, fullPage: !name.startsWith("run-") });
      }
      if (!suffix) {
        for (const [name, path, act] of states) {
          await shot.goto(base + path);
          await act(shot);
          await shot.waitForTimeout(400);
          await shot.screenshot({ path: `${shots}/${name}-${scheme}.png` });
        }
      }
      await shotContext.close();
    }
  }
  // Stop the run started for its screenshot.
  const { context: last, page: stop } = await signedIn();
  await stop.goto(`${base}/demo/runs/7`);
  await stop.click("form[action$='/cancel'] button");
  await stop.locator("dialog[role=alertdialog]").getByRole("button", { name: "Cancel run" }).click();
  await until(async () => /cancelled|stopping/i.test(await stop.locator(".run-heading .status").innerText()), 20000);
  await last.close();
  check("screenshots of every screen, both themes", true, shots);
}

check("no script errors", scriptErrors.length === 0, scriptErrors.slice(0, 5).join(" | "));
await browser.close();
console.log(results.join("\n"));
const failed = results.filter((r) => r.startsWith("FAIL")).length;
console.log(`\n${results.length - failed} passed, ${failed} failed`);
if (failed) process.exit(1);
