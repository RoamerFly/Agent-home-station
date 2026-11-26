'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { project: projectRounds, stage, activityTarget } = require('../internal/admin/static/rounds.js');
// Historical fixtures are evaluated at their last event unless a test supplies
// wall time explicitly. The production projection always uses the real clock.
const project = (items, states, now, inbox, sendStates, planStates) => projectRounds(items, states, now ?? Math.max(...items.map(item => Date.parse(item.time))), inbox, sendStates, planStates);
const at = n => new Date(Date.UTC(2026, 9, 27, 1, 0, n)).toISOString();
const entry = (n, fields = {}) => ({ id: String(n), time: at(n), thread_id: 'fixture@chatroom', message_id: 'message-1', source_kind: 'group', source: '合成测试群', ...fields });

test('system and legacy entries without a thread still project alongside conversation rounds', () => {
 const startup = { id:'startup', time:at(1), event:'bot_starting', category:'system' };
 const legacyQueue = { id:'legacy', time:at(2), message_id:'unlinked', event:'message_queued', category:'decision' };
 const groupMessage = entry(3, { event:'message_received', category:'received', status:'received' });
 const followup = entry(4, { message_id:'group-followup', event:'message_queued', category:'received', status:'queued' });
 const rounds = project([startup, legacyQueue, groupMessage, followup], {}, undefined, {
  loaded:true, states:{ 'message-1':{ phase:'tool_running' } },
 });
 assert.equal(rounds.length, 2);
 assert.deepEqual(rounds.find(round => round.source_kind === 'system').entries, [startup, legacyQueue]);
 assert.deepEqual(rounds.find(round => round.source_kind === 'group').entries, [groupMessage, followup]);
});

test('status filters group terminal failures separately from unresolved receipts and active retries',()=>{
 const cases=[
  ['message_received','received','received','','pending'],
  ['model_failed','error','failed','model_dns_failed','done'],
  ['character_decision','decision','silent','nothing_to_add','done'],
  ['task_delivery_updated','decision','partially_cancelled','','done'],
  ['task_delivery_updated','decision','partially_completed','','done'],
  ['task_delivery_updated','decision','waiting_tool_retry','','pending'],
  ['generation_paused','decision','needs_review','generation_evidence_changed','pending'],
  ['reminder_overdue','decision','expired','reminder_overdue_while_offline','done'],
  ['send_failed','error','failed','send_outcome_unknown','pending'],
  ['message_sent','sent','accepted','','done']
 ];
 for(const [event,category,status,reason,group] of cases){
  const r=project([entry(1,{event,category,status,reason})])[0];
  assert.equal(r.group,group,event);assert.equal(r.entries[0].status,status);
  if(status==='needs_review'){
   const processed=entry(2,{event:'message_processed',category:'decision',status:'completed'});
   assert.equal(project([...r.entries,processed])[0].group,'pending');
  }
  if(reason==='send_outcome_unknown') assert.equal(project([...r.entries,entry(2,{event:'message_processed',category:'decision',status:'completed'})])[0].group,'pending');
 }
});

test('a paused model after a successful download remains pending until its reply is sent',()=>{
 const rows=[
  entry(1,{event:'message_received',category:'received',status:'received'}),
  entry(2,{event:'generation_started',category:'decision',status:'processing'}),
  entry(3,{event:'plugin_output_ready',category:'tool',status:'succeeded'}),
  entry(4,{event:'generation_paused',category:'decision',status:'needs_review',reason:'generation_evidence_changed'}),
 ];
 const [round]=project(rows,{},undefined,{loaded:true,states:{'message-1':{phase:'needs_review'}}});
 assert.equal(round.status,'needs_review');
 assert.equal(round.group,'pending');
 assert.equal(round.lifecycle,'待处理');
 assert.equal(round.sent.length,0);
 assert(round.timeline.some(item=>item.event==='generation_paused'));
});

test('an unsent semantic reply cannot make its execution round completed',()=>{
 const rows=[
  entry(1,{event:'message_received',category:'received',status:'received'}),
  entry(2,{event:'utterance_plan_queued',category:'decision',status:'queued',reason:'semantic_message_plan'}),
  entry(3,{event:'send_failed',category:'error',status:'failed',reason:'reply_evidence_epoch_changed'}),
  entry(4,{event:'bot_runtime_failed',category:'error',status:'failed',reason:'bot_saved_state_unavailable'}),
 ];
 const key='fixture@chatroom\0message-1';
 const stopped={state:'stopped',stop_reason:'reply_evidence_epoch_changed',index:0,total:1,created:at(2)};
 const [unfinished]=project(rows,{},undefined,undefined,undefined,{[key]:stopped});
 assert.equal(unfinished.status,'waiting');assert.equal(unfinished.group,'pending');
 assert.equal(project(rows)[0].group,'pending');
 const [receiptPending]=project(rows,{},undefined,undefined,undefined,{[key]:{...stopped,state:'waiting_receipt'}});
 assert.equal(receiptPending.group,'active');
 const [delivered]=project([...rows,entry(5,{event:'message_sent',category:'sent',status:'accepted'})],{},undefined,undefined,undefined,{[key]:{...stopped,state:'accepted',index:1}});
 assert.equal(delivered.status,'accepted');assert.equal(delivered.group,'done');
});

test('an expired reminder leaves the original chat round unchanged', () => {
 const received = entry(1,{event:'message_received',category:'received',status:'received'});
 const expired = entry(2,{message_id:'reminder-1',event:'reminder_overdue',category:'decision',status:'expired'});
 const rounds = project([received, expired]);
 assert.equal(rounds.length, 2);
 assert.equal(rounds[0].status, 'expired');
 assert.equal(rounds[1].status, 'received');
});

test('only an actual receipt check is shown as Agent work until the receipt arrives',()=>{
 const wait=entry(1,{event:'media_waiting',category:'decision',status:'checking_receipt'});
 const update=entry(2,{event:'task_delivery_updated',category:'decision',status:'waiting_delivery',reason:'task_delivery_checking_receipt'});
 const end=entry(3,{event:'message_processed',category:'decision',status:'completed'});
 const receipt=entry(4,{event:'message_sent',category:'sent',status:'accepted'});
 assert.equal(project([wait,update,end])[0].status,'checking_receipt');
 assert.equal(project([wait,update,end])[0].group,'active');
 assert.equal(project([wait,update,end,receipt])[0].group,'done');
 assert.equal(project([entry(1,{event:'delivery_waiting',category:'decision',status:'waiting',retry_at:at(12)}),entry(2,{event:'send_failed',category:'error',status:'failed',reason:'receipt_wait_timeout'})])[0].status,'checking_receipt');
});
test('orphan receipt wait without an inbound turn is settled instead of shown as active forever',()=>{
 const row=entry(1,{thread_id:'wxid-owner',source_kind:'private',source:'主人',event:'delivery_waiting',category:'decision',status:'waiting',retry_at:at(12),content:'未提交的旧草稿'});
 const [round]=project([row]);
 assert.equal(round.status,'needs_review');
 assert.equal(round.group,'pending');
 assert.equal(round.orphanReceipt,true);
});
test('receipt-only continuation remains unfinished until a verified receipt',()=>{
 const inbound=entry(1,{event:'message_received',category:'received',status:'received'});
 const wait=entry(2,{event:'delivery_waiting',category:'decision',status:'waiting',content:'合成旧回复'});
 const detached=entry(3,{event:'utterance_plan_stopped',category:'decision',status:'completed',reason:'receipt_only_reconciliation'});
 const [round]=project([inbound,wait,detached]);
 assert.equal(round.group,'pending');assert.equal(round.status,'waiting');assert.equal(round.receiptOnly,true);
 assert.equal(round.timeline.some(e=>e.event==='delivery_waiting'),true);
 const scheduled=entry(2,{...wait,retry_at:at(12)});
 assert.equal(project([inbound,scheduled,detached])[0].group,'active');
	const confirmed=entry(4,{event:'message_sent',category:'sent',status:'accepted',content:'合成旧回复'});
 assert.equal(project([inbound,wait,detached,confirmed])[0].group,'done');
});
test('paused text receipt stays pending even after a task ends and an old retry was scheduled',()=>{
 const inbound=entry(1,{event:'message_received',category:'received',status:'received'});
 const wait=entry(2,{event:'delivery_waiting',category:'decision',status:'waiting',content:'未确认文字',retry_at:at(20)});
 const detached=entry(3,{event:'utterance_plan_stopped',category:'decision',status:'completed',reason:'receipt_only_reconciliation'});
 const paused=entry(4,{event:'delivery_receipt_paused',category:'decision',status:'waiting',content:'未确认文字'});
 const taskStates={'fixture@chatroom\0message-1':{state:'completed',updated:at(3),active:false}};
 const [pending]=project([inbound,wait,detached,paused],taskStates,Date.parse(at(5)));
 assert.equal(pending.group,'pending');assert.equal(pending.status,'waiting');
 const accepted=entry(6,{event:'message_sent',category:'sent',status:'accepted',content:'未确认文字'});
 const planStates={'fixture@chatroom\0message-1':{state:'accepted',index:1,total:1,created:at(1)}};
 assert.equal(project([inbound,wait,detached,paused,accepted],taskStates,Date.parse(at(6)),undefined,undefined,planStates)[0].group,'done');
});
test('legacy exhausted receipt is pending without a synthetic pause event',()=>{
 const inbound=entry(1,{event:'message_received',category:'received',status:'received'});
 const wait=entry(2,{event:'delivery_waiting',category:'decision',status:'waiting',content:'旧发送',retry_at:at(30)});
 const detached=entry(3,{event:'utterance_plan_stopped',category:'decision',status:'completed',reason:'receipt_only_reconciliation'});
 const key='fixture@chatroom\0message-1';
 const planStates={[key]:{state:'stopped',stop_reason:'receipt_only_reconciliation',index:0,total:1,created:at(1),receipt_dormant:true}};
 const [round]=project([inbound,wait,detached],{},Date.parse(at(4)),undefined,undefined,planStates);
 assert.equal(round.group,'pending');assert.equal(round.status,'waiting');assert.equal(round.receiptDormant,true);
});
test('receipt for another text segment does not finish an unresolved segment',()=>{
 const received=entry(1,{event:'message_received',category:'received',status:'received'});
 const pending=entry(2,{event:'delivery_waiting',category:'decision',status:'waiting',content:'第一段',retry_at:at(20)});
 const sent=entry(3,{event:'message_sent',category:'sent',status:'accepted',content:'第二段'});
 const [round]=project([received,pending,sent]);
 assert.equal(round.group,'active');assert.equal(round.status,'checking_receipt');
});
test('old stopped review with an accepted segment leaves the execution list',()=>{
 const accepted=entry(1,{message_id:'old-review',event:'message_sent',category:'sent',status:'accepted',send_id:'segment-1'});
 const review=entry(2,{message_id:'old-review',event:'generation_paused',category:'decision',status:'needs_review'});
 const later={...entry(3,{message_id:'new-message',event:'message_received',category:'received',status:'received'}),time:new Date(Date.parse(review.time)+2*60*60*1000).toISOString()};
 const rounds=project([accepted,review,later]);
 assert.equal(rounds.some(x=>x.triggerMessageID==='old-review'),false);
});
test('thread identity wins over a stale source kind label',()=>{
 const [round]=project([entry(1,{source_kind:'private',thread_id:'fixture@chatroom',event:'message_received',category:'received',status:'received'})]);
 assert.equal(round.source_kind,'group');
});
test('an old receive-only round is shown as stopped when later activity exists',()=>{
 const old=entry(1,{message_id:'old-message',event:'message_received',category:'received',status:'received'});
 const later={...entry(2,{message_id:'later-message',event:'message_received',category:'received',status:'received'}),time:new Date(Date.parse(old.time)+11*60*1000).toISOString()};
 const [round]=project([old,later]).filter(x=>x.triggerMessageID==='old-message');
 assert.equal(round.status,'stopped');
 assert.equal(round.group,'done');
 assert.equal(round.staleUnfinished,true);
});
test('owner continuation and accepted cross-media batch leave pending',()=>{
 const id='a'.repeat(64), thread='owner-chat', target='group@chatroom';
 const owner=entry(1,{thread_id:thread,message_id:'admin-owner-request-'+id,event:'owner_request_queued',status:'pending'});
 const media=entry(2,{thread_id:target,message_id:'console-media-'+id,event:'cross_media_queued',status:'queued'});
 const states={[thread+'\0'+id]:{state:'cancelled',updated:owner.time},[target+'\0console-media-'+id]:{state:'completed',updated:media.time}};
 const rounds=project([owner,media],states);
 assert.equal(rounds.find(x=>x.thread_id===thread).status,'stopped');
 assert.equal(rounds.find(x=>x.thread_id===target).status,'completed');
});
test('a durable continuation stays pending or active even when older than later activity',()=>{
 const old=entry(1,{message_id:'saved-task',event:'message_received',category:'received',status:'received'});
 const saved=entry(2,{message_id:'saved-task',event:'task_execution_waiting',category:'decision',status:'waiting'});
 const later={...entry(3,{message_id:'later-message',event:'message_received',category:'received',status:'received'}),time:new Date(Date.parse(old.time)+11*60*1000).toISOString()};
 const [round]=project([old,saved,later]).filter(x=>x.triggerMessageID==='saved-task');
 assert.equal(round.status,'waiting');
 assert.equal(round.staleUnfinished,undefined);
});
test('a 25-message unread batch retains all evidence but has a single timeline entry before the actual failure',()=>{
 const items=[];
 for(let i=0;i<25;i++) items.push(entry(i,{message_id:'m'+i,event:'message_received',category:'received',content:'合成消息'}));
 for(let i=0;i<24;i++) items.push(entry(25+i,{message_id:'m'+i,related_message_id:'m24',event:'backlog_message_grouped',category:'decision'}));
 for(const [event,category] of [['backlog_conversation_started','decision'],['generation_started','decision'],['model_context_prepared','decision'],['send_failed','error']])
  items.push(entry(items.length,{message_id:'m24',event,category}));
 const [round]=project(items);
 assert.equal(round.batchSize,25);assert.equal(round.batchMessages.length,25);
 assert.deepEqual(round.timeline.map(x=>x.event),['unread_batch_summary','send_failed']);
 assert.equal(round.entries.length,items.length);assert.equal(round.status,'waiting');assert.equal(round.hasDiagnostics,true);
});

test('one autonomous decision is visible, with its scheduled follow-up and raw context available',()=>{
 const [r]=project(['conversation_wake_started','generation_started','model_context_prepared','proactive_evaluated','conversation_wake_scheduled','character_decision'].map((event,i)=>entry(i,{event,category:'decision',...(['proactive_evaluated','character_decision'].includes(event)?{reason:'awaiting_participant',status:'silent'}:{})})));
 assert.deepEqual(r.timeline.map(x=>x.event),['conversation_wake_started','conversation_wake_scheduled','character_decision']);
 assert.equal(r.status,'silent');assert.equal(r.proactive,true);assert.equal(r.entries.length,6);
 const [failure]=project([entry(1,{event:'model_context_prepared',category:'error',status:'failed'})]);
 assert.equal(failure.timeline.length,1);assert.equal(failure.status,'failed');
});

test('no recognized speech remains an observation outcome rather than a failed turn',()=>{
 const rows=project([
  entry(1,{event:'message_received',category:'received'}),
  entry(2,{event:'video_audio_no_words',category:'decision',status:'no_recognizable_speech',tool_http_status:400,tool_code:'ASR_RESPONSE_HAVE_NO_WORDS'}),
  entry(3,{event:'channels_video_observed',category:'decision',status:'observed',reason:'video_visual_audio_no_words'})
 ]);
 assert.equal(rows.length,1);assert.equal(rows[0].errors,0);assert.notEqual(rows[0].status,'failed');
});

test('authenticated API endpoints are labels; real content remains navigable', () => {
  const api = activityTarget({target_url:'https://api.deepseek.com/v1/tools/search'});
  assert.equal(api.kind, 'service'); assert.equal(api.href, undefined);
  assert.equal(activityTarget({target_url:'https://example.org/song'}).href, 'https://example.org/song');
  assert.equal(activityTarget({target_url:'javascript:alert(1)'}), null);
});

test('startup unread from multiple people becomes one conversation round with original times',()=>{
 const rows=project([
  entry(1,{message_id:'old-one',event:'message_received',category:'received',sender:'one',message_time:'2026-10-26T22:00:00+08:00',content:'昨晚的问题'}),
  entry(2,{message_id:'new-two',event:'message_received',category:'received',sender:'two',message_time:'2026-10-27T07:00:00+08:00',content:'问题已解决'}),
  entry(3,{message_id:'old-one',related_message_id:'new-two',event:'backlog_message_grouped',category:'decision'}),
  entry(4,{message_id:'new-two',event:'backlog_conversation_started',category:'decision'}),
  entry(5,{thread_id:'another@chatroom',message_id:'other',event:'message_received',category:'received'})
 ]);
 assert.equal(rows.length,2);const batch=rows.find(x=>x.thread_id==='fixture@chatroom');assert.equal(batch.received.length,2);assert.equal(batch.received[0].message_time,'2026-10-26T22:00:00+08:00');
});
test('aggregated child messages join the parent instead of leaving pending cards',()=>{
 const child=entry(1,{message_id:'child',event:'message_received',category:'received',status:'received'});
 const aggregate=entry(2,{message_id:'child',related_message_id:'parent',event:'message_aggregated',status:'aggregated'});
 const parent=entry(3,{message_id:'parent',event:'message_processed',status:'completed'});
 const [round]=project([child,aggregate,parent]);
 assert.equal(round.entries.length,3);assert.equal(round.group,'done');
 const [legacy]=project([entry(1,{message_id:'older-child',event:'message_aggregated',status:'aggregated'})]);
 assert.equal(legacy.group,'done');
});
test('one group collection window has one queued card and keeps the actual batch anchor',()=>{
 const queued=(n,id,related_message_id,thread_id='fixture@chatroom')=>entry(n,{thread_id,message_id:id,related_message_id,event:'message_queued',category:'received',status:'queued'});
 const a=queued(1,'window-a',''),b=queued(2,'window-b','window-a'),c=queued(3,'window-c','window-a');
 const next=queued(4,'next-window',''),other=queued(5,'other-thread','','other@chatroom');
 let rows=project([a,b,c,next,other]);
 assert.equal(rows.length,3);
 assert.equal(rows.find(r=>r.triggerMessageID==='window-a').entries.length,3);
 // Older queue logs have no link. The persisted inbox deadline restores the
 // exact window without relying on a wall-clock 00/30-second boundary.
 const old=[a,queued(2,'window-b',''),queued(3,'window-c',''),next,other];
 const states={
  'window-a':{phase:'received',direction:'incoming',aggregate_until:'2026-10-27T01:00:30Z'},
  'window-b':{phase:'received',direction:'incoming',aggregate_until:'2026-10-27T01:00:30Z'},
  'window-c':{phase:'received',direction:'incoming',aggregate_until:'2026-10-27T01:00:30Z'},
  'next-window':{phase:'received',direction:'incoming',aggregate_until:'2026-10-27T01:01:01Z'},
  'other-thread':{phase:'received',direction:'incoming',aggregate_until:'2026-10-27T01:00:30Z'},
 };
 rows=project(old,{},undefined,{loaded:true,states});
 assert.equal(rows.length,3);
 assert.equal(rows.find(r=>r.triggerMessageID==='window-a').entries.length,3);
 rows=project([
  a,b,c,next,other,
  entry(6,{message_id:'window-a',related_message_id:'window-b',event:'message_aggregated',category:'decision',status:'aggregated'}),
  entry(7,{message_id:'window-c',related_message_id:'window-b',event:'message_aggregated',category:'decision',status:'aggregated'}),
  entry(8,{message_id:'window-b',event:'message_processed',category:'decision',status:'completed'}),
 ]);
 assert.equal(rows.length,3);
 const batch=rows.find(r=>r.triggerMessageID==='window-b');
 assert.equal(batch.entries.length,6);
 assert.equal(batch.group,'done');
});
test('messages queued during an active group turn join it before and after receipt',()=>{
 const root=entry(1,{message_id:'active-root',event:'generation_started',category:'decision',status:'running'});
 const queued=entry(2,{message_id:'new-message',event:'message_queued',category:'received',status:'queued'});
 const linkedQueue={...queued,related_message_id:'active-root'};
 const received=entry(3,{message_id:'new-message',event:'message_received',category:'received',status:'received',content:'请接着处理'});
 const inbox={loaded:true,states:{'active-root':{phase:'model_running'},'new-message':{phase:'received'}}};
 let rows=project([root,queued],{},undefined,inbox);
 assert.equal(rows.length,1);
 assert.equal(rows[0].entries.length,2);
 rows=project([root,queued,received],{},undefined,inbox);
 assert.equal(rows.length,1);
 assert.equal(rows[0].received.length,1);
 const states={
  'fixture@chatroom\0active-root':{state:'completed',active:false,updated:at(4)},
  'fixture@chatroom\0new-message':{state:'running',active:true,updated:at(4)},
 };
 rows=project([root,linkedQueue,received],states,Date.parse(at(4)),{loaded:true,states:{'active-root':{phase:'completed'},'new-message':{phase:'model_running'}}});
 assert.equal(rows.length,1);
 assert.equal(rows[0].group,'active');
});
test('old queued cards use a successful inbox snapshot before being settled',()=>{
 const queued=entry(1,{message_id:'old-queue',event:'message_queued',status:'queued'});
 const now=Date.parse(queued.time)+11*60*1000;
 assert.equal(project([queued],{},now,{loaded:false,states:{}})[0].group,'pending');
 assert.equal(project([queued],{},now,{loaded:true,states:{'old-queue':{phase:'received'}}})[0].group,'pending');
 assert.equal(project([queued],{},now,{loaded:true,states:{}})[0].group,'done');
 assert.equal(project([queued],{},Date.parse(queued.time),{loaded:true,states:{'old-queue':{phase:'skipped'}}})[0].group,'done');
});

test('one round keeps receive, decision, failed tool, recovered tool and delivery evidence', () => {
  const rows = project([
    entry(1, { category: 'received', event: 'message_received', content: '帮我找一首歌', status: 'received' }),
    entry(2, { category: 'error', event: 'tool_call_failed', tool: 'web_search', status: 'failed' }),
    entry(3, { category: 'tool', event: 'tool_call_succeeded', tool: 'web_search', status: 'succeeded' }),
    entry(4, { category: 'sent', event: 'message_sent', content: '[voice] 歌曲.wav', send_id: 'send-1', status: 'accepted' }),
    entry(5, { event: 'message_processed', status: 'completed' }),
  ]);
  assert.equal(rows.length, 1); assert.equal(rows[0].entries.length, 5);
  assert.equal(rows[0].status, 'accepted'); assert.equal(rows[0].tools, 2); assert.equal(rows[0].errors, 1);
  assert(rows[0].categories.includes('error')); assert(rows[0].search.includes('web_search'));
});
test('explicit same-session followups merge, cancellation evidence and other threads do not', () => {
  const rows = project([
    entry(1, { category: 'received', event: 'message_received', content: '找首歌' }),
    entry(2, { message_id: 'followup', event: 'message_received', category: 'received', content: '改成文件' }),
    entry(3, { message_id: 'followup', related_message_id: 'message-1', event: 'generation_followup_consumed', category: 'decision' }),
    entry(4, { message_id: 'other', related_message_id: 'message-1', event: 'generation_stopped', category: 'decision' }),
    entry(5, { thread_id: 'other@chatroom', event: 'message_received', category: 'received' }),
  ]);
  assert.equal(rows.length, 3);
  assert.equal(rows.find(x => x.received.some(m => m.content === '改成文件')).received.length, 2);
});
test('background continuations merge only with an explicit source and preserve raw activity', () => {
  const items = [
    entry(1, { message_id: 'saved-task', event: 'generation_started', category: 'decision', status: 'running' }),
    entry(2, { message_id: 'new-message', event: 'message_received', category: 'received', content: '继续处理' }),
    entry(3, { message_id: 'saved-task', related_message_id: 'new-message', event: 'background_turn_yielded', category: 'decision', status: 'waiting' }),
    entry(4, { message_id: 'orphan-task', event: 'background_turn_yielded', category: 'decision', status: 'waiting' }),
    entry(5, { message_id: 'unrelated-task', related_message_id: 'new-message', event: 'generation_stopped', category: 'decision', status: 'stopped' }),
  ];
  const before = JSON.stringify(items);
  const rows = project(items);
  assert.equal(JSON.stringify(items), before);
  assert.equal(rows.length, 3);
  const continued = rows.find(x => x.triggerMessageID === 'new-message');
  assert(continued);
  assert.equal(continued.entries.length, 3);
  assert.equal(continued.received[0].message_id, 'new-message');
  assert.equal(rows.find(x => x.triggerMessageID === 'orphan-task').entries.length, 1);
  assert.equal(rows.find(x => x.triggerMessageID === 'unrelated-task').entries.length, 1);
});
test('latest task execution evidence moves one round to waiting and later work resumes it', () => {
  const items = [
    entry(1, { message_id: 'task', event: 'generation_started', category: 'decision', status: 'running' }),
    entry(2, { message_id: 'task', event: 'task_execution_waiting', category: 'decision', status: 'waiting', reason: 'silent_execution_pending' }),
    entry(3, { message_id: 'other-task', event: 'generation_started', category: 'decision', status: 'running' }),
  ];
  let rows = project(items);
  assert.equal(rows.find(x => x.triggerMessageID === 'task').status, 'waiting');
  assert.equal(rows.find(x => x.triggerMessageID === 'task').group, 'pending');
  assert.equal(rows.find(x => x.triggerMessageID === 'other-task').status, 'running');
  rows = project([...items, entry(4, { message_id: 'task', event: 'generation_started', category: 'decision', status: 'running' })]);
  assert.equal(rows.find(x => x.triggerMessageID === 'task').status, 'running');
  assert.equal(rows.find(x => x.triggerMessageID === 'task').group, 'active');
});
test('timestamped attempts of one durable task form one card with its persisted final state', () => {
 const id='a'.repeat(64), first=`task-${id}-20261122T010000.000000001`, second=`task-${id}-20261122T020000.000000002`;
 const items=[
  entry(1,{message_id:first,event:'generation_started',status:'running'}),
  entry(2,{message_id:first,event:'task_execution_waiting',status:'waiting'}),
  entry(3,{message_id:second,event:'generation_started',status:'running'}),
  entry(4,{message_id:second,event:'task_execution_waiting',status:'waiting'}),
 ];
 const [round]=project(items,{['fixture@chatroom\0'+id]:{state:'completed',updated:at(5)}});
 assert.equal(round.entries.length,4);assert.equal(round.attempts,2);
 assert.equal(round.taskID,id);assert.equal(round.status,'completed');assert.equal(round.group,'done');
 assert.equal(project(items,{['fixture@chatroom\0'+id]:{state:'needs_review',updated:at(5)}})[0].status,'needs_review');
});
test('two callbacks for the same music result form one completed card', () => {
 const id='b'.repeat(64);
 const rounds=project([
  entry(1,{message_id:`music-result-${id}-010000.000000001`,event:'task_execution_waiting',status:'waiting'}),
  entry(2,{message_id:`music-result-${id}-010100.000000002`,event:'message_sent',category:'sent',status:'accepted'}),
 ]);
 assert.equal(rounds.length,1);assert.equal(rounds[0].attempts,2);assert.equal(rounds[0].group,'done');
});
test('old tool-only round with every call settled is no longer shown as running', () => {
 const old=entry(1,{message_id:'old-tool',event:'tool_started',category:'tool',tool:'read',call_id:'call-1',status:'running'});
 const end=entry(2,{message_id:'old-tool',event:'tool_completed',category:'tool',tool:'read',call_id:'call-1',status:'succeeded'});
 const later={...entry(3,{message_id:'later',event:'message_received',category:'received'}),time:new Date(Date.parse(old.time)+11*60*1000).toISOString()};
 const settled=project([old,end,later]).find(x=>x.triggerMessageID==='old-tool');
 assert.equal(settled.status,'needs_review');assert.equal(settled.orphanTools,true);
 const unfinished=project([old,later]).find(x=>x.triggerMessageID==='old-tool');
 assert.equal(unfinished.group,'active');
});
test('interrupted share read and untracked tool calls cannot stay active forever', () => {
 const received=entry(1,{message_id:'share',event:'message_received',category:'received',status:'received'});
 const failed=entry(2,{message_id:'share',event:'group_input_read_failed',category:'error',status:'failed'});
 const restarted=entry(3,{message_id:'share',event:'shared_video_read_started',category:'decision',status:'processing'});
 const open=entry(4,{message_id:'share',event:'tool_started',category:'tool',tool:'share.resolve_public',call_id:'open-call',status:'running'});
 assert.equal(project([received,failed,restarted,open])[0].group,'active');
 const [stale]=project([received,failed,restarted,open],{},Date.parse(open.time)+61*60*1000);
 assert.equal(stale.status,'failed');assert.equal(stale.group,'done');assert.equal(stale.staleUnfinished,true);
});
test('a final share observation failure ends the round immediately', () => {
 const start=entry(1,{event:'shared_video_read_started',status:'processing'});
 const unavailable=entry(2,{event:'shared_video_observation_unavailable',status:'unavailable',reason:'share_plugin_resolution_needs_review'});
 const [round]=project([start,unavailable]);
 assert.equal(round.status,'failed');assert.equal(round.group,'done');
});
test('native accepted cross-conversation send settles standalone queued card', () => {
 const queued=entry(1,{message_id:'cross-request',event:'cross_conversation_reply_queued',category:'decision',status:'queued',send_id:'send-1'});
 const receipt={'fixture@chatroom\0send-1':true};
 assert.equal(project([queued])[0].group,'pending');
 assert.equal(project([queued],{},undefined,undefined,receipt)[0].status,'accepted');
 assert.equal(project([queued],{'fixture@chatroom\0cross-request':{state:'pending',active:true,updated:queued.time}},undefined,undefined,receipt)[0].group,'active');
 assert.equal(project([queued],{},undefined,undefined,{'other@chatroom\0send-1':true})[0].group,'pending');
 const collision=entry(2,{message_id:'another-request',event:'agent_cross_send',category:'sent',status:'queued',send_id:'send-1'});
 assert(project([queued,collision],{},undefined,undefined,receipt).every(round=>round.group==='pending'));
});
test('sent-file read evidence cannot create a pending conversation card', () => {
 const rows=['identified','downloading','available'].map((part,i)=>entry(i,{message_id:'owner-sent-file',event:'sent_file_read_'+part,category:'tool',status:part}));
 const [round]=project(rows);
 assert.equal(round.group,'done');assert.equal(round.auxiliaryRead,true);
});
test('a fresh persisted task or scheduled receipt lookup remains live', () => {
 const id='c'.repeat(64), trace=`task-${id}-20261122T010000.000000001`;
 const running=entry(1,{message_id:trace,event:'generation_started',status:'running'});
 const updated=new Date(Date.parse(running.time)+65*60*1000).toISOString();
 assert.equal(project([running],{['fixture@chatroom\0'+id]:{state:'running',updated}},Date.parse(updated)+1000)[0].group,'active');
 assert.equal(project([running],{['fixture@chatroom\0'+id]:{state:'running',updated}},Date.parse(updated)+25*60*60*1000)[0].group,'done');
 const wait=entry(2,{message_id:'receipt',event:'delivery_waiting',status:'checking_receipt',retry_at:new Date(Date.parse(running.time)+2*60*60*1000).toISOString()});
 const inbound=entry(1,{message_id:'receipt',event:'message_received',category:'received',status:'received'});
 assert.equal(project([inbound,wait],{},Date.parse(wait.time)+70*60*1000)[0].group,'active');
});
test('no time-based merging; orphan system events form separate system periods', () => {
  const rows = project([
    entry(1, { message_id: '', thread_id: '', event: 'bot_starting', category: 'system' }),
    entry(2, { message_id: '', thread_id: '', event: 'memory_saved', category: 'system' }),
    entry(3, { event: 'message_received', category: 'received' }),
    entry(4, { message_id: 'message-2', event: 'message_received', category: 'received' }),
    entry(5, { message_id: '', thread_id: '', event: 'bot_starting', category: 'system' }),
  ]);
  assert.equal(rows.filter(x => x.system).length, 2);
  assert.equal(rows.filter(x => !x.system).length, 2);
  assert.equal(rows.find(x => x.entries.length === 2).system, true);
});
test('same send ID updates receipt; equal text from distinct sends is retained', () => {
  const rows = project([
    entry(1, { event: 'send_failed', category: 'error', send_id: 'send-1', content: '你好', status: 'unknown' }),
    entry(2, { message_id: '', event: 'manual_send_status', category: 'sent', send_id: 'send-1', content: '你好', status: 'accepted' }),
    entry(3, { event: 'message_sent', category: 'sent', send_id: 'send-2', content: '你好', status: 'accepted' }),
  ]);
  assert.equal(rows.length, 1); assert.equal(rows[0].sent.length, 2); assert.equal(rows[0].sent[0].status, 'accepted');
});
test('long identifiers cannot collide; cycles do not hang and data is not mutated', () => {
  const items = [entry(1, { event: 'message_received', category: 'received' }), entry(2, { message_id: 'two', related_message_id: 'message-1', event: 'generation_followup_consumed' }), entry(3, { related_message_id: 'two', event: 'generation_followup_consumed' })];
  const before = JSON.stringify(items); assert.equal(project(items).length, 1); assert.equal(JSON.stringify(items), before);
  assert.equal(stage({ category: 'tool', event: 'media_understood' }), 'tool');
});
test('an accepted acknowledgement is not completion of a long-running task', () => {
  const rows = project([
    entry(1, { category: 'received', event: 'message_received', content: '唱首歌', status: 'received' }),
    entry(2, { category: 'sent', event: 'message_progress', content: '我来试试～', status: 'accepted' }),
    entry(3, { category: 'tool', event: 'tool_call_succeeded', tool: 'music_generate', status: 'succeeded' }),
  ]);
  assert.equal(rows[0].status, 'running'); assert.equal(rows[0].sent[0].status, 'accepted');
});

test('an unfinished group task keeps its round active and includes later messages', () => {
  const items = [
    entry(1, { message_id:'image-request', event:'message_received', category:'received', content:'画一张居家照', status:'received' }),
    entry(2, { message_id:'image-request', event:'agent_task_saved', category:'decision', status:'pending' }),
    entry(3, { message_id:'image-request', event:'message_sent', category:'sent', content:'我画好了发你', status:'accepted' }),
    entry(4, { message_id:'followup', event:'message_received', category:'received', content:'这个画完没', status:'received' }),
    entry(5, { message_id:'followup', event:'message_processing', category:'decision', status:'processing' }),
  ];
  const states = { 'fixture@chatroom\0image-request': {state:'pending',active:true,updated:at(5)} };
  const rows = project(items, states);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].group, 'active');
  assert.equal(rows[0].received.length, 2);
});
test('a later partial delivery timeout remains visible after an earlier accepted segment', () => {
  const rows = project([
    entry(1, { event: 'message_sent', category: 'sent', content: '第一段', send_id: 'one', status: 'accepted' }),
    entry(2, { event: 'media_waiting', category: 'error', content: '第二段', send_id: 'two', status: 'unknown' }),
    entry(3, { event: 'tool_call_succeeded', category: 'tool', tool: 'read_receipt', status: 'succeeded' }),
  ]);
  assert.equal(rows[0].status, 'waiting');
});
test('ambiguous send IDs cannot attach unscoped records to an unrelated round', () => {
  const rows = project([
    entry(1, { send_id: 'collision' }), entry(2, { message_id: 'other', send_id: 'collision' }),
    entry(3, { send_id: 'collision' }), entry(4, { message_id: '', thread_id: '', send_id: 'collision' }),
  ]);
  assert.equal(rows.filter(x => x.system).length, 1);
});
test('task delivery outcome settles the round without mistaking a progress reply for audio delivery', () => {
  const received=entry(1,{event:'message_received',category:'received',content:'用指定声音讲解视频',status:'received'});
  const progress=entry(2,{event:'message_progress',category:'sent',content:'正在处理',status:'accepted'});
  const working=entry(3,{event:'task_delivery_updated',category:'decision',status:'waiting_delivery',reason:'task_delivery_needs_review'});
  const settled=entry(4,{event:'task_delivery_updated',category:'decision',status:'completed',reason:'task_delivery_accepted'});
  assert.equal(project([received,progress,working])[0].status,'waiting');
  assert.equal(project([received,progress,working,settled])[0].status,'completed');
  assert.equal(project([received,progress,entry(4,{event:'task_delivery_updated',category:'decision',status:'failed'})])[0].status,'failed');
});

test('private error-report delivery cannot turn failed share into silence or completion', () => {
  const rows = [
    entry(1,{event:'message_received',category:'received',status:'received'}),
    entry(2,{event:'model_failed',category:'error',status:'failed',reason:'source_identity_conflict_or_unavailable'}),
    entry(3,{event:'character_decision',category:'decision',status:'silent',reason:'operation_reported_privately'}),
    entry(4,{event:'task_delivery_updated',category:'decision',status:'completed',reason:'task_delivery_accepted'}),
  ];
  assert.equal(project(rows.slice(0,3))[0].status,'failed');
  assert.equal(project(rows)[0].status,'failed');
  assert.equal(project([...rows,entry(5,{event:'generation_started',category:'decision',status:'processing'}),entry(6,{event:'character_decision',category:'decision',status:'silent',reason:'model_chose_silence'})])[0].status,'silent');
});

test('a fail-closed grounding review remains a failed round, never model silence', () => {
  const rows = project([
    entry(1, { event: 'message_received', category: 'received', status: 'received', content: '请总结分享' }),
    entry(2, { event: 'character_decision', category: 'decision', status: 'silent', reason: 'grounding_check_failed', review_failure: { code: 'model_invalid_response', stage: 'decode', review_state: 'initial_check_failed' } }),
    entry(3, { event: 'message_processed', category: 'decision', status: 'completed' }),
  ]);
  assert.equal(rows[0].status, 'failed');
  assert.equal(rows[0].errors, 0, 'legacy records may not have category=error, but must retain failed status');
});
