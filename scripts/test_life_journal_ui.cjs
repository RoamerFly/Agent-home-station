/* Offline Playwright regression. Start TestMemoryBrowserFixture, export its
 * local URL as WECHAT_ADMIN_UI_BASE, then run this script. */
'use strict';
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const base = process.env.WECHAT_ADMIN_UI_BASE;
assert(base && new URL(base).hostname === '127.0.0.1');
const empty = {
  episodes: [],
  plans: [],
  notes: []
},
      race = {
        episodes: [{id: 'race-episode', detail: '延迟记录', sources: []}],
        plans: [{id: 'race-plan', detail: '延迟约定', commitment_state: 'open', sources: []}],
        notes: [{id: 'race-note', note: '延迟心情'}]
      };
(async () => {
  const browser = await chromium.launch(
      {headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
  try {
    const context = await browser.newContext({viewport: {width: 1280, height: 820}}),
          login = await context.request.post(
              base + '/api/login',
              {headers: {Origin: base}, data: {password: 'synthetic-browser-password-2026'}});
    assert(login.ok());
    const accounts = await (await context.request.get(base + '/api/accounts')).json(),
          config = await (await context.request.get(base + '/api/config')).json(), primary = accounts.primary,
          second = 'synthetic-second-account';
    const page = await context.newPage(), errors = [], writes = [], requests = [];
    page.on('pageerror', e => errors.push(e.message));
    let releaseRace;
    const raceReleased = new Promise(r => releaseRace = r);
    await page.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url());
      if (url.origin !== base) return route.abort();
      if (!url.pathname.startsWith('/api/')) return route.continue();
      const account = req.headers()['x-wechat-account'] || url.searchParams.get('account') || primary;
      if (url.pathname === '/api/accounts' && req.method() === 'GET')
        return route.fulfill({
          json: {
            ...accounts,
            items: [
              ...accounts.items,
              {id: second, name: '合成第二账号', native: {connection: 'online'}, bot: {state: 'disabled'}}
            ]
          }
        });
      if (url.pathname === '/api/session') return route.continue();
      if (account === second) {
        if (url.pathname === '/api/config') return route.fulfill({json: config});
        if (url.pathname === '/api/status')
          return route.fulfill({
            json: {
              account: second,
              account_name: '合成第二账号',
              native: {authenticated: true, connection: 'online'},
              bot: {state: 'disabled'},
              key_configured: true
            }
          });
        if (url.pathname === '/api/companion/state') return route.fulfill({json: {scopes: []}});
        if (req.method() !== 'GET') {
          writes.push(url.pathname);
          return route.fulfill({status: 403, json: {error: 'offline regression blocks write'}})
        }
        return route.fulfill({json: {items: []}})
      }
      if (req.method() === 'POST' && /^\/api\/memory\/[^/]+\/entries\/[^/]+$/.test(url.pathname) &&
          req.postDataJSON()?.action === 'correct')
        return route.continue();
      if (req.method() !== 'GET' && url.pathname !== '/api/login') {
        writes.push(url.pathname);
        return route.fulfill({status: 403, json: {error: 'offline regression blocks write'}})
      }
      const m = url.pathname.match(/^\/api\/companion\/memory\/([^/]+)\/(experiences|reflections)$/),
            scope = m && decodeURIComponent(m[1]);
      if (!m || !['race-first', 'race-second'].includes(scope)) return route.continue();
      requests.push({scope, kind: m[2], view: url.searchParams.get('view') || ''});
      if (scope === 'race-first') await raceReleased;
      const body = scope === 'race-first' ? race : empty,
            items = m[2] === 'reflections' ?
          body.notes :
          (url.searchParams.get('view') === 'open_commitments' ? body.plans : body.episodes);
      return route.fulfill({json: {items, next_cursor: ''}});
    });
    await page.goto(base + '/#overview');
    const journal = page.locator('#life-journal');
    await journal.waitFor();
    await journal.locator('#life-journal-commitments')
        .getByText('周日一起画海边明信片', {exact: true})
        .waitFor();
    await journal.locator('#life-journal-experiences strong')
        .filter({hasText: '今天参加同事家的喜酒'})
        .waitFor();
    await journal.getByText('下次想继续看海边明信片的配色灵感', {exact: true}).waitFor();
    await journal.locator('.life-journal-feeling').filter({hasText: '看见深蓝的海面很放松'}).waitFor();
    const scope = await page.locator('#life-journal-scope').inputValue(),
          plans = await (await context.request.get(
                             base + '/api/companion/memory/' + encodeURIComponent(scope) +
                             '/experiences?view=open_commitments&limit=20'))
                      .json(),
          plan = plans.items.find(x => x.detail === '周日一起画海边明信片');
    assert(plan?.sources?.length);
    const memory = await (await context.request.get(
                              base + '/api/memory/' + encodeURIComponent(scope) +
                              '?query=' + encodeURIComponent('改到周一') + '&originals=true'))
                       .json(),
          source = memory.page.items.find(x => x.event?.text === '改到周一一起画海边明信片。')?.event;
    assert(source);
    const corrected = await page.evaluate(async v => {
      try {
        await window.WechatAdmin.api(
            '/api/memory/' + encodeURIComponent(v.scope) + '/entries/' + encodeURIComponent(v.id), {
              method: 'POST',
              json: {
                action: 'correct',
                expected_revision: v.version,
                detail: '周一一起画海边明信片',
                sources: [{event_id: v.eventID, quote: v.quote}]
              }
            });
        return ''
      } catch (e) {
        return e.message
      }
    }, {scope, id: plan.id, version: plan.version, eventID: source.id, quote: source.text});
    assert.equal(corrected, '');
    await page.locator('#sidebar-refresh').click();
    await journal.locator('#life-journal-commitments')
        .getByText('周一一起画海边明信片', {exact: true})
        .waitFor();
    await journal.locator('#life-journal-commitments .life-journal-quote')
        .filter({hasText: '改到周一一起画海边明信片。'})
        .waitFor();
    await journal.locator('#life-journal-commitments .life-journal-source').click();
    await page.locator('dialog[open]')
        .last()
        .locator('pre')
        .filter({hasText: '改到周一一起画海边明信片。'})
        .waitFor();
    await page.keyboard.press('Escape');
    const shots = process.env.WECHAT_ADMIN_UI_SCREENSHOTS;
    if (shots) {
      fs.mkdirSync(shots, {recursive: true});
      await page.screenshot({path: path.join(shots, 'life-journal-desktop-light.png'), fullPage: true})
    }
    await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
    await page.setViewportSize({width: 375, height: 812});
    assert(!(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2)));
    if (shots)
      await page.screenshot({path: path.join(shots, 'life-journal-narrow-dark.png'), fullPage: true});
    await page.setViewportSize({width: 1280, height: 820});
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('wechat-admin:scopes', {
      detail: {
        scopes: [
          {id: 'race-first', thread: 'race-first@chatroom', label: '合成竞态一'},
          {id: 'race-second', thread: 'race-second@chatroom', label: '合成竞态二'}
        ]
      }
    })));
    await journal.getByText('正在读取“合成竞态一”的生活手记…', {exact: true}).waitFor();
    const select = page.locator('#life-journal-scope');
    await select.selectOption('race-second');
    releaseRace();
    await journal.locator('#life-journal-commitments')
        .getByText('这段对话里暂时没有仍有效的约定。', {exact: true})
        .waitFor();
    await journal.locator('#life-journal-experiences')
        .getByText('这段对话里暂时没有可展示的共同经历。', {exact: true})
        .waitFor();
    await journal.locator('#life-journal-reflections')
        .getByText('她还没有在这段对话里记下兴趣或心情。', {exact: true})
        .waitFor();
    assert.equal(await journal.getByText('延迟约定', {exact: true}).count(), 0);
    assert(requests.some(x => x.scope === 'race-first') && requests.some(x => x.scope === 'race-second'));
    await page.locator('#account-select').selectOption(second);
    await journal.locator('#life-journal-experiences')
        .getByText('当前账号还没有可查看的会话。', {exact: true})
        .waitFor();
    assert.equal(new URL(page.url()).searchParams.get('account'), second);
    assert(
        !writes.some(p => p.includes('/api/companion/memory/') || p === '/api/tools/send'), writes.join(','));
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({
      offline: true,
      realFixtureSources: true,
      realCASCorrection: true,
      weddingEpisode: true,
      syntheticEmpty: true,
      accountSwitch: true,
      lateResponseIgnored: true,
      desktopLightAndNarrowDark: true,
      requests: requests.length,
      pageErrors: errors,
      realWechatSends: 0
    }));
  } finally {
    await browser.close()
  }
})().catch(e => {
  console.error(e);
  process.exitCode = 1
});
