/* Synthetic-only component acceptance. All HTTP is fulfilled locally; no server,
 * login, private configuration, provider, native process or WeChat is contacted. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const root=path.resolve(__dirname,'../internal/admin/static'),origin='http://127.0.0.1:47893';
const output=path.resolve(process.env.WECHAT_DEEP_REPAIR_UI_OUTPUT||'temp/deep-repair-ui');
const stamp='2026-10-30T08:00:00Z',scope='synthetic-scope',writes=[],requests=[];
const names={'person-a':'小甲','person-b':'小乙','person-c':'小丙'};
const sources={anchor:{id:'anchor',sender:'person-a',sender_name:'小甲',at:stamp,revision:2,complete:true,text:'请解释小乙之前发的图片。',addressees:['person-b']},media:{id:'media',sender:'person-b',sender_name:'小乙',at:stamp,revision:1,complete:true,text:'这是今天的合成盒饭照片。',incoming_media:[{uploader_id:'person-b',observation_state:'verified',kind:'image'}]}};
let sourceMode='current';
let entry={id:'fact',subject:'person-a',kind:'fact',detail:'已更正：小甲的父亲刚结束视频通话。',status:'active',version:2,sources:[{event_id:'anchor',quote:sources.anchor.text}],assertions:[{subject:'person-a',subject_text:'小甲',predicate:'father',object:'relative:person-a:father',object_basis:'relative',object_text:'父亲',asserted_by:'person-b',state:'corrected'}]};
let editableEntry={id:'editable',subject:'person-a',kind:'fact',detail:'小甲喜欢红茶。',status:'active',version:2,sources:[{event_id:'anchor',quote:sources.anchor.text}]};
let memoryEntry='relation';
let settings={enabled:false,batch_messages:12,idle_seconds:300,daily_tokens:40000,recent_tokens:4096,recall_tokens:4096,continuity_tokens:1024,context_window_tokens:65536,output_reserve_tokens:4096,reply_audit:{enabled:true,snapshots:true,retention_days:19}};
let embedding={base_url:'https://embedding.example.invalid/v1',model:'synthetic-embedding',revision:'r7',dimensions:768};
let maintenance={base_url:'https://memory.example.invalid/v1',model:'synthetic-maintainer',provider:'openai-compatible'};
const evidence={scope,items:[{id:'audit',state:'reviewed',at:stamp,sources:[{scope,event_id:'anchor',revision:2}],contexts:[{target_ids:['person-a'],anchor_event_id:'anchor',source_event_ids:['anchor'],media_source_ids:['media'],source_versions:{anchor:2,media:1},quote_required:true,risk_flags:['cross_person_media']}]}],has_more:false,next_offset:1};
const baselineAudit=JSON.parse(JSON.stringify(evidence.items[0]));
async function reply(route,data,options={}){await route.fulfill({json:data,...options})}
(async()=>{
 await fs.mkdir(output,{recursive:true});
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 const results=[],errors=[];
 try{
  const context=await browser.newContext({viewport:{width:1440,height:900},reducedMotion:'reduce'}),page=await context.newPage();
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const req=route.request(),u=new URL(req.url());assert.equal(u.origin,origin,'external request forbidden');requests.push({path:u.pathname,search:u.search,method:req.method()});
   if(u.pathname.startsWith('/api/')){
    if(req.method()!=='GET'){
     const body=req.postDataJSON();writes.push({path:u.pathname,body});
     if(u.pathname==='/api/memory/services/automatic'){assert.deepEqual(body,{enabled:body.enabled},'daily switch must not submit advanced settings');settings={...settings,enabled:body.enabled};return reply(route,{settings,embedding,maintenance,embedding_key_configured:true,maintenance_key_configured:true,semantic_configured:true})}
     if(u.pathname==='/api/memory/services'){settings=body.settings;embedding={...body.embedding,api_key:undefined};maintenance={...body.maintenance,api_key:undefined};return reply(route,{settings,embedding,maintenance,embedding_key_configured:true,maintenance_key_configured:true,semantic_configured:true})}
     if(u.pathname===`/api/memory/${scope}/entries/editable`&&body.action==='correct'){assert.equal(body.expected_revision,2);assert.equal(body.sources[0].event_id,'anchor');editableEntry={...editableEntry,detail:body.detail,version:3};return reply(route,editableEntry)}
     throw Error('unexpected synthetic write '+u.pathname);
    }
    if(u.pathname==='/api/memory/services')return reply(route,{settings,embedding,maintenance,embedding_key_configured:true,maintenance_key_configured:true,semantic_configured:true});
    if(u.pathname.startsWith(`/api/companion/memory/${scope}/`))return reply(route,{items:[],next_cursor:''});
    if(u.pathname==='/api/reply-audits'&&u.searchParams.get('message_id')==='no-evidence')return reply(route,{scope,items:[],has_more:false,next_offset:0});
    if(u.pathname.endsWith('/reply-audits')||u.pathname==='/api/reply-audits')return reply(route,evidence);
    if(u.pathname.startsWith(`/api/memory/${scope}/sources/`)){
     const id=u.pathname.split('/').pop();
     if(sourceMode==='forgotten'&&id==='media')return reply(route,{error:'原始来源已不可用或已遗忘'}, {status:404});
     if(sourceMode==='changed'&&id==='anchor')return reply(route,{error:'原始来源已更新，请从当前记录重新读取'}, {status:409});
     const result={...sources[id]};
     return reply(route,result);
    }
    if(u.pathname===`/api/memory/${scope}`)return reply(route,{status:{events:2,entries:1,vectors:0,jobs:[]},page:{items:[{entry:memoryEntry==='relation'?entry:editableEntry}],has_more:false,degraded:true}});
    if(u.pathname==='/api/logs')return reply(route,{items:[],complete:true});
    throw Error('unexpected synthetic read '+u.pathname);
   }
   const name=u.pathname==='/'?'index.html':u.pathname.split('/').pop();let body=await fs.readFile(path.join(root,name));
   if(name==='index.html')body=body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,'');
   await route.fulfill({body,contentType:({'.html':'text/html','.css':'text/css','.js':'text/javascript','.png':'image/png','.svg':'image/svg+xml'})[path.extname(name)]||'application/octet-stream'});
  });
  await page.goto(origin);
  await page.evaluate(({names})=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   window.WechatNames={display:(id,fallback)=>names[id]||fallback||'群成员'};
   window.WechatAdmin={api:async(p,o={})=>{const r=await fetch(p,{method:o.method||'GET',headers:{'Content-Type':'application/json'},body:o.json?JSON.stringify(o.json):undefined}),body=await r.json();if(!r.ok){const error=Error(body.error||'synthetic HTTP '+r.status);error.status=r.status;throw error}return body},notice:()=>{}};
  },{names});
  for(const file of ['controls.js','refresh.js','rounds.js','dialogs.js','logs.js','memory.js','save-actions.js'])await page.addScriptTag({url:origin+'/assets/'+file});
  const topDialog=()=>page.locator('dialog[open]').last();
  const shot=async(name)=>page.screenshot({path:path.join(output,name+'.png')});
  const openRound=(message='synthetic-message')=>page.evaluate(id=>{
   const [round]=window.WechatRounds.project([{event:'message_received',category:'received',content:'合成消息',message_id:id,thread_id:'synthetic@chatroom',time:'2026-10-30T08:00:00Z'}]);
   window.WechatActivity.openRound(round);
  },message);
  await openRound();
  await topDialog().getByRole('heading',{name:'旧版模型诊断已完成',exact:true}).waitFor();
  assert.equal(await page.locator('dialog[open]').count(),1);
  assert.equal(await topDialog().getByRole('button',{name:'回复依据',exact:true}).count(),0);
  assert(requests.some(r=>r.path==='/api/reply-audits'&&new URLSearchParams(r.search).get('message_id')==='synthetic-message'),'execution dialog must preserve trigger message');
  await page.keyboard.press('Escape');
  const emptyResponse=page.waitForResponse(r=>r.url().includes('message_id=no-evidence'));
  await openRound('no-evidence');await emptyResponse;
  await topDialog().getByText('合成消息',{exact:true}).waitFor();
  assert.equal(await topDialog().locator('.conversation-summary:visible').count(),0);
  await topDialog().getByText('这次互动没有可核对的来源记录。',{exact:true}).waitFor();
  await page.keyboard.press('Escape');
  for(const [state,title] of [['checked','旧版模型诊断已完成'],['repaired_and_checked','旧版诊断后草稿已校验'],['final_check_rejected','核验未通过，已停止发送'],['review_failed','核验未通过，已停止发送']]){
   evidence.items[0].state=state;await openRound();await topDialog().getByRole('heading',{name:title,exact:true}).waitFor();await page.keyboard.press('Escape');
  }
  evidence.items[0].state='reviewed';
  // Older snapshots can have an HTTP response without a stored attempt count.
  evidence.items[0].review_failure={review_state:'initial_check_failed',stage:'model_decode',code:'legacy_fixture',request:{HTTPStatus:200,JSONValid:false}};
  await openRound();
  await topDialog().getByText('HTTP 200，JSON 校验未通过',{exact:true}).waitFor();
  await topDialog().getByText('复核代码：legacy_fixture',{exact:true}).waitFor();
  await page.keyboard.press('Escape');
  delete evidence.items[0].review_failure;
  for(const viewport of [{width:1440,height:900},{width:375,height:812}])for(const theme of ['light','dark']){
   await page.setViewportSize(viewport);await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   await openRound();
   await topDialog().getByRole('heading',{name:'旧版模型诊断已完成',exact:true}).waitFor();
   assert((await topDialog().textContent()).includes('回复对象：小甲'));
   const sourceLinks=topDialog().locator('.reply-source-link');
   assert.equal(await sourceLinks.count(),2,'source roles must deduplicate a repeated anchor');
   assert.match((await sourceLinks.nth(0).innerText())+(await sourceLinks.nth(1).innerText()),/触发消息/);
   assert.match((await sourceLinks.nth(0).innerText())+(await sourceLinks.nth(1).innerText()),/媒体来源/);
   await shot(`evidence-${theme}-${viewport.width}`);
   await sourceLinks.first().click();
   await topDialog().getByText('对话对象：小乙',{exact:true}).waitFor();assert((await topDialog().textContent()).includes('版本 2'));await page.keyboard.press('Escape');
   assert(requests.some(r=>r.path===`/api/memory/${scope}/sources/anchor`&&new URLSearchParams(r.search).get('revision')==='2'),'audit source must request the recorded version');
   await sourceLinks.last().click();
   await topDialog().getByText('媒体上传者：小乙 · 已读取',{exact:true}).waitFor();assert(!(await topDialog().textContent()).includes('undefined'));
   await shot(`source-${theme}-${viewport.width}`);await page.keyboard.press('Escape');await page.keyboard.press('Escape');
   await page.evaluate(()=>window.WechatMemory.open('synthetic-scope'));
   await topDialog().getByRole('button',{name:'全部记忆',exact:true}).click();
   await topDialog().getByText('小甲 · 父亲 · 小甲的父亲',{exact:true}).waitFor();await topDialog().getByText('已更正',{exact:true}).waitFor();
   assert.equal(await topDialog().locator('details').count(),0,'small evidence must not be folded');await shot(`relation-${theme}-${viewport.width}`);await page.keyboard.press('Escape');
   await page.evaluate(()=>window.WechatMemory.settings());await topDialog().getByLabel('自动整理长期记忆',{exact:true}).waitFor();
   assert.equal(await topDialog().getByLabel('自动整理长期记忆',{exact:true}).count(),1,'daily switch is the only default setting');
   const advanced=topDialog().locator('details.memory-settings-advanced');
   assert.equal(await advanced.count(),1,'advanced settings are collapsed by default');assert.equal(await advanced.evaluate(d=>d.open),false);
   const fitsWidth=()=>topDialog().evaluate(d=>[d,d.querySelector('.dialog-body')].every(el=>el.scrollWidth<=el.clientWidth+2));
   assert(await fitsWidth());
   await shot(`settings-${theme}-${viewport.width}`);
   await advanced.locator('summary').click();
   assert(await fitsWidth(),'advanced settings must fit even at narrow widths');
   await shot(`settings-advanced-${theme}-${viewport.width}`);
   await page.keyboard.press('Escape');
   results.push({width:viewport.width,theme,evidence:true,mediaSource:true,relationObject:true,topSave:true,horizontalOverflow:false});
  }
  // The audit carries the revision used in this interaction. A later edit must
  // never be displayed as its evidence, and an erased source remains unavailable.
  sourceMode='changed';
  await openRound();
  await topDialog().locator('.reply-source-link').filter({hasText:'触发消息'}).click();
  await topDialog().getByText('原始来源已更新，请从当前记录重新读取',{exact:true}).waitFor();
  assert.equal(await topDialog().getByText('这是一段后续改写的原话，不能作为旧回合的依据。',{exact:true}).count(),0);
  await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  sourceMode='forgotten';
  await openRound();
  await topDialog().locator('.reply-source-link').filter({hasText:'媒体来源'}).click();
  await topDialog().getByText('原始来源已不可用或已遗忘',{exact:true}).waitFor();
  await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  sourceMode='current';
  // Some prior audits only stored their local source index. It remains useful
  // when scoped to this conversation, while a different conversation's index
  // cannot appear here. No-version history remains readable but is labelled.
  evidence.items[0]={...baselineAudit,contexts:[],sources:[{scope,event_id:'media',revision:1}]};
  await openRound();
  await topDialog().locator('.reply-source-link').first().waitFor();
  assert.equal(await topDialog().locator('.reply-source-link').count(),1);
  await topDialog().getByRole('button',{name:/上下文来源/}).click();
  await topDialog().getByText('媒体上传者：小乙 · 已读取',{exact:true}).waitFor();
  await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  evidence.items[0]={...baselineAudit,contexts:[],sources:[{scope:'other-scope',event_id:'anchor',revision:2}]};
  await openRound();
  await topDialog().getByText('这次互动没有可核对的来源记录。',{exact:true}).waitFor();
  assert.equal(await topDialog().locator('.reply-source-link').count(),0,'cross-scope audit source cannot be opened');
  await page.keyboard.press('Escape');
  evidence.items[0]={...baselineAudit,contexts:[],sources:[{scope,event_id:'anchor'}]};
  await openRound();
  await topDialog().getByRole('button',{name:/上下文来源/}).click();
  await topDialog().getByText(/未记录当时版本，以下为当前原文/).waitFor();
  assert(requests.some(r=>r.path===`/api/memory/${scope}/sources/anchor`&&!new URLSearchParams(r.search).has('revision')),'old audit without a version must not pretend to have one');
  await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  evidence.items[0]=baselineAudit;
  // The day-to-day control is intentionally separate: it must not race a
  // concurrent advanced-edit form or overwrite its custom budget, audit data,
  // endpoint model, or already-configured private keys.
  await page.setViewportSize({width:1440,height:900});await page.evaluate(()=>window.WechatMemory.settings());
  const daily=topDialog().getByLabel('自动整理长期记忆',{exact:true});await daily.waitFor();assert.equal(await daily.isChecked(),false);
  const budget=topDialog().getByLabel('自动维护每日 Token 预算（0 为不限额）',{exact:true});
  assert.equal(await budget.inputValue(),'40000');
  settings={...settings,daily_tokens:54321}; // Another session saves after the dialog opened.
  await daily.check();const automaticResponse=page.waitForResponse(r=>r.url()===origin+'/api/memory/services/automatic'&&r.request().method()==='POST');await topDialog().getByRole('button',{name:'保存记忆设置',exact:true}).click();await automaticResponse;
  assert.deepEqual(writes.at(-1),{path:'/api/memory/services/automatic',body:{enabled:true}});
  assert.equal(settings.daily_tokens,54321,'daily save must preserve concurrent advanced budget');assert.equal(settings.reply_audit.retention_days,19,'daily save must preserve diagnostic retention');assert.equal(embedding.model,'synthetic-embedding');assert.equal(maintenance.model,'synthetic-maintainer');
  await topDialog().getByText('已保存，后续维护与召回使用新设置。',{exact:true}).waitFor();assert.equal(await budget.inputValue(),'54321');await page.keyboard.press('Escape');
  await page.evaluate(()=>window.WechatMemory.settings());await topDialog().getByLabel('自动整理长期记忆',{exact:true}).waitFor();assert.equal(await topDialog().getByLabel('自动整理长期记忆',{exact:true}).isChecked(),true,'daily switch must read back saved state');
  const advanced=topDialog().locator('details.memory-settings-advanced');assert.equal(await advanced.evaluate(d=>d.open),false);await advanced.locator('summary').click();assert.equal(await advanced.evaluate(d=>d.open),true);
  for(const heading of ['整理节奏与预算','聊天上下文','回复诊断','语义检索','独立维护模型'])await topDialog().getByRole('heading',{name:heading,exact:true}).waitFor();
  assert.equal(await topDialog().getByLabel('批量整理消息数',{exact:true}).inputValue(),'12');assert.equal(await topDialog().getByLabel('自动维护每日 Token 预算（0 为不限额）',{exact:true}).inputValue(),'54321');assert.equal(await topDialog().getByLabel('诊断记录保留天数',{exact:true}).inputValue(),'19');
  await topDialog().getByLabel('嵌入密钥（已保存，留空保留）',{exact:true}).waitFor();await topDialog().getByLabel('维护密钥（已保存，留空保留）',{exact:true}).waitFor();
  await topDialog().getByLabel('批量整理消息数',{exact:true}).fill('14');await topDialog().getByLabel('诊断记录保留天数',{exact:true}).fill('9');const fullSaveResponse=page.waitForResponse(r=>r.url()===origin+'/api/memory/services'&&r.request().method()==='POST');await topDialog().getByRole('button',{name:'保存记忆设置',exact:true}).click();await fullSaveResponse;await topDialog().getByText('已保存，后续维护与召回使用新设置。',{exact:true}).waitFor();
  assert.equal(writes.at(-1).path,'/api/memory/services');assert.equal(writes.at(-1).body.settings.enabled,true);assert.equal(writes.at(-1).body.settings.batch_messages,14);assert.equal(writes.at(-1).body.settings.daily_tokens,54321);assert.equal(writes.at(-1).body.settings.reply_audit.retention_days,9);assert.equal(writes.at(-1).body.embedding.api_key,'');assert.equal(writes.at(-1).body.maintenance.api_key,'');await page.keyboard.press('Escape');
  memoryEntry='editable';await page.evaluate(()=>window.WechatMemory.open('synthetic-scope'));await topDialog().getByRole('button',{name:'全部记忆',exact:true}).click();await topDialog().getByRole('button',{name:'更正',exact:true}).click();await topDialog().getByLabel('更正内容',{exact:true}).fill('合成更正内容');await topDialog().getByLabel('新来源事件编号',{exact:true}).fill('anchor');await topDialog().getByLabel('该来源中的精确原句',{exact:true}).fill(sources.anchor.text);await topDialog().getByRole('button',{name:'保存更正',exact:true}).click();await page.waitForFunction(()=>document.querySelector('dialog[open]')?.textContent.includes('合成更正内容'));assert.equal(writes.at(-1).body.expected_revision,2);
  assert.deepEqual(errors,[]);
  const report={syntheticOnly:true,browser:browser.version(),platform:process.platform,syntheticHarness:true,realWechatSends:0,providerCalls:0,privateConfigReads:0,actualStaticComponents:['controls.js','dialogs.js','logs.js','memory.js','save-actions.js'],results,checkerStatusLabels:true,executionEvidenceLink:true,sourceRolesAndDeduplication:true,sourceAddresseeAndVersion:true,changedAndForgottenSourcesHidden:true,legacyAuditSourceScopeIsolation:true,legacyUnknownRevisionLabel:true,settingsSaved:true,automaticOnlySave:true,advancedReadBack:true,configuredKeysFlags:true,correctionVersionChecked:true,pageErrors:errors,requests,writes};
  await fs.writeFile(path.join(output,'report.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
