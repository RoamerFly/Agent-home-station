'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const {pathToFileURL} = require('node:url');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

// This visual sample must also work when opened from a checkout without a web server.
const entry = pathToFileURL(path.join(__dirname, '../docs/design/palace-preview/index.html')).href;
const shots = process.env.PALACE_PREVIEW_SCREENSHOTS;

async function capture(page, name) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
  assert.equal(overflow, false, name + ' exceeds the viewport');
  if (shots) await page.screenshot({path: path.join(shots, name + '.png'), fullPage: true});
}

async function checkSize(browser, width, height) {
  const page = await browser.newPage({viewport: {width, height}, reducedMotion: 'reduce'});
  try {
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('requestfailed', req => errors.push(req.url()));
    await page.route(/^https?:/, route => {
      errors.push('Remote dependency: ' + new URL(route.request().url()).origin);
      return route.abort();
    });
    await page.goto(entry);
    const home = page.locator('[data-view="journal"]');
    const palace = page.locator('[data-view="palace"]');
    const evidence = page.locator('#source-stack');
    let originalEvidence;
    for (const scene of ['night-sea', 'tide-atlas']) {
      if (scene === 'tide-atlas') await page.locator('.scene-button').click();
      await page.locator('.mode-button[data-mode="journal"]').click();
      assert(await home.isVisible() && !await palace.isVisible());
      const sceneImage = home.locator('.hero-image img');
      await sceneImage.evaluate(image => image.decode());
      assert((await sceneImage.getAttribute('src')).endsWith(scene + '.webp'));
      await capture(page, scene + '-home-' + width);
      await home.locator('[data-node="revision"]').click();
      assert(await palace.isVisible() && !await home.isVisible());
      assert.match(await evidence.innerText(), /周日/);
      await capture(page, scene + '-palace-' + width);
      const records = {};
      for (const node of await page.locator('.map-node').all()) {
        const id = await node.getAttribute('data-node');
        await node.scrollIntoViewIfNeeded();
        const centerIsReachable = await node.evaluate(n => {
          const dot = n.querySelector('circle').getBoundingClientRect();
          const hit = document.elementFromPoint(dot.x + dot.width / 2, dot.y + dot.height / 2);
          return hit && n.contains(hit);
        });
        assert(centerIsReachable, scene + ': unreachable map node ' + id + ' at ' + width);
        await node.focus();
        await page.keyboard.press('Enter');
        assert(await node.evaluate(n => n.classList.contains('selected')));
        records[id] = await evidence.innerText();
        assert(records[id].trim(), id + ' has no source');
      }
      assert.notEqual(records.plan, records.revision);
      assert.notEqual(records.wave, records.interest);
      if (originalEvidence) assert.deepEqual(records, originalEvidence);
      originalEvidence = records;
      await page.locator('.list-toggle').click();
      const listNodes = page.locator('#source-list button[data-list-node]');
      assert.equal(await listNodes.count(), Object.keys(records).length);
      for (const button of await listNodes.all()) {
        await button.click();
        assert.equal(await evidence.innerText(), records[await button.getAttribute('data-list-node')]);
      }
      await page.locator('.list-toggle').click();
    }
    assert.deepEqual(errors, []);
    console.log(width + '×' + height + ': both layouts, local art, graph, list and source checks passed');
  } finally {
    await page.close();
  }
}

(async () => {
  if (shots) await fs.mkdir(shots, {recursive: true});
  const browser = await chromium.launch({headless: true, channel: process.env.PLAYWRIGHT_CHANNEL || undefined});
  try {
    for (const [width, height] of [[1280, 800], [820, 900], [375, 812]]) await checkSize(browser, width, height);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
