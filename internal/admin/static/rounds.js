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
  const partialCancellation = item => item.status === 'stopped'
    && ['media_cancel_requested_by_user', 'media_superseded_by_new_request'].includes(item.reason)
    && /已接受\s+[1-9]\d*\s*\/\s*\d+\s*段/.test(item.content || '');
  // A failed safety review is deliberately fail-closed, but it is not a social
  // decision to stay quiet. Older persisted entries used status=silent, so
  // identify the reason as well as the status when projecting a round.
  const reviewFailureReason = reason => ['grounding_check_failed', 'grounding_check_rejected', 'grounding_context_invalidated', 'grounding_check_stopped_unverified_reply'].includes(reason);
  // The filter describes the disposition of the round. The detailed status
  // stays available for receipts, failures and partial results.
  const statusGroups = {
    pending: ['received','queued','retrying_tool','retrying_model','retrying_delivery','waiting','waiting_receipt'],
    active: ['running','cancelling','checking_receipt'],
    done: ['accepted','completed','silent','skipped','blocked','stopped','failed','partially_completed','partially_cancelled','needs_review'],
  };
  const groupByStatus = Object.fromEntries(Object.entries(statusGroups).flatMap(([group, states]) => states.map(state => [state,group])));
  const groupLabels = {pending:'待处理',active:'进行中',done:'已完成'};
  // Derive the reader's timeline after grouping. Raw entries still drive
  // search, receipts and diagnostics, including records from older versions.
  function present(round) {
    const entries = round.entries;
    const grouped = new Set();
    const silentFinals = new Set();
    for (const e of entries) {
      if (e.event === 'backlog_message_grouped') {
        if (e.message_id) grouped.add(e.message_id);
        if (e.related_message_id) grouped.add(e.related_message_id);
      }
      if (e.event === 'backlog_conversation_started' && e.message_id) grouped.add(e.message_id);
      if (e.event === 'character_decision' && e.status === 'silent') silentFinals.add(JSON.stringify([e.message_id,e.reason]));
    }
    round.batchSize = grouped.size;
    round.batchMessages = round.received.filter(e => e.event === 'message_received' && grouped.has(e.message_id));
    round.timeline = [];
    let batchShown = false;
    for (const e of entries) {
      const problem = !!e.review_failure || e.category === 'error' || ['failed','blocked','needs_review'].includes(e.status);
      if (!problem && grouped.has(e.message_id) && ['backlog_message_grouped','backlog_conversation_started','message_received'].includes(e.event)) {
        if (!batchShown) round.timeline.push({event:'unread_batch_summary',category:'received',time:e.time});
        batchShown = true;
        continue;
      }
      if (!problem && (e.event === 'model_context_prepared' || e.event === 'generation_started' && entries.length > 1)) continue;
      if (!problem && e.event === 'proactive_evaluated' && silentFinals.has(JSON.stringify([e.message_id,e.reason]))) continue;
      round.timeline.push(e);
    }
    round.hasDiagnostics = batchShown || round.timeline.length !== entries.length;
  }
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
        round = { id: key, triggerMessageID: key.startsWith('system:') ? '' : JSON.parse(key)[1], system: key.startsWith('system:'), time: item.time, updated: item.time, source: '', source_kind: '', thread_id: item.thread_id || '', sender: '', entries: [], received: [], sent: [], tools: 0, errors: 0, status: 'received' };
        groups.set(key, round);
      }
      round.entries.push(item); round.updated = item.time;
      if (item.source && item.source_kind !== 'system') { round.source = item.source; round.source_kind = item.source_kind; }
      round.sender ||= item.sender || '';
      round.proactive ||= ['proactive', 'surf'].includes(item.origin) || item.event === 'conversation_wake_started';
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
      if (['generation_started', 'message_processing'].includes(item.event)) round.failureReported = false;
      if (reviewFailureReason(item.reason)) {
        round.status = 'failed';
      }
      else if (item.reason === 'operation_reported_privately') {
        // Historical logs used status=silent here. The separate private error
        // report's receipt also uses the original ID; neither completes its task.
        round.failureReported = true;
        round.status = 'failed';
      }
      else if (item.event === 'task_delivery_updated' && round.failureReported) round.status = 'failed';
      else if (item.event === 'task_delivery_updated') {
        // A final task receipt is authoritative for this round. A previously
        // accepted progress text never proves that a requested attachment
        // was made; use the task's persisted outcome instead.
        if (item.reason === 'task_delivery_checking_receipt' && item.status === 'waiting_delivery') round.status = 'checking_receipt';
        else if (item.status === 'completed') round.status = 'completed';
        else if (['failed', 'cancelled'].includes(item.status)) round.status = item.status === 'cancelled' ? 'stopped' : 'failed';
        else if (['partially_cancelled', 'partial_cancelled'].includes(item.status)) round.status = 'partially_cancelled';
        else if (['partially_completed', 'partial_completed', 'partial', 'partial_failed'].includes(item.status)) round.status = 'partially_completed';
        else if (['cancelling', 'stopping'].includes(item.status)) round.status = 'cancelling';
        else if (['waiting_receipt', 'receipt_pending'].includes(item.status)) round.status = 'waiting_receipt';
        else if (['retrying_tool', 'waiting_tool_retry'].includes(item.status)) round.status = 'retrying_tool';
        else if (['retrying_model', 'waiting_model_retry'].includes(item.status)) round.status = 'retrying_model';
        else if (['retrying_delivery', 'waiting_delivery_retry'].includes(item.status)) round.status = 'retrying_delivery';
        else if (item.status === 'needs_review') round.status = 'needs_review';
        else if (['waiting', 'waiting_delivery'].includes(item.status)) round.status = 'waiting';
        else if (item.status === 'running') round.status = 'running';
      }
      else if (['partially_cancelled', 'partial_cancelled'].includes(item.status)) round.status = 'partially_cancelled';
      else if (['partially_completed', 'partial_completed', 'partial', 'partial_failed'].includes(item.status)) round.status = 'partially_completed';
      else if (['cancelling', 'stopping'].includes(item.status)) round.status = 'cancelling';
      else if (item.status === 'checking_receipt' || item.event === 'delivery_waiting' && item.retry_at) round.status = 'checking_receipt';
      else if (['waiting_receipt', 'receipt_pending'].includes(item.status)) round.status = 'waiting_receipt';
      else if (['retrying_tool', 'waiting_tool_retry'].includes(item.status)) round.status = 'retrying_tool';
      else if (['retrying_model', 'waiting_model_retry'].includes(item.status)) round.status = 'retrying_model';
      else if (['retrying_delivery', 'waiting_delivery_retry'].includes(item.status)) round.status = 'retrying_delivery';
      else if (item.status === 'needs_review') round.status = 'needs_review';
      else if (stage(item) === 'delivery' && ['send_outcome_unknown','submission_outcome_unknown','receipt_wait_timeout','receipt_lookup_failed'].includes(item.reason) && item.status === 'failed') {
        // A legacy error without a scheduled lookup is evidence of an unknown
        // result, not a live job. Preserve a lookup already recorded for it.
        if (!['checking_receipt','waiting_receipt','waiting'].includes(round.status)) round.status = 'needs_review';
      }
      else if (item.status === 'retrying' && stage(item) === 'delivery') round.status = 'retrying_delivery';
      else if (['unknown', 'waiting'].includes(item.status) && stage(item) === 'delivery') round.status = 'waiting';
      else if (['accepted', 'observed'].includes(item.status) && stage(item) === 'delivery') round.status = item.event === 'message_progress' ? 'running' : 'accepted';
      else if (item.category === 'error' && !item.tool) round.status = ['queued', 'processing', 'running'].includes(item.status) ? 'running' : 'failed';
      else if (item.event === 'context_compression_completed' && item.status === 'completed' && ['running','received','queued'].includes(round.status) && round.entries.some(e=>e.event==='context_compression_started')) round.status = 'completed';
      else if (item.event === 'message_processed' && ['running','received','queued'].includes(round.status)) round.status = 'completed';
      else if (partialCancellation(item)) round.status = 'partially_cancelled';
      else if (item.event === 'character_decision' && ['quiet_hours','no_reliable_context'].includes(item.reason)) round.status = 'skipped';
      else if (item.event === 'character_decision' && ['invalid_model_decision','invalid_tool_arguments','tool_unavailable_or_budget_exhausted','tool_budget_exhausted','unverified_model_response','media_unavailable_or_permission_denied'].includes(item.reason)) round.status = 'failed';
      else if (item.event === 'character_decision' && ['cross_conversation_private_route_unavailable','cross_conversation_media_requires_explicit_delivery','private_diagnostic_only'].includes(item.reason)) round.status = 'blocked';
      else if (item.event === 'character_decision' && item.reason === 'cross_conversation_result_private_delivery_handled') round.status = 'completed';
      else if (['silent', 'skipped', 'blocked', 'stopped', 'cancelled'].includes(item.status)) round.status = item.status === 'cancelled' ? 'stopped' : item.status;
      else if (item.event === 'character_decision' && /silence|silent/.test(item.reason || '')) round.status = 'silent';
      else if (['processing', 'running'].includes(item.status)) round.status = 'running';
      else if (['queued','received'].includes(item.status) && ['queued','received'].includes(round.status)) round.status = item.status;
    }
    return [...groups.values()].map(round => {
      present(round);
      round.group = groupByStatus[round.status] || 'pending';
      round.source ||= round.system ? '系统运行' : round.thread_id || '来源未知';
      round.source_kind ||= round.system ? 'system' : 'private';
      round.duration = Math.max(0, Date.parse(round.updated) - Date.parse(round.time));
      round.categories = [...new Set(round.entries.map(x => x.category || 'system'))];
      round.search = round.entries.map(x => [x.source, x.sender, x.thread_id, x.message_id, x.send_id, x.content, x.reason, x.event, x.tool, x.query, x.target_url, x.effect, x.decision_note].filter(Boolean).join(' ')).join(' ').toLocaleLowerCase();
      return round;
    }).sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated));
  }
  return { project, stage, activityTarget, groupLabels };
});
