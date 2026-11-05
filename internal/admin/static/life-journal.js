(() => {
 'use strict';
 const $ = selector => document.querySelector(selector);
 const state = {scopes: [], selected: '', controller: null, request: 0, loaded: false, navigated: false, loadPromise: null, palaceScope: {scope: '', label: '', ready: false}};
 const text = value => typeof value === 'string' ? value.trim() : '';
 const label = scope => text(scope.thread_name) || text(scope.label) || text(scope.thread) || text(scope.id) || '未命名会话';
 const time = value => window.WechatActivity?.time(value) || text(value);
 const active = () => state.scopes.find(scope => scope.id === state.selected);
 const scopeSelect = () => $('#life-journal-scope');
 const experiences = () => $('#life-journal-experiences');
 const commitments = () => $('#life-journal-commitments');
 const reflections = () => $('#life-journal-reflections');
 const status = () => $('#life-journal-status');
 const palaceOpen = () => $('#memory-palace-open');

 function publish(detail) {
  state.palaceScope = detail;
  window.dispatchEvent(new CustomEvent('wechat-life-journal:data', {detail}));
 }

 function node(parent, tag, value, className) {
  const child = document.createElement(tag);
  if (className) child.className = className;
  if (value !== undefined) child.textContent = value;
  parent.append(child);
  return child;
 }
 function empty(parent, message) { parent.replaceChildren(); node(parent, 'p', message, 'empty'); }
 function renderScopes() {
  const select = scopeSelect(), previous = state.selected;
  select.replaceChildren();
  if (!state.scopes.length) {
   select.append(new Option('暂无可查看的会话', ''));
   select.disabled = true;
   $('#life-journal-open').disabled = true;
   palaceOpen().disabled = true;
   state.selected = '';
   return previous !== state.selected;
  }
  for (const scope of state.scopes) select.append(new Option(label(scope), scope.id));
  if (!state.scopes.some(scope => scope.id === state.selected)) state.selected = state.scopes[0].id;
  select.value = state.selected;
  select.disabled = false;
  $('#life-journal-open').disabled = false;
  palaceOpen().disabled = false;
  return previous !== state.selected;
 }
 function renderCommitments(items) {
  const parent = commitments(); parent.replaceChildren();
  if (!items.length) return empty(parent, '这段对话里暂时没有仍有效的约定。');
  for (const item of items) {
   const row = node(parent, 'article', undefined, 'life-journal-entry');
   row.dataset.entryId = text(item.id);
   node(row, 'strong', text(item.detail) || '未命名约定');
   const meta = [];
   if (item.commitment_state === 'open') meta.push('仍待续办');
   if (item.updated) meta.push(time(item.updated));
   else if (item.created) meta.push(time(item.created));
   node(row, 'small', meta.join(' · ') || '有来源的共同经历', 'hint');
   sourceButton(row, item);
  }
 }
 function sourceButton(row, item) {
  const sources = Array.isArray(item.sources) ? item.sources : [];
  // A corrected agreement retains its earlier source. Its final cited source
  // is the current agreement, so prefer that rather than an obsolete plan.
  const source = item.commitment_state && sources.length ? sources[sources.length - 1] : sources.find(ref => text(ref.event_id));
  if (!source) return;
  const who = text(source.sender_name) || (source.role === 'admin_correction' ? '管理员更正' : source.role === 'assistant' ? '大肥鱼' : text(source.sender) || '会话成员');
  const sourceMeta = [who, source.at ? time(source.at) : ''].filter(Boolean).join(' · ');
  node(row, 'small', '依据：'+sourceMeta, 'hint life-journal-evidence');
  if (text(source.quote)) node(row, 'p', '“'+text(source.quote)+'”', 'life-journal-quote');
  const button = node(row, 'button', '查看依据', 'life-journal-source');
  button.type = 'button';
  button.dataset.sourceId = source.event_id;
  button.addEventListener('click', () => window.WechatMemory?.source(state.selected, source.event_id, source.revision));
 }
 function renderExperiences(items) {
  const parent = experiences(); parent.replaceChildren();
  if (!items.length) return empty(parent, '这段对话里暂时没有可展示的共同经历。');
  for (const item of items) {
   const row = node(parent, 'article', undefined, 'life-journal-entry');
   row.dataset.entryId = text(item.id);
   node(row, 'strong', text(item.detail) || '未命名的共同经历');
   const meta = [];
   meta.push(item.perspective === 'source_statement' ? '成员陈述' : item.perspective === 'reported_statement' ? '转述' : '共同经历');
   if (item.updated) meta.push(time(item.updated));
   node(row, 'small', meta.join(' · '), 'hint');
   sourceButton(row, item);
  }
 }
 function renderReflections(items) {
  const parent = reflections(); parent.replaceChildren();
  if (!items.length) return empty(parent, '她还没有在这段对话里记下兴趣或心情。');
  for (const item of items) {
   const row = node(parent, 'article', undefined, 'life-journal-entry');
   row.dataset.entryId = text(item.id);
   const content = text(item.note) || text(item.feeling);
   node(row, 'strong', content || '一条未命名的手记');
   if (item.note && item.feeling) node(row, 'p', '当时的心情：'+text(item.feeling), 'life-journal-feeling');
   const detail = [];
   if (item.note && item.feeling) detail.push('兴趣与心情');
   else if (item.note) detail.push('兴趣');
   else if (item.feeling) detail.push('当时的心情');
   if (item.updated) detail.push(time(item.updated));
   node(row, 'small', detail.join(' · ') || '有来源的手记', 'hint');
   const source = item.source;
   if (source?.event_id) {
    const who = text(source.sender_name) || text(source.sender) || '会话成员';
    node(row, 'small', '素材来自'+[who, source.at ? time(source.at) : ''].filter(Boolean).join(' · '), 'hint life-journal-evidence');
    if (text(source.observation_excerpt)) node(row, 'p', source.observation_excerpt, 'life-journal-quote');
    const button = node(row, 'button', '查看观察依据', 'life-journal-source');
    button.type = 'button'; button.dataset.sourceId = source.event_id;
    button.addEventListener('click', () => window.WechatMemory?.source(state.selected, source.event_id, source.revision));
   }
  }
 }
 function loading() {
  empty(experiences(), '正在读取共同经历…');
  empty(commitments(), '正在读取未完约定…');
  empty(reflections(), '正在读取兴趣与心情…');
 }
 async function runLoad() {
  const scope = active(), request = ++state.request;
  state.controller?.abort();
  if (!scope) {
   empty(experiences(), '当前账号还没有可查看的会话。');
   empty(commitments(), '当前账号还没有可查看的会话。');
   empty(reflections(), '请先在会话中形成有来源的记录。');
   status().textContent = '生活手记只读取当前账号中的会话，不会合并其他账号资料。';
   publish({scope: '', label: '', ready: true});
   return;
  }
  const controller = new AbortController(); state.controller = controller;
  loading(); status().textContent = '正在读取“'+label(scope)+'”的生活手记…';
  // The palace receives only scope lifecycle information. It owns its own
  // paginated reads and never keeps the overview's short preview in memory.
  publish({scope: scope.id, label: label(scope), loading: true, ready: false});
  const base = '/api/companion/memory/'+encodeURIComponent(scope.id);
  try {
   const [feed, plans, notes] = await Promise.all([
    window.WechatAdmin.api(base+'/experiences?'+new URLSearchParams({view: 'episodes', limit: '3'}), {signal: controller.signal}),
    window.WechatAdmin.api(base+'/experiences?'+new URLSearchParams({view: 'open_commitments', limit: '3'}), {signal: controller.signal}),
    window.WechatAdmin.api(base+'/reflections?'+new URLSearchParams({limit: '3'}), {signal: controller.signal})
   ]);
   if (controller.signal.aborted || request !== state.request || scope.id !== state.selected) return;
   renderExperiences(Array.isArray(feed.items) ? feed.items : []);
   renderCommitments(Array.isArray(plans.items) ? plans.items : []);
   renderReflections(Array.isArray(notes.items) ? notes.items : []);
   publish({scope: scope.id, label: label(scope), ready: true});
   status().textContent = '已更新。可在会话手记中查看完整来源、版本并更正或遗忘。';
  } catch (error) {
   if (controller.signal.aborted || request !== state.request) return;
   empty(commitments(), '未能读取未完约定。');
   empty(experiences(), '未能读取共同经历。');
   empty(reflections(), '未能读取兴趣与心情。');
   status().textContent = '读取失败：'+(error.message || '请稍后重试')+'。';
   publish({scope: scope.id, label: label(scope), error: true, ready: true});
  }
 }
 function load() {
  const request = runLoad();
  state.loadPromise = request;
  return request;
 }
 function receiveScopes(items) {
  if (!window.WechatAdmin?.isAuthenticated()) return;
  state.scopes = Array.isArray(items) ? items.filter(item => item && text(item.id)) : [];
  const changed = renderScopes();
  if ((changed || !state.loaded) && state.navigated) load();
  state.loaded = true;
 }
 function openMemory() {
  if (!active()) return;
  window.WechatMemory?.open(state.selected, () => load());
 }
 function openPalace() {
  const palace = $('#memory-palace');
  palace.hidden = false;
  $('#overview').scrollTop = 0;
  window.dispatchEvent(new CustomEvent('wechat-life-journal:palace-open'));
 }
 async function refresh() {
  // Refresh the authorized scope list first: a conversation that was empty
  // can acquire its first sourced record after the overview was opened.
  const before = state.request;
  await window.WechatAdmin?.refreshScopes?.();
  if (state.request === before && state.loaded && state.navigated) return load();
  return state.loadPromise;
 }
 document.addEventListener('DOMContentLoaded', () => {
  window.WechatLifeJournal = {scope: () => state.palaceScope, reload: load};
  scopeSelect().addEventListener('change', event => { state.selected = event.target.value; load(); });
  $('#life-journal-open').addEventListener('click', openMemory);
  palaceOpen().addEventListener('click', openPalace);
  window.WechatRefresh?.register($('#life-journal'), refresh);
  window.addEventListener('wechat-admin:scopes', event => receiveScopes(event.detail?.scopes));
  window.addEventListener('wechat-admin:navigate', event => {
   state.navigated = event.detail?.section === 'overview';
   if (event.detail?.section === 'overview' && state.loaded) load();
  });
  window.addEventListener('wechat-admin:logout', () => {
   state.controller?.abort(); state.scopes = []; state.selected = ''; state.loaded = false; state.navigated = false; state.request++;
   renderScopes(); empty(experiences(), '登录后读取生活手记。'); empty(commitments(), '登录后读取生活手记。'); empty(reflections(), '登录后读取生活手记。'); status().textContent = '';
   palaceOpen().disabled = true;
   publish({scope: '', label: '', ready: true});
  });
 });
})();
