/* Offline browser check for the production memory-palace entry point.
 * Start TestMemoryBrowserFixture, set WECHAT_ADMIN_UI_BASE, then run this file. */
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const base = process.env.WECHAT_ADMIN_UI_BASE;
assert(base && new URL(base).hostname === '127.0.0.1');

async function sourceText(page) {
  return page.locator('dialog[open]').last().locator('pre').innerText();
}

function syntheticPage(scope, endpoint, view, cursor) {
  const second = scope === 'palace-race-new';
  const record = {
    id: second ? 'new-episode' : 'old-episode',
    detail: second ? '新的会话记录不能被旧响应覆盖' : '过期响应不应显示',
    summary: second ? '新的会话记录不能被旧响应覆盖' : '过期响应不应显示',
    version: 1,
    sources: [],
    assertions: []
  };
  if (second && view === 'episodes') {
    return cursor
      ? {items: [{...record, id: 'next-episode', detail: '另一页的经历'}], has_more: false}
      : {items: [record], has_more: true, next_cursor: 'synthetic-continuation'};
  }
  return {
    items: endpoint === 'experiences' && view === 'episodes' ? [record] : [],
    next_cursor: '',
    has_more: false
  };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})
  });
  try {
    const context = await browser.newContext({reducedMotion: 'reduce', viewport: {width: 1280, height: 820}});
    const login = await context.request.post(base + '/api/login', {
      headers: {Origin: base},
      data: {password: 'synthetic-browser-password-2026'}
    });
    assert(login.ok());

    const page = await context.newPage();
    const errors = [];
    const writes = [];
    const palaceScopes = [];
    let releaseOldScope;
    const oldScopeCanFinish = new Promise(resolve => { releaseOldScope = resolve; });
    let oldScopeStarted;
    const oldScopeRequested = new Promise(resolve => { oldScopeStarted = resolve; });
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== base) return route.abort();
      if (url.pathname === '/api/billing/balance') return route.fulfill({json: {}});
      const palaceRequest = url.pathname.match(/^\/api\/companion\/memory\/([^/]+)\/(experiences|reflections)$/);
      if (palaceRequest) palaceScopes.push(decodeURIComponent(palaceRequest[1]));
      const syntheticMatch = url.pathname.match(/^\/api\/companion\/memory\/(palace-race-old|palace-race-new)\/(experiences|reflections)$/);
      if (syntheticMatch) {
        const [, syntheticScope, endpoint] = syntheticMatch;
        if (syntheticScope === 'palace-race-old') {
          oldScopeStarted();
          await oldScopeCanFinish;
        }
        return route.fulfill({json: syntheticPage(syntheticScope, endpoint, url.searchParams.get('view'), url.searchParams.get('cursor'))});
      }
      if (request.method() === 'POST' && /^\/api\/memory\/[^/]+\/entries\/[^/]+$/.test(url.pathname) &&
          request.postDataJSON()?.action === 'correct') return route.continue();
      if (request.method() !== 'GET' && url.pathname !== '/api/login') {
        writes.push(url.pathname);
        return route.fulfill({status: 403, json: {error: 'offline regression blocks writes'}});
      }
      return route.continue();
    });

    await page.goto(base + '/#overview');
    const journal = page.locator('#life-journal');
    await journal.waitFor();
    await journal.getByText('周日一起画海边明信片', {exact: true}).waitFor();

    const scope = await page.locator('#life-journal-scope').inputValue();
    const commitments = await (await context.request.get(
        base + '/api/companion/memory/' + encodeURIComponent(scope) +
        '/experiences?view=open_commitments&limit=20')).json();
    const plan = commitments.items.find(item => item.detail === '周日一起画海边明信片');
    assert(plan?.sources?.length);
    const memory = await (await context.request.get(
        base + '/api/memory/' + encodeURIComponent(scope) + '?query=' +
        encodeURIComponent('改到周一') + '&originals=true')).json();
    const source = memory.page.items.find(item => item.event?.text === '改到周一一起画海边明信片。')?.event;
    assert(source);
    const correction = await page.evaluate(async values => {
      try {
        await window.WechatAdmin.api(
            '/api/memory/' + encodeURIComponent(values.scope) + '/entries/' + encodeURIComponent(values.id), {
              method: 'POST',
              json: {
                action: 'correct',
                expected_revision: values.version,
                detail: '周一一起画海边明信片',
                sources: [{event_id: values.eventID, quote: values.quote}]
              }
            });
        return '';
      } catch (error) {
        return error.message;
      }
    }, {scope, id: plan.id, version: plan.version, eventID: source.id, quote: source.text});
    assert.equal(correction, '');
    await page.locator('#sidebar-refresh').click();
    await journal.getByText('周一一起画海边明信片', {exact: true}).waitFor();
    await page.locator('#memory-palace-open').click();

    const palace = page.locator('#memory-palace');
    await palace.waitFor({state: 'visible'});
    const graphItems = palace.locator('#memory-palace-graph [data-entry-id]');
    await graphItems.first().waitFor();
    const graphIDs = await graphItems.evaluateAll(nodes => nodes.map(node => node.dataset.entryId));
    assert(graphIDs.length > 0);
    const childEdges = palace.locator('svg path[data-person="relative:lin:child"]');
    assert.equal(await childEdges.count(), 1);
    assert.match(await childEdges.getAttribute('data-record'), /^relation:/);

    assert(new Set(graphIDs).size === graphIDs.length);
    const commitmentID = await journal.locator('#life-journal-commitments .life-journal-entry').first().getAttribute('data-entry-id');
    const commitmentKey = 'commitment:' + commitmentID;
    assert(graphIDs.includes(commitmentKey));
    await palace.locator('#memory-palace-graph [data-entry-id="' + commitmentKey + '"]').click();
    const correctedDetail = await palace.locator('#memory-palace-detail').innerText();
    assert.match(correctedDetail, /周一一起画海边明信片/);
    assert.match(correctedDetail, /第 2 版/);
    const correctedSource = palace.locator('#memory-palace-detail .memory-palace-source').filter({hasText: '改到周一一起画海边明信片。'});
    await correctedSource.click();
    assert.match(await sourceText(page), /改到周一一起画海边明信片。/);
    await page.keyboard.press('Escape');
    await palace.locator('#memory-palace-detail').getByText('查看修订记录', {exact: true}).click();
    assert.match(await page.locator('dialog[open]').last().innerText(), /周日一起画海边明信片/);
    assert.match(await page.locator('dialog[open]').last().innerText(), /周一一起画海边明信片/);
    await page.keyboard.press('Escape');

    await palace.locator('#memory-palace-list-toggle').click();
    const listItems = palace.locator('#memory-palace-list [data-entry-id]');
    await listItems.first().waitFor();
    const listIDs = await listItems.evaluateAll(nodes => nodes.map(node => node.dataset.entryId));
    assert.deepEqual([...new Set(listIDs)].sort(), [...new Set(graphIDs)].sort());

    const relation = listItems.filter({hasText: '有来源的关系'}).first();
    await relation.click();
    const relationSource = palace.locator('#memory-palace-detail .memory-palace-source').first();
    await relationSource.waitFor();
    await relationSource.click();
    assert.match(await sourceText(page), /我儿子/);
    await page.keyboard.press('Escape');

    await palace.locator('#memory-palace-map-toggle').click();
    assert(await palace.locator('#memory-palace-graph-wrap').isVisible());
    assert.equal(await palace.locator('#memory-palace-list').isVisible(), false);
    const shots = process.env.WECHAT_ADMIN_UI_SCREENSHOTS;
    if (shots) {
      fs.mkdirSync(shots, {recursive: true});
      await page.locator('#overview').evaluate(el => { el.scrollTop = 0; });
      await page.screenshot({path: path.join(shots, 'memory-palace-desktop-light.png')});
    }
    await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
    await page.setViewportSize({width: 375, height: 812});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2), false);
    if (shots) {
      await page.locator('#overview').evaluate(el => { el.scrollTop = 0; });
      await page.screenshot({path: path.join(shots, 'memory-palace-narrow-dark.png')});
    }
    await page.setViewportSize({width: 1280, height: 820});

    const person = palace.locator('#memory-palace-person');
    await person.waitFor();
    const options = await person.locator('option').evaluateAll(items => items.map(option => option.value));
    assert(options.some(value => value));
    const relationKey = await relation.getAttribute('data-entry-id');
    const relationPerson = await palace.locator('#memory-palace-graph [data-entry-id="' + relationKey + '"]').getAttribute('data-person-id');
    assert(relationPerson);
    const personNode = palace.locator('.palace-chart-person[data-person-id="' + relationPerson + '"]');
    await personNode.click();
    await page.waitForFunction(id => document.querySelector('#memory-palace-person').value === id, relationPerson);
    await palace.locator('#memory-palace-status').getByText(/第 1 页/).waitFor();
    assert.equal(await person.inputValue(), relationPerson);
    assert.match(await palace.locator('#memory-palace-detail').innerText(), /有来源的关系|共同经历/);

    // A late response for the old scope must never replace the newer scope's palace.
    const dispatchScope = (scope, label, loading, ready) => page.evaluate(detail => {
      window.dispatchEvent(new CustomEvent('wechat-life-journal:data', {detail}));
    }, {scope, label, loading, ready});
    await dispatchScope('palace-race-old', '延迟的旧会话', true, false);
    await dispatchScope('palace-race-old', '延迟的旧会话', false, true);
    await oldScopeRequested;
    await dispatchScope('palace-race-new', '新的会话', true, false);
    await dispatchScope('palace-race-new', '新的会话', false, true);
    releaseOldScope();
    await palace.locator('#memory-palace-graph').getByText('新的会话记录不能被旧响应覆盖', {exact: true}).waitFor();
    assert.equal(await palace.locator('#memory-palace-graph').getByText('过期响应不应显示', {exact: true}).count(), 0);

    await page.locator('#memory-palace-more').click();
    await palace.locator('#memory-palace-graph button').filter({hasText: '另一页的经历'}).waitFor();
    assert.equal(await page.locator('#memory-palace-more').isVisible(), false);
    await page.locator('#memory-palace-prev').click();
    await palace.locator('#memory-palace-graph button').filter({hasText: '新的会话记录不能被旧响应覆盖'}).waitFor();
    assert.equal(await page.locator('#memory-palace-prev').isVisible(), false);
    await page.locator('#memory-palace-close').click();
    assert.equal(await palace.isHidden(), true);

    // The conversation entry must use that conversation's exact scope, then
    // return to the same selected transcript and reading position.
    await page.locator('.main-nav[data-section="memory"]').click();
    const conversation = page.locator('.conversation-open').first();
    await conversation.waitFor();
    await conversation.click();
    const conversationTitle = await page.locator('#conversation-title').innerText();
    await page.locator('#memory-content').locator('.conversation-message').first().waitFor();
    const currentScope = await page.locator('.conversation-row[data-selected="true"]').getAttribute('data-scope-id');
    assert.equal(currentScope, scope);
    await page.locator('#memory-content').evaluate(el => {
      el.style.height = '90px';
      el.style.maxHeight = '90px';
      el.style.overflowY = 'auto';
      el.scrollTop = 60;
    });
    const feedBeforePalace = await page.locator('#memory-content').evaluate(el => el.scrollTop);
    assert(feedBeforePalace > 0);
    palaceScopes.length = 0;
    await page.locator('#scope-palace-open').click();
    await palace.waitFor({state: 'visible'});
    await palace.locator('#memory-palace-graph [data-entry-id]').first().waitFor();
    await palace.locator('#memory-palace-status').getByText(/第 1 页/).waitFor();
    assert.equal(await page.locator('#memory-palace-title').innerText(), conversationTitle + ' · 记忆宫殿');
    assert(palaceScopes.length > 0 && palaceScopes.every(id => id === currentScope));
    const actualGraphNode = await palace.locator('#memory-palace-graph [data-entry-id]').first().getAttribute('data-entry-id');
    if (shots) {
      await page.evaluate(() => document.documentElement.dataset.theme = 'light');
      await page.setViewportSize({width: 1280, height: 820});
      await page.screenshot({path: path.join(shots, 'memory-palace-conversation-light.png')});
      await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
      await page.setViewportSize({width: 375, height: 812});
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 2), false);
      await page.screenshot({path: path.join(shots, 'memory-palace-conversation-narrow-dark.png')});
      await page.setViewportSize({width: 1280, height: 820});
    }

    // An overview update belongs to its own selected scope and cannot replace
    // a palace that was opened from the conversation reader.
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('wechat-life-journal:data', {
      detail: {scope: 'palace-race-new', label: '不应进入当前会话', loading: false, ready: true}
    })));
    assert.equal(await page.locator('#memory-palace-title').innerText(), conversationTitle + ' · 记忆宫殿');
    assert.equal(await palace.locator('#memory-palace-graph').getByText('新的会话记录不能被旧响应覆盖', {exact: true}).count(), 0);
    assert.equal(await palace.locator('#memory-palace-graph [data-entry-id="' + actualGraphNode + '"]').count(), 1);
    await page.locator('#memory-palace-close').click();
    assert.equal(await palace.isHidden(), true);
    assert.equal(await page.locator('#conversation-title').innerText(), conversationTitle);
    assert.equal(await page.locator('.conversation-row[data-selected="true"]').getAttribute('data-scope-id'), currentScope);
    assert.equal(await page.locator('#memory-content').evaluate(el => el.scrollTop), feedBeforePalace);
    assert.equal(await page.locator('#memory-palace-close').innerText(), '收起');
    assert.deepEqual(writes, []);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({
      offline: true,
      sourcedRelation: true,
      graphListSameIDs: true,
      typedNodeKeysAndCorrectedHistory: true,
      personGraphFilter: true,
      sourceReader: true,
      staleScopeResponseIsolation: true,
      forwardAndBackwardPagination: true,
      desktopLightAndNarrowDark: true,
      conversationEntryKeepsScopeAndReadingPosition: true,
      realWechatSends: 0
    }));
  } finally {
    await browser.close();
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
