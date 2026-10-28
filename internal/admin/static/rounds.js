/* Pure projection: retain raw evidence and join only explicit identities. */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.WechatRounds = api;
})(typeof window === 'object' ? window : globalThis, function () {
  'use strict';
  const stage = item => item.category === 'received' ? 'receive'
    : item.tool || item.category === 'tool' ? 'tool'
    : item.category === 'sent' || /^(media_|delivery_|send_|manual_send|message_progress)/.test(item.event) ? 'delivery'
    : item.category === 'decision' || /^(generation_|model_|character_|utterance_)/.test(item.event) ? 'decision' : 'system';
  const identity = item => item.round_id || item.message_id;
  const messageKey = (item, id = identity(item)) => id && item.thread_id ? JSON.stringify([item.thread_id, id]) : '';
  function activityTarget(item) {
    try {
      const url = new URL(item.target_url);
      if (!['https:', 'http:'].includes(url.protocol)) return null;
      if (url.hostname === 'api.deepseek.com' && /^\/(?:v1\/)?tools\/(search|fetch)\/?$/.test(url.pathname)) {
        return { kind: 'service', label: url.pathname.includes('/search') ? 'DeepSeek 搜索服务' : 'DeepSeek 网页读取服务', detail: '由后台携带配置的 Key 调用；这是服务地址，实际内容见执行结果。' };
      }
      return { kind: 'page', label: url.hostname + ' · ' + url.pathname, href: url.href };
    } catch { return null; }
  }
  function project(items) {
    const entries = [...items].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
    const parents = new Map(), sends = new Map(), groups = new Map();
    const find = key => {
      const seen = new Set();
      while (parents.has(key) && !seen.has(key)) { seen.add(key); key = parents.get(key); }
      return key;
    };
    // related_message_id can also mean a quote or cancellation evidence. Only
    // explicit session-followup events establish ownership of another message.
    for (const item of entries) {
      if (['generation_followup_consumed', 'generation_followup_settled', 'backlog_message_grouped'].includes(item.event) && item.related_message_id) {
        const child = messageKey(item), parent = messageKey(item, item.related_message_id);
        if (child && parent && find(parent) !== child) parents.set(child, parent);
      }
    }
    for (const item of entries) {
      const key = messageKey(item);
      if (key && item.send_id) {
        const previous = sends.get(item.send_id), owner = find(key);
        sends.set(item.send_id, sends.has(item.send_id) && previous !== owner ? null : owner);
      }
    }
    let systemSession = '';
    for (const item of entries) {
      const ownKey = messageKey(item);
      let key = ownKey ? find(ownKey) : sends.get(item.send_id);
      if (!key) {
        const day = String(item.time).slice(0, 10);
        if (!systemSession.startsWith(day) || item.event === 'bot_starting') systemSession = day + ':' + (item.id || item.time);
        key = 'system:' + systemSession;
      }
      let round = groups.get(key);
      if (!round) {
        round = { id: key, system: key.startsWith('system:'), time: item.time, updated: item.time, source: '', source_kind: '', thread_id: item.thread_id || '', sender: '', entries: [], received: [], sent: [], tools: 0, errors: 0, status: 'received' };
        groups.set(key, round);
      }
      round.entries.push(item); round.updated = item.time;
      if (item.source && item.source_kind !== 'system') { round.source = item.source; round.source_kind = item.source_kind; }
      round.sender ||= item.sender || '';
      round.proactive ||= ['proactive', 'surf'].includes(item.origin);
      if (item.category === 'received' && item.content) round.received.push(item);
      if (item.category === 'error') round.errors++;
      if (item.tool) round.tools++;
      if (stage(item) === 'delivery' && item.content && ['sent', 'error'].includes(item.category)) {
        // One send record may change from unknown to accepted. Equal text with
        // distinct send IDs remains distinct; no global text deduplication.
        let previous = item.send_id ? round.sent.findIndex(x => x.send_id === item.send_id) : -1;
        if (previous < 0 && !item.send_id && ['semantic_plan_bubble_accepted', 'media_accepted_then_caption', 'tool_progress_accepted'].includes(item.reason)) {
          previous = round.sent.findIndex(x => x.reason === 'delivery_confirmed_by_receipt_or_sync' && x.content === item.content && Math.abs(Date.parse(item.time) - Date.parse(x.time)) < 2000);
        }
        if (previous >= 0) {
          if (!['accepted', 'observed'].includes(round.sent[previous].status) || ['accepted', 'observed'].includes(item.status)) round.sent[previous] = item;
        } else round.sent.push(item);
      }
      if (['unknown', 'needs_review', 'waiting', 'retrying'].includes(item.status) && stage(item) === 'delivery') round.status = 'waiting';
      else if (['accepted', 'observed'].includes(item.status) && stage(item) === 'delivery') round.status = item.event === 'message_progress' ? 'running' : 'accepted';
      else if (item.category === 'error' && !item.tool) round.status = ['queued', 'processing', 'running'].includes(item.status) ? 'running' : 'failed';
      else if (item.event === 'message_processed' && !['accepted', 'waiting', 'failed', 'silent', 'stopped'].includes(round.status)) round.status = 'completed';
      else if (['silent', 'skipped', 'blocked', 'stopped', 'cancelled'].includes(item.status)) round.status = item.status === 'cancelled' ? 'stopped' : item.status;
      else if (item.event === 'character_decision' && /silence|silent/.test(item.reason || '')) round.status = 'silent';
      else if (['processing', 'running', 'queued', 'received'].includes(item.status)) round.status = item.status === 'received' && round.entries.length === 1 ? 'received' : 'running';
    }
    return [...groups.values()].map(round => {
      round.source ||= round.system ? '系统运行' : round.thread_id || '来源未知';
      round.source_kind ||= round.system ? 'system' : 'private';
      round.duration = Math.max(0, Date.parse(round.updated) - Date.parse(round.time));
      round.categories = [...new Set(round.entries.map(x => x.category || 'system'))];
      round.search = round.entries.map(x => [x.source, x.sender, x.thread_id, x.message_id, x.send_id, x.content, x.reason, x.event, x.tool, x.query, x.target_url, x.decision_note].filter(Boolean).join(' ')).join(' ').toLocaleLowerCase();
      return round;
    }).sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated));
  }
  return { project, stage, activityTarget };
});
