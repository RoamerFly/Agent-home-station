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
  const taskTraceID = id => /^task-(.+)-\d{8}T\d{6}\.\d{9}$/.exec(id || '')?.[1]
    || /^admin-owner-request-([0-9a-f]{64})$/.exec(id || '')?.[1]
    || /^music-result-([0-9a-f]{64})-\d{6}\.\d{9}$/.exec(id || '')?.[1] || '';
  const musicResultID = id => /^music-result-([0-9a-f]{64})-\d{6}\.\d{9}$/.exec(id || '')?.[1] || '';
  const roundIdentity = id => taskTraceID(id) ? 'task:' + taskTraceID(id)
    : musicResultID(id) ? 'music-result:' + musicResultID(id) : id;
  const messageKey = (item, id = identity(item)) => id && item.thread_id ? JSON.stringify([item.thread_id, roundIdentity(id)]) : '';
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
    done: ['accepted','completed','silent','skipped','blocked','stopped','failed','expired','partially_completed','partially_cancelled','needs_review'],
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
  function project(items, taskStates = {}, now = Date.now(), inbox = {loaded:false, states:{}}, sendStates = {}, planStates = {}) {
    const entries = [...items].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
    const latestEntryTime = entries.reduce((latest, item) => Math.max(latest, Date.parse(item.time) || 0), 0);
    const observedTime = Math.max(latestEntryTime, Number(now) || 0);
    const parents = new Map(), sends = new Map(), groups = new Map();
    const find = key => {
      const seen = new Set();
      while (parents.has(key) && !seen.has(key)) { seen.add(key); key = parents.get(key); }
      return key;
    };
    // related_message_id can also mean a quote or cancellation evidence. Only
    // explicit session-followup events establish ownership of another message.
    // A persisted background continuation is an explicit ownership edge too.
    // Without it every process-result checkpoint becomes a separate active
    // round even when it yielded back to the same original message.
    for (const item of entries) {
      if (['generation_followup_consumed', 'generation_followup_settled', 'backlog_message_grouped', 'background_turn_yielded', 'message_aggregated', 'task_followup_attached'].includes(item.event) && item.related_message_id) {
        const child = messageKey(item), parent = messageKey(item, item.related_message_id);
        if (child && parent && find(parent) !== child) parents.set(child, parent);
      }
    }
    const activeOrigins = new Map();
    for (const item of entries) {
      const id = identity(item), task = taskStates[item.thread_id + '\0' + id];
      if (!id || !task?.active) continue;
      const known = activeOrigins.get(item.thread_id) || new Map();
      const stamp = Date.parse(item.time) || 0;
      known.set(id, Math.min(known.get(id) ?? stamp, stamp));
      activeOrigins.set(item.thread_id, known);
    }
    for (const item of entries) {
      if (item.event !== 'message_received' || !item.message_id || taskStates[item.thread_id + '\0' + item.message_id]?.active) continue;
      const key = messageKey(item);
      if (!key || parents.has(key)) continue;
      const stamp = Date.parse(item.time) || 0;
      const candidates = [...(activeOrigins.get(item.thread_id) || [])].filter(([id, started]) => id !== item.message_id && started <= stamp);
      candidates.sort((a, b) => b[1] - a[1]);
      if (candidates.length) parents.set(key, messageKey(item, candidates[0][0]));
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
        const canonical = key.startsWith('system:') ? '' : JSON.parse(key)[1];
        const taskID = canonical.startsWith('task:') ? taskTraceID(identity(item)) : '';
        round = { id: key, triggerMessageID: taskID ? identity(item) : canonical, taskID, system: key.startsWith('system:'), time: item.time, updated: item.time, source: '', source_kind: '', thread_id: item.thread_id || '', sender: '', entries: [], received: [], sent: [], tools: 0, errors: 0, status: 'received' };
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
      else if (['needs_review', 'expired'].includes(item.status)) round.status = item.status;
      else if (stage(item) === 'delivery' && ['send_outcome_unknown','submission_outcome_unknown','receipt_wait_timeout','receipt_lookup_failed'].includes(item.reason) && item.status === 'failed') {
        // A legacy error without a scheduled lookup is evidence of an unknown
        // result, not a live job. Preserve a lookup already recorded for it.
        if (!['checking_receipt','waiting_receipt','waiting'].includes(round.status)) round.status = 'needs_review';
      }
      else if (item.status === 'retrying' && stage(item) === 'delivery') round.status = 'retrying_delivery';
      else if (['unknown', 'waiting'].includes(item.status) && stage(item) === 'delivery') round.status = 'waiting';
      else if (['accepted', 'observed'].includes(item.status) && stage(item) === 'delivery') round.status = item.event === 'message_progress' ? 'running' : 'accepted';
      else if (item.event === 'send_failed') round.status = 'waiting';
      else if (item.category === 'error' && !item.tool) round.status = ['queued', 'processing', 'running'].includes(item.status) ? 'running' : 'failed';
      else if (item.event === 'context_compression_completed' && item.status === 'completed' && ['running','received','queued'].includes(round.status) && round.entries.some(e=>e.event==='context_compression_started')) round.status = 'completed';
      else if (item.event === 'message_processed' && ['running','received','queued'].includes(round.status)) round.status = 'completed';
      else if (item.event === 'round_manually_completed' && item.status === 'completed') round.status = 'completed';
      else if (['agent_operation','music_generation_finished'].includes(item.event) && item.status === 'completed' && ['received','queued','running'].includes(round.status)) round.status = 'completed';
      else if (item.event === 'round_manually_closed' && item.status === 'stopped') round.status = 'stopped';
      else if (item.event === 'task_execution_waiting' && item.status === 'waiting') round.status = 'waiting';
      else if (partialCancellation(item)) round.status = 'partially_cancelled';
      else if (item.event === 'character_decision' && ['quiet_hours','no_reliable_context'].includes(item.reason)) round.status = 'skipped';
      else if (item.event === 'character_decision' && ['invalid_model_decision','invalid_tool_arguments','tool_unavailable_or_budget_exhausted','tool_budget_exhausted','unverified_model_response','media_unavailable_or_permission_denied'].includes(item.reason)) round.status = 'failed';
      else if (item.event === 'character_decision' && ['cross_conversation_private_route_unavailable','cross_conversation_media_requires_explicit_delivery','private_diagnostic_only'].includes(item.reason)) round.status = 'blocked';
      else if (item.event === 'message_aggregated' && !item.related_message_id) round.status = 'completed';
      else if (item.event === 'shared_video_observation_unavailable') round.status = 'failed';
      else if (item.event === 'character_decision' && item.reason === 'cross_conversation_result_private_delivery_handled') round.status = 'completed';
      else if (['silent', 'skipped', 'blocked', 'stopped', 'cancelled'].includes(item.status)) round.status = item.status === 'cancelled' ? 'stopped' : item.status;
      else if (item.event === 'character_decision' && /silence|silent/.test(item.reason || '')) round.status = 'silent';
      else if (['processing', 'running'].includes(item.status)) round.status = 'running';
      else if (['queued','received'].includes(item.status) && ['queued','received'].includes(round.status)) round.status = item.status;
    }
    return [...groups.values()].map(round => {
      present(round);
      const canonical = round.system ? '' : JSON.parse(round.id)[1];
      round.attempts = new Set(round.entries.map(e => e.message_id).filter(id => id && roundIdentity(id) === canonical)).size;
      round.continuation = !!round.taskID || canonical.startsWith('music-result:');
      {
        const task = taskStates[round.thread_id + '\0' + (round.taskID || canonical)];
        const states = {completed:'completed', cancelled:'stopped', stopped:'stopped', needs_review:'needs_review', failed:'failed', waiting:'waiting', pending:'queued', running:'running', waiting_delivery:'waiting_receipt'};
        if (task && states[task.state]) {
          round.status = task.active ? 'running' : states[task.state];
          round.taskState = task.state;
          round.taskUpdated = task.updated;
          if (Date.parse(task.updated) > Date.parse(round.updated)) round.updated = task.updated;
        }
      }
      // A bare receive/processing entry can survive a worker crash without a
      // durable continuation or a terminal event. Once a later activity entry
      // is present, settle such an old projection for review instead of
      // reporting it as a live task forever. Persistent task, tool and delivery
      // evidence remains active so a real resumable job is never hidden.
      const hasDurableContinuation = round.entries.some(e => [
        'generation_started', 'tool_call_started', 'tool_call_succeeded',
        'tool_call_failed', 'task_execution_waiting', 'task_delivery_updated',
        'delivery_waiting', 'agent_task_started', 'agent_task_saved',
        'background_turn_yielded', 'message_progress',
      ].includes(e.event));
      const hasTerminalEvidence = round.entries.some(e =>
        e.event === 'message_processed' || ['completed', 'accepted', 'failed', 'expired', 'blocked', 'stopped', 'cancelled', 'needs_review'].includes(e.status));
      const ageFromLatest = observedTime - Date.parse(round.updated);
      const hasOnlyReceiveProcessing = round.entries.every(e =>
        e.event === 'message_received' || e.event === 'message_processing' || e.category === 'received');
      if (ageFromLatest >= 10 * 60 * 1000 && !hasDurableContinuation && !hasTerminalEvidence && hasOnlyReceiveProcessing && ['received', 'running'].includes(round.status)) {
        round.status = 'stopped';
        round.staleUnfinished = true;
      }
      // A queued event can outlive its durable inbox entry after the worker
      // processed or pruned it. Only reconcile when the current inbox snapshot
      // was read successfully; an unavailable state file proves nothing.
      if (inbox.loaded && round.status === 'queued' && !round.taskID && round.entries.every(e => e.event === 'message_queued')) {
        const phases = round.entries.map(e => inbox.states?.[e.message_id]?.phase).filter(Boolean);
        if (phases.length && phases.every(phase => ['replied','completed','skipped','stopped','failed','blocked','needs_review'].includes(phase))
            || !phases.length && ageFromLatest >= 10 * 60 * 1000) {
          round.status = 'stopped';
          round.staleUnfinished = true;
        }
      }
      // A receipt-wait record without an inbound turn, a submitted send, or
      // any task continuation is an orphan from an interrupted pre-submit
      // draft. It must not keep the execution-round list in "进行中" forever.
      // Keep the raw event visible for diagnosis, but settle the projection
      // so a later restart can no longer resurrect a phantom active task.
      if (round.status === 'checking_receipt') {
        const hasInbound = round.entries.some(e => e.event === 'message_received' || e.category === 'received');
        const hasSend = round.entries.some(e => e.send_id || e.event === 'message_sent' || e.category === 'sent');
        const hasContinuation = round.entries.some(e => ['generation_started', 'task_delivery_updated', 'agent_task_started', 'agent_task_saved'].includes(e.event));
        const onlyReceiptWait = round.entries.length > 0 && round.entries.every(e => ['delivery_waiting', 'tool_progress_receipt_waiting'].includes(e.event));
        if (onlyReceiptWait && !hasInbound && !hasSend && !hasContinuation) {
          round.status = 'needs_review';
          round.orphanReceipt = true;
        }
		// Repeatedly pausing an old unsent text plan cannot establish that a
		// receipt lookup is still running. Show it for review instead of
		// keeping a permanent active card.
		if (!hasSend && !round.taskID && round.entries.filter(e => e.event === 'utterance_plan_paused').length >= 3 &&
		    !round.entries.some(e => ['delivery_retry', 'task_delivery_updated', 'agent_task_saved'].includes(e.event))) {
		  round.status = 'needs_review';
		  round.stalePlan = true;
		}
      }
      if (round.status === 'running' && ageFromLatest >= 10 * 60 * 1000 && round.entries.length > 0 && round.entries.every(e => e.tool && ['tool_started', 'tool_completed', 'tool_failed', 'tool_blocked', 'tool_cancelled'].includes(e.event) && e.call_id)) {
        const open = new Set();
        for (const e of round.entries) {
          if (e.event === 'tool_started') open.add(e.call_id);
          else open.delete(e.call_id);
        }
        if (!open.size) { round.status = 'needs_review'; round.orphanTools = true; }
      }
      // A started operation is evidence of an attempt, not proof that a
      // worker still owns it. A persisted task is authoritative while fresh;
      // an untracked round is closed after an hour without any new record.
      // This also covers interrupted share reads whose final failure was
      // followed by another start and no matching completion.
      const activeTask = taskStates[round.thread_id + '\0' + (round.taskID || canonical)];
      // A cross-conversation send may receive its native receipt after the
      // last activity row. These rows are standalone delivery cards, not a
      // progress text that could complete a larger still-running task.
      const crossSends = round.entries.filter(e => ['agent_cross_send','cross_conversation_reply_queued'].includes(e.event) && e.send_id);
      if (!activeTask?.active && crossSends.length && crossSends.length === round.entries.length &&
          crossSends.every(e => sends.get(e.send_id) === round.id && sendStates[e.thread_id + '\0' + e.send_id] === true)) {
        round.status = 'accepted';
      }
      // Reading a file already sent by the owner is an auxiliary observation.
      // It cannot create a new pending conversation round on its own.
      if (round.entries.length && round.entries.every(e => String(e.event || '').startsWith('sent_file_read_'))) {
        round.status = round.entries.some(e => e.category === 'error' || e.status === 'failed') ? 'failed' : 'completed';
        round.auxiliaryRead = true;
      }
      const taskAge = observedTime - (Date.parse(activeTask?.updated) || Date.parse(round.updated));
      const scheduledRetry = round.entries.some(e => Date.parse(e.retry_at) > observedTime);
      if (['running', 'cancelling', 'checking_receipt'].includes(round.status) && !activeTask?.active && !scheduledRetry
          && (activeTask ? taskAge >= 24 * 60 * 60 * 1000 : ageFromLatest >= 60 * 60 * 1000)) {
        const failed = round.entries.some(e => e.category === 'error' && e.status === 'failed');
        round.status = failed ? 'failed' : 'stopped';
        round.staleUnfinished = true;
      }
      // A receipt-only continuation still has an unconfirmed send. Historical
      // logs may lack a persisted plan, so the stop event cannot complete it.
      if (round.entries.some(e => e.event === 'utterance_plan_stopped' && e.reason === 'receipt_only_reconciliation') &&
          !round.entries.some(e => e.event === 'task_delivery_updated' && e.status === 'completed')) {
        round.status = activeTask?.active || scheduledRetry ? 'checking_receipt' : 'waiting';
        round.receiptOnly = true;
      }
      // Sending is part of the round. A model decision or an earlier accepted
      // progress message cannot complete a plan with unsent bubbles.
      let plan;
      for (const item of round.entries) {
        const candidate = planStates[item.thread_id + '\0' + item.message_id];
        if (candidate && (!plan || Date.parse(candidate.created) >= Date.parse(plan.created))) plan = candidate;
      }
      const manualCompletion = round.entries.some(e => e.event === 'round_manually_completed');
      if (!manualCompletion && plan) {
        if (plan.state === 'accepted' && plan.index >= plan.total) round.status = 'accepted';
        else if (plan.total > plan.index) {
          if (plan.state === 'waiting_receipt' || plan.stop_reason === 'receipt_only_reconciliation' && (activeTask?.active || scheduledRetry)) round.status = 'checking_receipt';
          else if (['submitting', 'sending'].includes(plan.state) || activeTask?.active) round.status = 'running';
          else if (['model_cancel', 'manual_completion', 'media_queue_cancelled', 'media_cancel_requested_by_user', 'media_superseded_by_new_request'].includes(plan.stop_reason)) round.status = 'stopped';
          else round.status = 'waiting';
        }
      } else if (!manualCompletion && !plan) {
        const lastFailure = round.entries.findLastIndex(e => e.event === 'send_failed');
        if (lastFailure >= 0 && !round.entries.slice(lastFailure + 1).some(e => e.event === 'message_sent' && e.status === 'accepted' || e.event === 'task_delivery_updated' && e.status === 'completed') && !['checking_receipt', 'waiting_receipt'].includes(round.status)) round.status = activeTask?.active ? 'running' : 'waiting';
      }
      if (round.entries.some(e => e.event === 'round_manually_closed')) round.status = 'completed';
      round.group = groupByStatus[round.status] || 'pending';
      // Execution rounds have exactly three public lifecycle states. The raw
      // outcome remains available in the timeline for diagnosis.
      round.lifecycle = groupLabels[round.group];
      round.source ||= round.system ? '系统运行' : round.thread_id || '来源未知';
      // The native thread identity is authoritative for the chat type. A
      // stale or hand-written source_kind must never turn a @chatroom round
      // into a private round in the console.
      round.source_kind = round.system ? 'system' : round.thread_id.endsWith('@chatroom') ? 'group' : 'private';
      round.duration = Math.max(0, Date.parse(round.updated) - Date.parse(round.time));
      round.categories = [...new Set(round.entries.map(x => x.category || 'system'))];
      round.search = round.entries.map(x => [x.source, x.sender, x.thread_id, x.message_id, x.send_id, x.content, x.reason, x.event, x.tool, x.query, x.target_url, x.effect, x.decision_note].filter(Boolean).join(' ')).join(' ').toLocaleLowerCase();
      return round;
    }).filter(round => {
      if (round.entries.some(item => item.event === 'round_manually_closed')) return false;
      // Old settled reviews are no longer execution rounds, including those
      // that contain an earlier accepted segment. Keep the raw journal.
      if (round.system || round.status !== 'needs_review') return true;
      return observedTime - Date.parse(round.updated) < 60 * 60 * 1000;
    }).sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated));
  }
  return { project, stage, activityTarget, groupLabels };
});
