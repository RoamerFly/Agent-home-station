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
let entry={id:'fact',subject:'person-a',kind:'fact',detail:'已更正：小甲的父亲刚结束视频通话。',status:'active',version:2,sources:[{event_id:'anchor',quote:sources.anchor.text}],assertions:[{subject:'person-a',subject_text:'小甲',predicate:'father',object:'synthetic-father',object_text:'小甲的父亲',asserted_by:'person-b',state:'corrected'}]};
let settings={enabled:true,batch_messages:12,idle_seconds:300,daily_tokens:100000,recent_tokens:4096,recall_tokens:4096,continuity_tokens:1024,context_window_tokens:65536,output_reserve_tokens:4096,reply_audit:{enabled:true,snapshots:true,retention_days:7}};
const evidence={scope,items:[{id:'audit',state:'reviewed',at:stamp,contexts:[{target_ids:['person-a'],anchor_event_id:'anchor',source_event_ids:['anchor'],media_source_ids:['media'],quote_required:true,risk_flags:['cross_person_media']}]}],has_more:false,next_offset:1};
async function reply(route,data){await route.fulfill({json:data})}
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
     if(u.pathname==='/api/memory/services'){settings=body.settings;return reply(route,{ok:true})}
     if(u.pathname===`/api/memory/${scope}/entries/fact`&&body.action==='correct'){assert.equal(body.expected_revision,2);assert.equal(body.sources[0].event_id,'anchor');entry={...entry,detail:body.detail,version:3};return reply(route,entry)}
     throw Error('unexpected synthetic write '+u.pathname);
    }
    if(u.pathname==='/api/memory/services')return reply(route,{settings,embedding:{},maintenance:{}});
    if(u.pathname.endsWith('/reply-audits')||u.pathname==='/api/reply-audits')return reply(route,evidence);
    if(u.pathname.startsWith(`/api/memory/${scope}/sources/`))return reply(route,sources[u.pathname.split('/').pop()]);
    if(u.pathname===`/api/memory/${scope}`)return reply(route,{status:{events:2,entries:1,vectors:0,jobs:[]},page:{items:[{entry}],has_more:false,degraded:true}});
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
   window.WechatAdmin={api:async(p,o={})=>{const r=await fetch(p,{method:o.method||'GET',headers:{'Content-Type':'application/json'},body:o.json?JSON.stringify(o.json):undefined});if(!r.ok)throw Error('synthetic HTTP '+r.status);return r.json()},notice:()=>{}};
  },{names});
  for(const file of ['refresh.js','rounds.js','dialogs.js','logs.js','memory.js','save-actions.js'])await page.addScriptTag({url:origin+'/assets/'+file});
  const topDialog=()=>page.locator('dialog[open]').last();
  const shot=async(name)=>page.screenshot({path:path.join(output,name+'.png')});
  await page.evaluate(()=>window.WechatActivity.openRound({source:'合成群',time:'2026-10-30T08:00:00Z',status:'accepted',system:false,thread_id:'synthetic@chatroom',received:[{message_id:'synthetic-message'}],sent:[],entries:[]}));
  await topDialog().getByRole('button',{name:'回复依据',exact:true}).click();await topDialog().getByRole('heading',{name:'已复核回复',exact:true}).waitFor();
  assert(requests.some(r=>r.path==='/api/reply-audits'&&new URLSearchParams(r.search).get('message_id')==='synthetic-message'),'execution dialog must preserve trigger message');
  await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  for(const [state,title] of [['checked','已核验回复'],['repaired_and_checked','修复后已核验'],['final_check_rejected','核验未通过，已停止发送'],['review_failed','核验未通过，已停止发送']]){
   evidence.items[0].state=state;await page.evaluate(()=>window.WechatMemory.showReplyEvidence({scope:'synthetic-scope'}));await topDialog().getByRole('heading',{name:title,exact:true}).waitFor();await page.keyboard.press('Escape');
  }
  evidence.items[0].state='reviewed';
  for(const viewport of [{width:1440,height:900},{width:375,height:812}])for(const theme of ['light','dark']){
   await page.setViewportSize(viewport);await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   await page.evaluate(()=>window.WechatMemory.showReplyEvidence({thread:'synthetic@chatroom',message:'synthetic-message'}));
   await topDialog().getByRole('heading',{name:'已复核回复',exact:true}).waitFor();
   assert((await topDialog().textContent()).includes('回复对象：小甲'));
   assert.equal(await topDialog().getByRole('button',{name:'查看参考消息',exact:true}).count(),2,'media source must be readable');
   await shot(`evidence-${theme}-${viewport.width}`);
   await topDialog().getByRole('button',{name:'查看参考消息',exact:true}).first().click();
   await topDialog().getByText('对话对象：小乙',{exact:true}).waitFor();assert((await topDialog().textContent()).includes('版本 2'));await page.keyboard.press('Escape');
   await topDialog().getByRole('button',{name:'查看参考消息',exact:true}).last().click();
   await topDialog().getByText('媒体上传者：小乙 · 已读取',{exact:true}).waitFor();assert(!(await topDialog().textContent()).includes('undefined'));
   await shot(`source-${theme}-${viewport.width}`);await page.keyboard.press('Escape');await page.keyboard.press('Escape');
   await page.evaluate(()=>window.WechatMemory.open('synthetic-scope'));
   await topDialog().getByText('小甲 · 父亲 · 小甲的父亲',{exact:true}).waitFor();await topDialog().getByText('已更正',{exact:true}).waitFor();
   assert.equal(await topDialog().locator('details').count(),0,'small evidence must not be folded');await shot(`relation-${theme}-${viewport.width}`);await page.keyboard.press('Escape');
   await page.evaluate(()=>window.WechatMemory.settings());await topDialog().getByLabel('保留回复依据',{exact:true}).waitFor();
   await page.waitForFunction(()=>document.querySelector('.dialog-save-actions button')?.textContent==='保存记忆设置');
   const overflow=await topDialog().evaluate(d=>({dialog:d.scrollWidth>d.clientWidth+2,body:d.querySelector('.dialog-body').scrollWidth>d.querySelector('.dialog-body').clientWidth+2}));assert.deepEqual(overflow,{dialog:false,body:false});
   if(viewport.width>760)assert(await topDialog().locator('.memory-settings-form').evaluate(f=>{const labels=[...f.querySelectorAll(':scope>label')];return Math.abs(labels[0].getBoundingClientRect().width-labels[1].getBoundingClientRect().width)<2&&labels[0].getBoundingClientRect().left<labels[1].getBoundingClientRect().left}),'desktop settings must use equal columns');
   assert.equal(await topDialog().getByLabel('保留天数',{exact:true}).inputValue(),'7');await shot(`settings-${theme}-${viewport.width}`);await page.keyboard.press('Escape');
   results.push({width:viewport.width,theme,evidence:true,mediaSource:true,relationObject:true,topSave:true,horizontalOverflow:false});
  }
  await page.evaluate(()=>window.WechatMemory.settings());await topDialog().getByLabel('保留天数',{exact:true}).fill('9');await topDialog().getByRole('button',{name:'保存记忆设置',exact:true}).click();await topDialog().getByText('已保存，后续维护与召回使用新设置。',{exact:true}).waitFor();assert.equal(writes.at(-1).body.settings.reply_audit.retention_days,9);await page.keyboard.press('Escape');
  await page.evaluate(()=>window.WechatMemory.open('synthetic-scope'));await topDialog().getByRole('button',{name:'更正',exact:true}).click();await topDialog().getByLabel('更正内容',{exact:true}).fill('合成更正内容');await topDialog().getByLabel('新来源事件编号',{exact:true}).fill('anchor');await topDialog().getByLabel('该来源中的精确原句',{exact:true}).fill(sources.anchor.text);await topDialog().getByRole('button',{name:'保存更正',exact:true}).click();await page.waitForFunction(()=>document.querySelector('dialog[open]')?.textContent.includes('合成更正内容'));assert.equal(writes.at(-1).body.expected_revision,2);
  assert.deepEqual(errors,[]);
  const report={syntheticOnly:true,browser:browser.version(),platform:process.platform,syntheticHarness:true,realWechatSends:0,providerCalls:0,privateConfigReads:0,actualStaticComponents:['dialogs.js','logs.js','memory.js','save-actions.js'],results,checkerStatusLabels:true,executionEvidenceLink:true,sourceAddresseeAndVersion:true,settingsSaved:true,correctionVersionChecked:true,pageErrors:errors,requests,writes};
  await fs.writeFile(path.join(output,'report.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
