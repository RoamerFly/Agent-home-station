(() => {
  'use strict';
  const $ = s => document.querySelector(s), size = 12;
  const state = {
    open: false,
    scope: '',
    label: '',
    controller: null,
    request: 0,
    category: '',
    back: [],
    page: {},
    person: '',
    people: new Map(),
    items: [],
    cursors: {},
    hasMore: false,
    selected: ''
  };
  const text = x => typeof x === 'string' ? x.trim() : '';
  const root = () => $('#memory-palace'), graph = () => $('#memory-palace-graph'),
        list = () => $('#memory-palace-list'), detail = () => $('#memory-palace-detail'),
        status = () => $('#memory-palace-status');
  function make(parent, tag, value, cls) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (value !== undefined) e.textContent = value;
    parent.append(e);
    return e;
  }
  function empty(parent, message) {
    parent.replaceChildren();
    make(parent, 'p', message, 'empty');
  }
  function ids(item) {
    return [...(window.WechatPalaceGraph?.participants?.(item) || new Set())];
  }
  function kind(item) {
    return item.palaceType === 'relation'       ? '有来源的关系' :
        item.commitment_state === 'open'        ? '仍在续办' :
        item.note || item.feeling               ? '她的回想' :
        item.perspective === 'source_statement' ? '成员陈述' :
                                                  '共同经历';
  }
  function title(item) {
    return item.palaceType === 'relation' && window.WechatPalaceGraph?.title ?
        window.WechatPalaceGraph.title(item) :
        text(item.detail) || text(item.note) || text(item.feeling) || text(item.summary) ||
            '一段有来源的相处';
  }
  function sources(item) {
    return Array.isArray(item.sources) && item.sources.length ? item.sources :
        item.source?.event_id                                 ? [item.source] :
                                                                [];
  }
  function rememberPeople(item) {
    for (const id of ids(item)) {
      const source = sources(item).find(ref => ref.sender === id && ref.sender_name);
      const assertion = (item.assertions || []).find(a => a.subject === id || a.object === id);
      const label = source?.sender_name || (assertion && window.WechatMemory.relationParticipant(
        item, assertion, assertion.subject === id ? 'subject' : 'object'));
      if (label || !state.people.has(id)) state.people.set(id, label || '会话成员');
    }
  }
  function updatePeople() {
    const select = $('#memory-palace-person');
    select.replaceChildren(new Option('全部人物', ''));
    for (const [id, name] of state.people) select.append(new Option(name, id));
    select.value = state.person;
    select.disabled = !state.people.size;
  }
  function selectPerson(id) {
    state.person = id;
    if (id && state.category === 'reflections') state.category = '';
    $('#memory-palace-kind').value = state.category;
    clear(false);
    load();
  }
  function entry(parent, item, cls) {
    const people = ids(item), b = make(parent, 'button', undefined, cls);
    b.type = 'button';
    b.dataset.entryId = item.palaceKey;
    b.dataset.person = people[0] || '';
    b.dataset.personId = people[0] || '';
    b.setAttribute('aria-label', kind(item) + '：' + title(item));
    make(b, 'small', kind(item));
    make(b, 'strong', title(item));
    b.addEventListener('click', () => choose(item.palaceKey));
    return b;
  }
  function renderGraph() {
    const target = graph(), items = state.items;
    target.replaceChildren();
    if (!items.length)
      return empty(
          target, state.person ? '这个人物还没有可展示的来源记录。' : '这段对话还没有可展示的来源记录。');
    window.WechatPalaceGraph.render(target, items, {
      createEntry: entry, selectPerson, personLabel: id => state.people.get(id), personID: state.person
    });
  }
  function renderList() {
    const target = list(), items = state.items;
    target.replaceChildren();
    if (!items.length)
      return empty(
          target, state.person ? '这个人物还没有可展示的来源记录。' : '这段对话还没有可展示的来源记录。');
    items.forEach(item => entry(target, item, 'memory-palace-row'));
  }
  function choose(id) {
    const item = state.items.find(x => x.palaceKey === id);
    if (!item) return;
    state.selected = id;
    root().querySelectorAll('[data-entry-id]').forEach(e => {
      const yes = e.dataset.entryId === id;
      e.classList.toggle('is-selected', yes);
      e.setAttribute('aria-current', yes ? 'true' : 'false');
    });
    const target = detail();
    target.replaceChildren();
    make(target, 'p', kind(item), 'memory-palace-detail-kind');
    make(target, 'h3', title(item));
    if (text(item.summary) && text(item.summary) !== title(item)) make(target, 'p', text(item.summary));
    const version = [];
    if (item.updated) version.push('更新于 ' + window.WechatActivity.time(item.updated));
    version.push('第 ' + (item.version || item.revision) + ' 版');
    if (version.length) make(target, 'p', version.join(' · '), 'hint');
    if (item.note && item.feeling) make(target, 'p', '当时的心情：' + item.feeling);
    if (item.version > 1) {
      const history = make(target, 'button', '查看修订记录', 'quiet');
      history.type = 'button';
      history.onclick = async () => {
        const scope = state.scope, seq = state.request;
        try {
          const record = await window.WechatAdmin.api('/api/memory/' + encodeURIComponent(scope) + '/entries/' + encodeURIComponent(item.id));
          if (state.open && state.request === seq && state.selected === item.palaceKey)
            window.WechatMemory.showMemoryDetail('记忆来源与版本', record);
        } catch (error) {
          if (state.open && state.request === seq && state.selected === item.palaceKey) status().textContent = error.message;
        }
      };
    }
    const evidence = sources(item);
    if (!evidence.length) return make(target, 'p', '这条记录缺少可打开的原话。', 'hint');
    make(target, 'p', '依据', 'memory-palace-detail-label');
    evidence.forEach(source => {
      const quote = text(source.quote) || text(source.observation_excerpt),
            b = make(target, 'button', undefined, 'memory-palace-source');
      b.type = 'button';
      b.dataset.sourceId = source.event_id;
      const meta =
          [text(source.sender_name) || text(source.sender) ||
           (source.role === 'assistant' ? '大肥鱼' : '会话成员')];
      if (source.at) meta.push(window.WechatActivity.time(source.at));
      if (source.revision) meta.push('来源版本 ' + source.revision);
      make(b, 'small', meta.join(' · '));
      make(b, 'span', quote ? '“' + quote + '”' : '查看原始记录');
      b.addEventListener('click', () => window.WechatMemory?.source(state.scope, source.event_id));
    });
  }
  function render() {
    updatePeople();
    renderGraph();
    renderList();
    const selected = state.items.some(item => item.palaceKey === state.selected) ? state.selected : state.items[0]?.palaceKey;
    if (selected)
      choose(selected);
    else
      empty(detail(), '选择一个片段，查看它的来源。');
    $('#memory-palace-more').hidden = !state.hasMore;
    $('#memory-palace-prev').hidden = !state.back.length;
  }
  function mode(name) {
    const map = name === 'map';
    graph().parentElement.hidden = !map;
    list().hidden = map;
    $('#memory-palace-map-toggle').classList.toggle('is-active', map);
    $('#memory-palace-list-toggle').classList.toggle('is-active', !map);
    $('#memory-palace-map-toggle').setAttribute('aria-pressed', String(map));
    $('#memory-palace-list-toggle').setAttribute('aria-pressed', String(!map));
  }
  function url(view, cursor) {
    const q = new URLSearchParams({view, limit: String(size)});
    if (state.person && view !== 'reflections') q.set('person', state.person);
    if (cursor) q.set('cursor', cursor);
    return '/api/companion/memory/' + encodeURIComponent(state.scope) + '/' +
        (view === 'reflections' ? 'reflections' : 'experiences') + '?' + q;
  }
  function clear(resetPeople = true) {
    state.controller?.abort();
    state.request++;
    if (resetPeople) {
      state.person = '';
      state.people.clear();
    }
    state.selected = '';
    state.items = [];
    state.back = [];
    state.page = {};
    state.cursors = {};
    state.hasMore = false;
    render();
  }
  const types = {relations: 'relation', episodes: 'episode', open_commitments: 'commitment', reflections: 'reflection'};
  async function load(page = {}, back = [], canRetry = true) {
    if (!state.open || !state.scope) return;
    const req = ++state.request;
    state.controller?.abort();
    const controller = new AbortController();
    state.controller = controller;
    status().textContent = '正在读取当前会话的记忆…';
    const views = Object.keys(types).filter(view =>
      (!state.category || view === state.category) &&
      (!state.person || view !== 'reflections') &&
      (!Object.keys(page).length || page[view]));
    try {
      const results = await Promise.all(views.map(view =>
        window.WechatAdmin.api(url(view, page[view]), {signal: controller.signal})));
      if (req !== state.request) return;
      state.items = [];
      state.cursors = {};
      results.forEach((result, i) => {
        const view = views[i], type = types[view];
        for (const row of result.items || []) {
          const item = {...row, palaceType: type, palaceKey: type + ':' + row.id};
          state.items.push(item);
          rememberPeople(item);
        }
        if (result.next_cursor) state.cursors[view] = result.next_cursor;
      });
      state.back = back;
      state.page = page;
      state.hasMore = Object.keys(state.cursors).length > 0;
      render();
      status().textContent = '第 ' + (back.length + 1) + ' 页 · ' + state.items.length + ' 条记录';
    } catch (error) {
      if (controller.signal.aborted || req !== state.request) return;
      clear();
      if (error.status === 409 && canRetry) return load({}, [], false);
      status().textContent = '读取失败：' + error.message + '。可从导航栏刷新。';
    }
  }
  function receive(snapshot) {
    if (!snapshot) return;
    const changed = text(snapshot.scope) !== state.scope;
    state.scope = text(snapshot.scope);
    state.label = text(snapshot.label);
    if (!state.open) return;
    if (changed || snapshot.loading) {
      if (changed) window.WechatDialogs.closeAll();
      clear();
      if (snapshot.loading) status().textContent = '会话已切换，正在读取新的记忆宫殿…';
    }
    if (snapshot.ready && state.scope) load();
  }
  function open() {
    state.open = true;
    root().hidden = false;
    receive(window.WechatLifeJournal?.scope?.());
    if (!state.scope) render();
  }
  function close() {
    state.open = false;
    root().hidden = true;
    clear();
  }
  document.addEventListener('DOMContentLoaded', () => {
    $('#memory-palace-map-toggle').addEventListener('click', () => mode('map'));
    $('#memory-palace-list-toggle').addEventListener('click', () => mode('list'));
    $('#memory-palace-close').addEventListener('click', close);
    $('#memory-palace-person').addEventListener('change', e => selectPerson(e.target.value));
    $('#memory-palace-kind').addEventListener('change', e => {
      state.category = e.target.value;
      if (state.category === 'reflections') state.person = '';
      clear(false);
      load();
    });
    $('#memory-palace-prev').addEventListener('click', () => load(state.back.at(-1), state.back.slice(0,-1)));
    $('#memory-palace-more').addEventListener('click', () => load(state.cursors, [...state.back, state.page]));
    window.addEventListener('wechat-life-journal:data', e => receive(e.detail));
    window.addEventListener('wechat-life-journal:palace-open', open);
    window.addEventListener('wechat-admin:logout', close);
    window.addEventListener('wechat-admin:navigate', e => {
      if (e.detail?.section !== 'overview') close();
    });
    mode('map');
  });
})();
