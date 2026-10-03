import { chromium } from 'playwright';
import { AxeBuilder } from '@axe-core/playwright';
import { mkdirSync } from 'node:fs';
const { BASE: base, PASSWORD: password, SHOTS: shots } = process.env;
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? {executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH} : {});
const errors = [];
const pages = ['/', '/demo', '/empty', '/demo/runs', '/demo/runs/1', '/demo/activity', '/demo@main', '/demo/commits', '/demo/commits?ref=v1.1.0&base=v1.0.0&tab=changes', '/demo@main/docs', '/demo@main/README.md', '/demo/settings', '/demo/settings?tab=rules', '/demo/settings?tab=secrets', '/demo/settings?tab=access', '/me', '/me?tab=password', '/people', '/workers', '/operations'];
for (const js of [true, false]) {
 for (const theme of ['light', 'dark']) {
  const context = await browser.newContext({javaScriptEnabled: js, viewport:{width:1440,height:900},colorScheme:theme});
  const page = await context.newPage();
  page.on('pageerror', e => errors.push(String(e)));
  await page.goto(`${base}/login`);
  await page.fill('#f-username','darius'); await page.fill('#f-password',password);
  await Promise.all([page.waitForURL(`${base}/`),page.click('button[type=submit]')]);
  for (const [i,path] of pages.entries()) {
   const response = await page.goto(base+path);
   if (!response.ok()) throw Error(`${path}: ${response.status()}`);
   await page.locator('main').waitFor();
   if (js) {
    const result = await new AxeBuilder({page}).withTags(['wcag2a','wcag2aa','wcag21a','wcag21aa']).analyze();
    if (result.violations.length) errors.push(`${theme} ${path}: ${JSON.stringify(result.violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>n.target)})))}`);
   }
   await page.screenshot({path:`${shots}/${js?'js':'nojs'}-${theme}-${i}.png`,fullPage:true});
   for (const width of [320, 390, 768, 1024]) {
    await page.setViewportSize({width,height:844});
    const layoutErrors = await page.evaluate(() => {
     const problems = [];
     if (document.documentElement.scrollWidth > innerWidth + 1) {
      const overflowing = [...document.querySelectorAll('main *')].filter(el => {
       const box = el.getBoundingClientRect();
       return el.checkVisibility() && box.right > innerWidth + 1;
      }).slice(0, 6).map(el => `${el.tagName}.${el.className}`);
      problems.push(`page overflow: ${overflowing.join(', ')}`);
     }
     if (innerWidth <= 640) {
      for (const cell of document.querySelectorAll('.table-cards .cell-shrink')) {
       if (cell.getClientRects().length && cell.getBoundingClientRect().width < 80) problems.push('collapsed card column');
      }
      for (const cell of document.querySelectorAll('.table-cards .table-cell')) {
       if (!cell.getClientRects().length) continue;
       if (cell.scrollHeight > cell.clientHeight + 1) problems.push('clipped card content');
       const bounds = cell.getBoundingClientRect();
       for (const control of cell.querySelectorAll('button, .btn')) {
        if (!control.checkVisibility({visibilityProperty:true})) continue;
        const box = control.getBoundingClientRect();
        if (box.width && (box.top < bounds.top - 1 || box.bottom > bounds.bottom + 1
          || box.left < bounds.left - 1 || box.right > bounds.right + 1)) problems.push('clipped card control');
       }
      }
     }
     return problems;
    });
    for (const problem of layoutErrors) errors.push(`${width}px ${path}: ${problem}`);
    if (width === 390) {
     await page.screenshot({path:`${shots}/${js?'js':'nojs'}-${theme}-${i}-mobile.png`,fullPage:true});
    }
   }
   await page.setViewportSize({width:1440,height:900});
  }
  if (js) {
   await page.goto(`${base}/me`);
   await page.locator("summary[aria-label^='Account menu']").click();
   await page.locator('[data-theme-choice=dark]').focus();
   await page.keyboard.press('Enter');
   if (await page.locator('[data-theme-choice=dark]').getAttribute('role') !== 'menuitemradio'
       || await page.locator('[data-theme-choice=dark]').getAttribute('aria-checked') !== 'true'
       || await page.locator('[data-theme-choice=light]').getAttribute('aria-checked') !== 'false') errors.push('theme radio state');
   const menuAxe = await new AxeBuilder({page}).withTags(['wcag2a','wcag2aa','wcag21a','wcag21aa']).analyze();
   if (menuAxe.violations.length) errors.push(`open account menu: ${JSON.stringify(menuAxe.violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>n.target)})))}`);
   await page.locator('[data-theme-choice=system]').click();
   await page.keyboard.press('Escape');
   await page.locator('[data-tab=tokens]').focus(); await page.keyboard.press('ArrowRight');
   if (new URL(page.url()).searchParams.get('tab')!=='password') errors.push('keyboard tab navigation');
   await page.setViewportSize({width:320,height:568});
   await page.goto(`${base}/me`);
   await page.locator('[data-dialog-open=token-new]').click();
   const tokenDialog = page.locator('#token-new');
   if (await tokenDialog.evaluate(dialog => dialog.scrollHeight > dialog.clientHeight + 1)) errors.push('mobile token dialog overflows');
   await page.screenshot({path:`${shots}/js-${theme}-token-dialog-mobile.png`,fullPage:true});
   await Promise.all([page.waitForURL(`${base}/me`), page.keyboard.press('Escape')]);
   if (await tokenDialog.isVisible()) errors.push('mobile token dialog did not close');
   await page.goto(`${base}/demo/settings?tab=secrets`);
   await page.locator('[data-dialog-open=secret-new]').click();
   if (!await page.locator('#secret-new').isVisible()) errors.push('secret dialog did not open');
   const dialogOverflow = await page.locator('#secret-new').evaluate(dialog => dialog.scrollHeight > dialog.clientHeight + 1);
   if (dialogOverflow) errors.push('dialog content overflows its frame');
   await page.keyboard.press('Escape');
   if (await page.locator('#secret-new').isVisible()) errors.push('Escape did not close dialog');
  }
  await context.close();
 }
}
await browser.close();
if (errors.length) throw Error(errors.join('\n'));
console.log(`PASS: ${pages.length} screens, both themes, 320/390/768/1024/1440px, JS/no-JS, axe and keyboard/theme/dialog checks`);
