const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

test('permission form projects account layout without persisting its preview as grant roots', async () => {
  const nodes = new Map(), created = [], listeners = new Map(), posted = [];
  class Element {
    constructor(tag = 'div') { this.tagName = tag; this.value = ''; this.checked = false; this.dataset = {}; this.parentElement = {}; this.handlers = {}; this.children = []; }
    addEventListener(name, handler) { this.handlers[name] = handler; }
    append(...children) { this.children.push(...children); }
    replaceChildren(...children) { this.children = children; }
    setAttribute() {}
    checkValidity() { return true; }
    closest() { return null; }
    querySelector() { return null; }
  }
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, new Element());
    return nodes.get(id);
  };
  let selected = 'alpha';
  const settings = id => ({
    revision: `revision-${id}`, enabled: true, tasks_enabled: true, read_conversations: true,
    owner_ids: ['synthetic-owner'], privileged_threads: ['*'], account_actions: [],
    cross_conversation: true, external_owner_only: true,
    host: {mode: 'full', roots: [`/legacy-${id}`], timeout_seconds: 60, output_bytes: 16000},
  });
  const layout = id => ({private_workspace: `/managed/pri_workspace/${id}`, public_workspace: '/managed/public_workspace'});
  const document = {
    querySelector(selector) { assert.match(selector, /^#/); return node(selector.slice(1)); },
    querySelectorAll(selector) {
      if (selector === '[data-agent-action]') return created.filter(x => x.dataset.agentAction);
      if (selector === '[data-agent-action]:checked') return created.filter(x => x.dataset.agentAction && x.checked);
      if (selector.startsWith('[id^="agent-"]')) return [...nodes.values()];
      throw new Error(`Unexpected selector ${selector}`);
    },
    createElement(tag) { const result = new Element(tag); created.push(result); return result; },
  };
  for (const id of ['agent-cross-conversation', 'agent-external-owner-only', 'agent-owner-all-threads']) node(id).type = 'checkbox';
  const window = {
    addEventListener(name, handler) { listeners.set(name, handler); },
    WechatAdmin: {
      isAuthenticated: () => true,
      notice() {},
      async api(url, options) {
        if (url === '/api/agent') return {settings: settings(selected), workspace: layout(selected), tasks: [], operations: [], history_count: 0};
        if (url === '/api/agent/config') { posted.push(structuredClone(options.json)); return options.json; }
        throw new Error(`Unexpected API ${url}`);
      },
    },
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../internal/admin/static/agent.js'), 'utf8'), {window, document, Date, JSON});
  const load = async () => {
    listeners.get('wechat-admin:navigate')({detail: {section: 'permissions'}});
    await new Promise(resolve => setImmediate(resolve));
  };
  await load();
  assert.equal(node('agent-host-roots').value, '/managed/pri_workspace/alpha\n/managed/public_workspace');
  assert.equal(node('agent-host-roots').readOnly, true);
  // Even a tampered DOM preview must never become a grant in a save request.
  node('agent-host-roots').value = '/unrelated-root';
  node('agent-host-mode').value = 'files';
  await node('agent-save-permissions').handlers.click();
  assert.deepEqual(posted[0].host.roots, ['/legacy-alpha']);
  assert.equal(posted[0].host.mode, 'files');
  selected = 'beta';
  await load();
  assert.equal(node('agent-host-roots').value, '/managed/pri_workspace/beta\n/managed/public_workspace');
  await node('agent-save-permissions').handlers.click();
  assert.deepEqual(posted[1].host.roots, ['/legacy-beta']);
  listeners.get('wechat-admin:logout')();
  assert.equal(node('agent-host-roots').value, '');
});
