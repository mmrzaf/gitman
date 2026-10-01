import { chromium } from 'playwright';
import { AxeBuilder } from '@axe-core/playwright';
import { mkdirSync } from 'node:fs';
const { BASE: base, PASSWORD: password, SHOTS: shots } = process.env;
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? {executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH} : {});
const errors = [];
const pages = ['/', '/demo', '/empty', '/demo/runs', '/demo/runs/1', '/demo/activity', '/demo@main', '/demo/commits', '/demo/settings', '/demo/settings?tab=rules', '/demo/settings?tab=secrets', '/demo/settings?tab=access', '/me', '/me?tab=password', '/people', '/workers', '/operations'];
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
   await page.setViewportSize({width:390,height:844});
   const overflow = await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);
   if (overflow) errors.push(`mobile overflow: ${path}`);
   await page.screenshot({path:`${shots}/${js?'js':'nojs'}-${theme}-${i}-mobile.png`,fullPage:true});
   await page.setViewportSize({width:1440,height:900});
  }
  if (js) {
   await page.goto(`${base}/me`);
   await page.locator('[data-tab=tokens]').focus(); await page.keyboard.press('ArrowRight');
   if (new URL(page.url()).searchParams.get('tab')!=='password') errors.push('keyboard tab navigation');
   await page.goto(`${base}/demo/settings?tab=secrets`);
   await page.locator('[data-dialog-open=secret-new]').click();
   if (!await page.locator('#secret-new').isVisible()) errors.push('secret dialog did not open');
   await page.keyboard.press('Escape');
   if (await page.locator('#secret-new').isVisible()) errors.push('Escape did not close dialog');
  }
  await context.close();
 }
}
await browser.close();
if (errors.length) throw Error(errors.join('\n'));
console.log(`PASS: ${pages.length} screens, both themes, mobile/desktop, JS/no-JS, axe and keyboard/dialog checks`);
