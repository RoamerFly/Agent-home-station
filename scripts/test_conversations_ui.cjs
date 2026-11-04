/* Opt-in browser QA: synthetic conversations and compression responses only. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname));
const now='2026-10-27T04:00:00Z',scopes=[{id:'scope-a',thread:'a@chatroom',label:'合成群 A',message_count:5,visible_count:2,has_summary:true,last_interaction:now},{id:'scope-b',thread:'b@chatroom',label:'合成群 B',message_count:3,visible_count:3,last_interaction:now}];
const summary=focus=>JSON.stringify({version:1,current_focus:focus,people:[],topics:[],media:[],pending_tasks:[],preferences:[],corrections:[]});
const message=(id,text,time)=>({role:'user',message_id:id,sender:'fixture',sender_name:'合成用户',text,time});
const data={
 'scope-a':{...scopes[0],summary:summary('A 摘要'),summary_updated:'2026-10-27T03:00:00Z',history_count:3,compressed_count:2,retained_context_count:1,notes:[],messages:[message('a-new','A 最新','2026-10-27T03:20:00Z'),message('a-previous','A 次新','2026-10-27T03:10:00Z')]},
 'scope-b':{...scopes[1],history_count:0,compressed_count:0,notes:[],messages:[message('b-3','B 最新','2026-10-27T03:30:00Z'),message('b-2','B 次新','2026-10-27T03:20:00Z'),message('b-1','B 最早','2026-10-27T03:10:00Z')]}
};
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password);
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const page=await context.newPage(),errors=[],pending=new Map();let historyReads=0,compressions=0;page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());if(url.origin!==base)return route.abort();
   if(url.pathname==='/api/companion/state')return route.fulfill({json:{scopes}});
   if(url.pathname==='/api/logs')return route.fulfill({json:{items:[{id:'log-b',time:'2026-10-27T03:30:00Z',thread_id:'b@chatroom',message_id:'b-3',source:'合成群 B',source_kind:'group',category:'received',event:'message_received',content:'B 最新',status:'received'}]}});
   const m=url.pathname.match(/^\/api\/companion\/memory\/(scope-[ab])(\/history|\/compress)?$/);
   if(m){const id=m[1];if(m[2]==='/compress'){compressions++;pending.set(id,route);return}if(m[2]==='/history'){historyReads++;return route.fulfill({json:{messages:[message('old-2','仅历史弹窗可见的旧消息二','2026-10-27T02:02:00Z'),message('old-1','仅历史弹窗可见的旧消息一','2026-10-27T02:01:00Z')],total:2,compressed_count:1,retained_context_count:1,next_offset:null,revision:'fixture'}})}return route.fulfill({json:data[id]})}
   if(url.pathname.startsWith('/api/'))return req.method()==='GET'?route.continue():route.fulfill({status:403,json:{error:'测试阻止真实写入'}});
   const filename=url.pathname==='/'?'index.html':url.pathname.split('/').pop(),file=path.join(__dirname,'../internal/admin/static',filename);
   try{return route.fulfill({body:await fs.readFile(file),contentType:({'.html':'text/html','.js':'text/javascript','.css':'text/css','.png':'image/png'})[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base+'/#memory');await page.locator('.conversation-row[data-scope-id="scope-a"]').waitFor();assert(await page.locator('.main-nav[data-section="memory"]').count());
  await page.locator('[data-scope-id="scope-a"] .conversation-open').click();await page.getByText('A 摘要',{exact:true}).waitFor();
  await page.locator('#context-summary-view').click();const saved=page.getByRole('dialog',{name:'合成群 A · 压缩摘要'});await saved.getByText('A 摘要',{exact:true}).waitFor();await saved.locator('.dialog-close').click();
  assert.equal(historyReads,0);assert(!(await page.locator('#memory-content').textContent()).includes('旧消息'));
  assert.deepEqual(await page.locator('#memory-content .memory-message-body').allTextContents(),['A 最新','A 次新']);
  await page.locator('#memory-history').click();await page.getByText('仅历史弹窗可见的旧消息二',{exact:true}).waitFor();assert.equal(historyReads,1);assert.equal(await page.locator('dialog[open]').count(),1);
  await page.locator('dialog[open]').last().locator('.dialog-close').click();assert.equal(await page.evaluate(()=>document.activeElement.id),'memory-history');
  await page.locator('#context-compress-now').click();await page.waitForFunction(()=>document.querySelector('#context-compress-now').disabled);
  await page.evaluate(()=>document.getElementById('conversation-close').click());
  assert(await page.locator('[data-scope-id="scope-b"] .conversation-row-actions button').isEnabled());
  await page.locator('[data-scope-id="scope-b"] .conversation-open').click();await page.getByText('B 最新',{exact:true}).waitFor();
  assert.equal(await page.locator('#memory-executions,.conversation-process').count(),0,'execution rounds still duplicated in conversation detail');
  await page.locator('#context-compress-now').click();await page.waitForTimeout(100);assert.equal(compressions,2,'B waited behind A');
  await pending.get('scope-a').fulfill({json:{compressed:true,scope_id:'scope-a'}});await page.waitForTimeout(100);
  assert((await page.locator('#memory-content').textContent()).includes('B 最新'));assert(!(await page.locator('#memory-content').textContent()).includes('A 摘要'));assert(await page.locator('#context-compress-now').isDisabled());
  data['scope-b']={...data['scope-b'],messages:[],summary:summary('B 压缩后的摘要'),summary_updated:'2026-10-27T04:00:01Z',history_count:3,compressed_count:2,retained_context_count:1};
  await pending.get('scope-b').fulfill({json:{compressed:true,scope_id:'scope-b'}});await page.getByText('B 压缩后的摘要',{exact:true}).waitFor();assert(await page.locator('#context-compress-now').isEnabled());assert.equal(await page.locator('#memory-content .memory-message').count(),0);
  await page.locator('#context-compress-now').click();await page.waitForFunction(()=>document.querySelector('#context-compress-now').disabled);
  for(let i=0;i<100&&compressions<3;i++)await page.waitForTimeout(20);assert.equal(compressions,3);
  await pending.get('scope-b').fulfill({status:502,json:{error:'合成测试：模型摘要输出已截断'}});
  await page.locator('#context-compress-state').getByText('合成测试：模型摘要输出已截断').waitFor();
  await page.locator('#context-summary-view').click();const previous=page.getByRole('dialog',{name:'合成群 B · 压缩摘要'});await previous.getByText('B 压缩后的摘要',{exact:true}).waitFor();await previous.locator('.dialog-close').click();
  for(const width of [375,768,1440]){await page.setViewportSize({width,height:900});assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1)));assert(!(await page.locator('#conversation-inspector').evaluate(e=>e.scrollWidth>e.clientWidth+1)))}
  await fs.mkdir('research/evidence/conversation-workspace',{recursive:true});await page.screenshot({path:'research/evidence/conversation-workspace/dialog-light.png'});
  await page.evaluate(()=>document.documentElement.dataset.theme='dark');await page.screenshot({path:'research/evidence/conversation-workspace/dialog-dark.png'});
  await page.evaluate(()=>document.getElementById('conversation-close').click());await page.screenshot({path:'research/evidence/conversation-workspace/index-dark.png'});
  assert.deepEqual(errors,[]);console.log(JSON.stringify({separateExecutionPage:true,descendingMessages:true,historyReadOnDemand:true,splitTranscript:true,historyDialogs:true,focusRestored:true,parallelCompression:true,noCrossConversationCompletion:true,compressedViewHidesOldMessages:true,widths:[375,768,1440],pageErrors:errors,realModelCalls:0,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
