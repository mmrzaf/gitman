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
  const homeBefore = await home.locator("[data-live-region=repos]").innerText();
  sh(process.env.PUSH_SLOW);

  const homeUpdated = await until(async () => /#6/.test(await home.locator("[data-live-region=repos]").innerText()));
  check("Home shows a new run live", homeUpdated && !/#6/.test(homeBefore));
  check("Home says it is live", (await home.locator("[data-live-status]").getAttribute("data-state")) === "open");
  const repoUpdated = await until(async () => /slow\/one/.test(await repo.locator("[data-live-region=branches]").innerText()));
  check("the Repository page shows the new branch and its run live", repoUpdated);
  await repo.close();

  // Focus on Home, inside a region that is about to be replaced.
  const runLink = home.locator("[data-live-region=repos] a[href$='/runs/6']");
  await runLink.focus();
  const regionBefore = await home.locator("[data-live-region=repos]").elementHandle();

  const run = watch(await context.newPage());
  await run.goto(`${base}/demo/runs/6`);
  check("a run page opened while queued says so", /queued/i.test(await run.locator(".run-heading .status").innerText()));

  sh(process.env.START_WORKER);

  const swapped = await until(async () => !(await regionBefore.evaluate((el) => el.isConnected))
    && /running/i.test(await home.locator("[data-live-region=repos]").innerText()));
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
  await page.goto(`${base}/me`);
  await page.locator("[role=tab][data-tab=tokens]").focus();
  await page.keyboard.press("ArrowRight");
  check("arrow keys move between tabs and the address follows",
    (await page.locator("[data-tab=password]").getAttribute("aria-selected")) === "true" && query(page).get("tab") === "password"
    && (await page.locator("#panel-password").isVisible()) && !(await page.locator("#panel-tokens").isVisible())
    && (await focused(page))?.role === "tab");
  await page.goBack();
  check("Back returns to the tab before", (await page.locator("[data-tab=tokens]").getAttribute("aria-selected")) === "true"
    && !query(page).has("tab") && (await page.locator("#panel-tokens").isVisible()));
  await page.goto(`${base}/me?tab=password`);
  check("a tab's address opens that tab", (await page.locator("[data-tab=password]").getAttribute("aria-selected")) === "true"
    && (await page.locator("#panel-password").isVisible()));
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
  await Promise.all([page.waitForURL(/\/demo\/commits\?/), page.click(".page-actions a:has-text('Commits')")]);
  check("a file's Commits button leads to its commits",
    query(page).get("path") === "internal/pay/charge.go" && query(page).get("ref") === "main");
  await page.goto(`${base}/demo@main/internal/pay/charge.go`);
  await Promise.all([page.waitForURL(/@broken/), page.selectOption("[data-ref-picker]", "broken")]);
  check("choosing a branch goes to the same file there", page.url() === `${base}/demo@broken/internal/pay/charge.go`);

  // Commits: the two ref fields are searchable lists; choosing sends the form.
  await page.goto(`${base}/demo/commits`);
  await page.evaluate(() => { window.__sameDocument = true; });
  check("the ref fields are buttons, not text boxes", (await page.locator("button.ref-button").count()) === 2
    && (await page.locator("#commits-ref-button").innerText()).trim() === "main");
  await page.click("#commits-base-button");
  check("the picker says what leaving it empty means", (await page.locator("dialog[open] [role=option]").first().innerText()).includes("No comparison"));
  await page.locator("dialog[open] input[role=combobox]").fill("v1.4");
  await until(() => page.locator("dialog[open] [role=option]").count(), 5000);
  await Promise.all([page.waitForURL(/base=v1\.4\.0/), page.keyboard.press("Enter")]);
  check("choosing a base compares it with the ref", query(page).get("base") === "v1.4.0" && query(page).get("ref") === "main"
    && (await page.locator(".compare-summary").count()) === 1 && (await page.locator("[data-tab=changes]").count()) === 1);
  check("choosing a ref swaps the list in place: the page is not reloaded, and stays still",
    (await page.evaluate(() => window.__sameDocument === true)) && (await page.locator(".live").count()) === 0);
  await page.click("[data-tab=changes]");
  check("the files tab shows the changes, and the address follows",
    query(page).get("tab") === "changes" && (await page.locator("#panel-changes").isVisible()) && !(await page.locator("#panel-commits").isVisible()));
  await page.click("#commits-ref-button");
  await page.locator("dialog[open] input[role=combobox]").fill(process.env.COMMIT.slice(0, 9));
  check("a typed hash is offered as a commit", (await page.locator("dialog[open] [role=option]").first().innerText()).includes(`Commit ${process.env.COMMIT.slice(0, 9)}`));
  await Promise.all([page.waitForURL(new RegExp(`ref=${process.env.COMMIT.slice(0, 9)}`)), page.keyboard.press("Enter")]);
  check("choosing a commit keeps the base", query(page).get("base") === "v1.4.0");
  await page.click("#commits-base-button");
  await Promise.all([page.waitForURL((url) => !url.searchParams.get("base")), page.locator("dialog[open] [role=option]").first().click()]);
  check("No comparison goes back to the log", (await page.locator(".compare-summary").count()) === 0);
  await page.goBack();
  await until(async () => query(page).get("base") === "v1.4.0" && (await page.locator(".compare-summary").count()) === 1, 5000);
  check("Back returns to the comparison, in place", query(page).get("base") === "v1.4.0" && (await page.evaluate(() => window.__sameDocument === true)));

  // The changes of a commit are a list of files, each opening to its diff.
  await page.goto(`${base}/demo/commits?ref=v1.4.0`);
  await page.goto(await page.locator("table a[href*='/commit/']").first().evaluate((a) => a.href));
  const files = page.locator(".diff-file");
  check("a commit of many files lists them closed", (await files.count()) > 3 && (await page.locator(".diff-file[open]").count()) === 0);
  await page.click("[data-diff-all=open]");
  check("Expand all opens every file", (await page.locator(".diff-file[open]").count()) === (await files.count()));
  await page.click("[data-diff-all=closed]");
  check("Collapse all closes them again", (await page.locator(".diff-file[open]").count()) === 0);
  await files.first().locator("summary").click();
  check("a file's row opens its own diff", (await page.locator(".diff-file[open]").count()) === 1 && (await files.first().locator(".diff-table").isVisible()));

  // Downloading an archive of what the page shows.
  await page.goto(`${base}/demo@main`);
  const download = page.waitForEvent("download");
  await page.click("a:has-text('Download')");
  check("Download saves an archive named for the repository and ref", (await download).suggestedFilename() === "demo-main.tar.gz");

  // The Run dialog picks its branch or tag the way Commits does.
  await page.goto(`${base}/demo/runs?dialog=run-new`);
  check("the run dialog's ref is a searchable button", (await page.locator("#f-run-ref-button").count()) === 1
    && (await page.locator("#f-run-ref-button").innerText()).includes("main"));
  await page.click("#f-run-ref-button");
  await page.locator("dialog[open] input[role=combobox]").fill("broken");
  await until(() => page.locator("dialog[open] [role=option]").count(), 5000);
  await page.keyboard.press("Enter");
  check("choosing a ref fills the run form without sending it", (await page.locator("#f-run-ref").inputValue()) === "refs/heads/broken"
    && (await page.locator("#f-run-ref-button").innerText()).includes("broken") && page.url().includes("/demo/runs"));

  // Copying, inline editing, times.
  await page.goto(`${base}/demo`);
  await page.click("button[aria-label='Copy HTTPS']");
  check("a copy button copies", (await page.evaluate(() => navigator.clipboard.readText())) === `${base}/demo.git`);
  check("hovering a time shows it in the reader's time zone", !/UTC$/.test(await page.locator("time").first().getAttribute("title")));
  await page.goto(`${base}/demo/settings`);
  await page.click("[data-inline-start][aria-label='Edit the description']");
  check("Edit turns the description into its form, focused", (await focused(page))?.id === "f-description");
  await page.keyboard.press("Escape");
  check("Escape turns it back", (await page.locator("[data-inline-start][aria-label='Edit the description']").evaluate((el) => el === document.activeElement)));
  await page.click("[data-inline-start][aria-label='Edit the description']");
  const original = await page.inputValue("#f-description");
  await page.fill("#f-description", "Edited in place");
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  check("an inline edit saves", (await page.locator(".inline-edit-value").first().innerText()) === "Edited in place"
    && /Saved/.test(await page.locator("[data-toast]").innerText()));
  await page.click("[data-inline-start][aria-label='Edit the description']");
  await page.fill("#f-description", original);
  await Promise.all([page.waitForURL(`${base}/demo/settings`), page.click("[data-inline-form] button[type=submit]")]);
  await context.close();
}

// ---- 3. Accessibility, both themes ----------------------------------
const screens = [
  ["home", "/", "#home-title"],
  ["repository", "/demo", "#repo-title"],
  ["repository-tags", "/demo?tags=all", "#repo-title"],
  ["files", "/demo@main", "#files-title"],
  ["directory", "/demo@main/internal/pay", "#files-title"],
  ["file", "/demo@main/internal/pay/charge.go", "#files-title"],
  ["commits", "/demo/commits", "#commits-title"],
  ["commits-path", "/demo/commits?ref=main&path=internal/pay/charge.go", "#commits-title"],
  ["activity", "/demo/activity", "#activity-title"],
  ["runs", "/demo/runs", "#runs-title"],
  ["commit", `/demo/commit/${commit}`, "#commit-title"],
  ["compare", `/demo/commits?base=main&ref=${encodeURIComponent(feature)}`, "#commits-title"],
  ["compare-files", `/demo/commits?base=main&ref=${encodeURIComponent(feature)}&tab=changes`, "#commits-title"],
  ["compare-diverged", "/demo/commits?base=broken&ref=main", "#commits-title"],
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
  ["commits-ref-picker", "/demo/commits", async (p) => { await p.click("#commits-ref-button"); await p.locator("dialog[open] input").fill("v1"); }],
  ["run-ref-picker", "/demo/runs?dialog=run-new", async (p) => { await p.click("#f-run-ref-button"); }],
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

// ---- The dashboards fit the window ---------------------------------------
{
  const { context, page } = await signedIn({ viewport: { width: 1440, height: 900 } });
  for (const path of ["/", "/demo", "/sms-gateway"]) {
    await page.goto(base + path);
    const fit = await page.evaluate(() => ({
      scroll: document.documentElement.scrollHeight, height: innerHeight,
      panels: [...document.querySelectorAll(".fit")].map((el) => Math.round(el.getBoundingClientRect().height)),
      shared: [...document.querySelectorAll(".fit:not(.fit-auto)")].map((el) => Math.round(el.getBoundingClientRect().height)),
      overflowing: [...document.querySelectorAll(".fit > .panel-scroll")].filter((el) => el.scrollWidth > el.clientWidth + 1)
        .map((el) => `${el.parentElement.getAttribute("aria-labelledby")} +${el.scrollWidth - el.clientWidth}px`),
    }));
    check(`the page does not scroll, only its panels: ${path}`, fit.scroll <= fit.height, JSON.stringify(fit));
    check(`every panel keeps a usable share of the window, empty or not: ${path}`, fit.panels.length >= 4 && fit.shared.every((h) => h >= 100), JSON.stringify(fit));
    check(`nothing inside a panel spills sideways: ${path}`, fit.overflowing.length === 0, JSON.stringify(fit));
  }
  await context.close();
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
  await page.goto(`${base}/demo/commits?path=internal/pay/charge.go`);
  check("without JavaScript the ref fields are text boxes", (await page.locator("button.ref-button").count()) === 0);
  await page.fill("#commits-ref", "broken");
  await Promise.all([page.waitForURL(/ref=broken/), page.click("form.commit-picker button:has-text('Show')")]);
  check("choosing a ref works without JavaScript, and keeps the path",
    query(page).get("ref") === "broken" && query(page).get("path") === "internal/pay/charge.go");
  await page.fill("#commits-base", "main");
  await Promise.all([page.waitForURL(/base=main/), page.click("form.commit-picker button:has-text('Show')")]);
  check("comparing two refs works without JavaScript", query(page).get("base") === "main" && (await page.locator(".compare-summary").count()) === 1);
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
