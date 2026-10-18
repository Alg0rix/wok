import { chromium } from 'playwright-core';
import assert from 'node:assert/strict';
import { performance } from 'node:perf_hooks';

export async function workload(page, kind, url) {
  if (kind === 'eval') return page.evaluate(() => document.title);
  if (kind === 'navigate') { await page.goto(url, { waitUntil: 'load' }); return page.title(); }
  await page.goto(url, { waitUntil: 'load' });
  await page.locator('#name').fill('wok benchmark');
  await page.locator('#go').click();
  const result = await page.locator('#result').textContent();
  assert.equal(result, 'wok benchmark');
  return result;
}

if (process.argv[1]?.endsWith('playwright-command.mjs')) {
  const [mode, endpoint, kind, url] = process.argv.slice(2);
  let browser;
  try {
    const started = performance.now();
    if (mode === 'cold') {
      browser = await chromium.launch({ executablePath: process.env.WOK_BENCH_BROWSER, headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
      const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
      // An eval has to operate on the fixture in all modes. This navigation is
      // part of the cold-start lifecycle and therefore remains inside timing.
      if (kind === 'eval') await page.goto(url, { waitUntil: 'load' });
      const value = await workload(page, kind, url);
      await browser.close(); browser = null;
      console.log(JSON.stringify({ value, nodeRssBytes: process.memoryUsage().rss, operationMs: performance.now() - started }));
    } else {
      browser = await chromium.connectOverCDP(endpoint);
      const page = browser.contexts()[0].pages()[0];
      const value = await workload(page, kind, url);
      // For an attached browser close disconnects the client. Do not close pages
      // or contexts owned by Wok. The runner verifies its endpoint is still alive.
      await browser.close(); browser = null;
      console.log(JSON.stringify({ value, nodeRssBytes: process.memoryUsage().rss, operationMs: performance.now() - started }));
    }
  } finally { if (browser) await browser.close(); }
}
