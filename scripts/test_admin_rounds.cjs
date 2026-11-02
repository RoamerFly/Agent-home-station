'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { project, stage, activityTarget } = require('../internal/admin/static/rounds.js');
const at = n => new Date(Date.UTC(2026, 9, 27, 1, 0, n)).toISOString();
const entry = (n, fields = {}) => ({ id: String(n), time: at(n), thread_id: 'fixture@chatroom', message_id: 'message-1', source_kind: 'group', source: '合成测试群', ...fields });

test('status filters group terminal failures separately from unresolved receipts and active retries',()=>{
 const cases=[
  ['message_received','received','received','','active'],
  ['model_failed','error','failed','model_dns_failed','closed'],
  ['character_decision','decision','silent','nothing_to_add','closed'],
  ['task_delivery_updated','decision','partially_cancelled','','closed'],
  ['task_delivery_updated','decision','partially_completed','','closed'],
  ['task_delivery_updated','decision','waiting_tool_retry','','active'],
  ['generation_paused','decision','needs_review','generation_evidence_changed','verify'],
  ['send_failed','error','failed','send_outcome_unknown','verify'],
  ['message_sent','sent','accepted','','done']
 ];
 for(const [event,category,status,reason,group] of cases){
  const r=project([entry(1,{event,category,status,reason})])[0];
  assert.equal(r.group,group,event);assert.equal(r.entries[0].status,status);
  if(group==='verify'){
   const processed=entry(2,{event:'message_processed',category:'decision',status:'completed'});
   assert.equal(project([...r.entries,processed])[0].group,'verify');
  }
 }
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
 assert.equal(round.entries.length,items.length);assert.equal(round.status,'failed');assert.equal(round.hasDiagnostics,true);
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
