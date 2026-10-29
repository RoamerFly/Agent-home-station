/* Opt-in browser acceptance: synthetic records, mocked writes, zero WeChat sends. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['localhost','127.0.0.1','[::1]'].includes(new URL(base).hostname));
const prose='这是一段需要完整保留的业务正文，包含来源、处理过程与发送结果。🐳\n'.repeat(140)+'【正文末尾完整保留】';
const filename='一份名字很长的角色使用说明与项目文档'.repeat(7)+'.md';
const scope={id:'synthetic-room',thread:'synthetic@chatroom',label:'合成测试群：'+('长名称'.repeat(15)),message_count:60,last_interaction:'2026-10-29T08:00:00Z'};
const log=(id,content,event='message_received')=>({id,time:'2026-10-29T08:00:00Z',message_id:'synthetic-message',thread_id:scope.thread,source:scope.label,source_kind:'group',category:event==='message_received'?'received':'sent',event,status:event==='message_received'?'received':'accepted',content});
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password);
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:900}});assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const config=await(await context.request.get(base+'/api/config')).json(),agent=await(await context.request.get(base+'/api/agent')).json();
  const page=await context.newPage(),errors=[],writes=[],screens=process.env.WECHAT_ADMIN_UI_SCREENSHOTS;let documentText=prose,revision='synthetic-r1';
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url());if(url.origin!==base)return route.abort();
   if(url.pathname.startsWith('/api/')){
    if(url.pathname==='/api/accounts')return route.fulfill({json:{items:[{id:'synthetic-account',name:'蓝色大肥鱼 · 合成账号',native:{connection:'online'},bot:{state:'running'}}],primary:'synthetic-account',supported:true}});
    if(url.pathname==='/api/status')return route.fulfill({json:{native:{authenticated:true,connection:'online'},bot:{state:'running'},account_name:'蓝色大肥鱼 · 合成账号',key_configured:true}});
    if(url.pathname==='/api/companion/state')return route.fulfill({json:{scopes:[scope]}});
    if(url.pathname==='/api/companion/memory/'+scope.id)return route.fulfill({json:{...scope,notes:[],messages:[{message_id:'synthetic-message',role:'user',sender:'synthetic-user',sender_name:'合成用户',time:'2026-10-29T08:00:00Z',text:prose}]}});
    if(url.pathname==='/api/logs')return route.fulfill({json:{items:[log('receive',prose),log('sent','这是完整发送内容。','send_accepted'),{...log('tool','', 'tool_call_failed'),category:'error',status:'failed',tool:'server_exec',reason:'合成执行器不可用',tool_code:'synthetic_engine_error',tool_stage:'请求解析'}, {...log('effect','', 'tool_call_succeeded'),category:'tool',status:'succeeded',tool:'server_write',effect:'已创建文件：会话根目录/output/result.txt'}]}});
    if(url.pathname==='/api/agent')return route.fulfill({json:{...agent,tasks:[{id:'synthetic-task',title:'整理项目资料',state:'waiting',checkpoint:prose,thread:scope.thread,runs:1,next:'2026-10-29T10:00:00Z'}],operations:[],history_count:0}});
    if(url.pathname==='/api/agent/workspace'){
     if(request.method()==='POST'){const body=request.postDataJSON();writes.push({path:url.pathname,body});assert.equal(body.revision,revision);documentText=body.content;revision='synthetic-r2';return route.fulfill({json:{name:body.name,revision,content:documentText}})}
     if(url.searchParams.has('name'))return route.fulfill({json:{name:url.searchParams.get('name'),content:documentText,revision}});
     return route.fulfill({json:{documents:[{name:'MEMORY.md'},{name:filename}],processes:[{id:'synthetic-process',state:'completed',started:'2026-10-29T08:00:00Z',output:prose}],layout:{backend:'合成隔离环境',private_workspace:'/synthetic/private',public_workspace:'/synthetic/public'}}});
    }
    if(url.pathname==='/api/voice-library')return route.fulfill({json:{items:[{id:'synthetic-voice',name:'曼波',role:'曼波',engine:'RVC',status:'合成测试数据',library_scope:'public',notes:prose,weights:[],references:[],previews:[]}]}});
    if(url.pathname==='/api/config'){
     if(request.method()==='GET')return route.fulfill({json:config});const body=request.postDataJSON();writes.push({path:url.pathname,body});return route.fulfill({json:{...body,key_configured:config.key_configured}});
    }
    return request.method()==='GET'?route.continue():route.fulfill({status:403,json:{error:'合成界面验收禁止真实写入'}});
   }
   const file=path.join(__dirname,'../internal/admin/static',url.pathname==='/'?'index.html':url.pathname.split('/').pop());
   try{return route.fulfill({body:await fs.readFile(file),contentType:({'.html':'text/html','.css':'text/css','.js':'text/javascript','.png':'image/png','.json':'application/json'})[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base);await page.locator('#overview.active-page').waitFor();assert.equal(await page.locator('.main-nav').count(),5);
  await page.locator('#overview-tasks').getByText('整理项目资料',{exact:true}).waitFor();
  assert((await page.locator('#overview-tasks').textContent()).includes('【正文末尾完整保留】'));assert.equal(await page.locator('#overview-tasks p').first().evaluate(e=>getComputedStyle(e).webkitLineClamp),'3');
  // Full documents, save revision and unsaved edits across navigation/refresh.
  await page.evaluate(()=>window.WechatAdmin.nav('agent'));await page.locator('#tab-documents').click();await page.locator('.document-file').first().waitFor();
  await page.locator('.document-file').filter({hasText:filename}).click();await page.waitForFunction(text=>document.querySelector('#agent-doc-content').value===text,prose);
  assert.equal(await page.locator('#agent-doc-content').inputValue(),prose);
  await page.locator('#agent-doc-content').fill(prose+'\n新的草稿');await page.locator('#agent-doc-save').click();await page.getByText('文档已保存',{exact:true}).waitFor();
  assert.equal(writes[0].body.content,prose+'\n新的草稿');assert.equal(writes[0].body.revision,'synthetic-r1');
  await page.locator('#agent-doc-content').fill('未保存的草稿必须保留');await page.evaluate(()=>window.WechatAdmin.nav('overview'));await page.evaluate(()=>window.WechatAdmin.nav('agent'));await page.waitForTimeout(250);
  assert.equal(await page.locator('#agent-doc-content').inputValue(),'未保存的草稿必须保留');
  // Model settings keep execution/recovery policy fields untouched.
  await page.evaluate(()=>window.WechatAdmin.nav('companion'));await page.locator('#companion-name').fill(config.companion.name+' · 测试');await page.locator('#save-companion').click();await page.getByText('配置已保存并应用。',{exact:true}).waitFor();
  const saved=writes.find(x=>x.path==='/api/config').body;assert.deepEqual(saved.behavior,config.behavior);assert.deepEqual(saved.group_rules,config.group_rules);
  await page.evaluate(()=>window.WechatAdmin.nav('memory'));await page.locator('.conversation-open').click();await page.locator('#memory-content .memory-message-body').waitFor();assert.equal(await page.locator('#memory-content .long-content-preview').textContent(),prose);
  await page.locator('#memory-content .long-content').click();assert((await page.locator('dialog.console-dialog .readable-full-text').textContent()).includes('【正文末尾完整保留】'));await page.keyboard.press('Escape');await page.locator('#memory-refresh').click();await page.waitForTimeout(250);assert(await page.locator('#memory-content .long-content').isVisible());
  await page.evaluate(()=>window.WechatAdmin.nav('logs'));await page.locator('.round-summary').first().click();assert(await page.locator('dialog.console-dialog').isVisible());assert((await page.locator('dialog.console-dialog').textContent()).includes('调用工具 · 在隔离环境执行命令'));assert((await page.locator('dialog.console-dialog').textContent()).includes('失败原因：合成执行器不可用'));assert((await page.locator('dialog.console-dialog').textContent()).includes('会话根目录/output/result.txt'));
  assert.equal(await page.locator('dialog.console-dialog .technical-json:visible').count(),0,'raw code leaked into normal reading');
  await page.locator('dialog.console-dialog .long-content').first().click();assert((await page.locator('dialog.console-dialog').last().textContent()).includes('【正文末尾完整保留】'));await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  // Long names in custom selectors remain complete and keyboard operable.
  await page.evaluate(()=>{const s=document.querySelector('#log-status');s.add(new Option('长选项'.repeat(60),'long'));s.dispatchEvent(new Event('change'))});
  await page.locator('#log-status+.select-trigger').click();const last=page.locator('.select-option').last();assert.equal((await last.textContent()).length,180);await page.keyboard.press('End');await page.keyboard.press('Escape');assert.equal(await page.locator('.select-popup').count(),0);
  const pages=['overview','agent','workbench','memory','logs','surf','companion','voices','wechat','provider','plugins','mcp','permissions','mobile','settings','rules','catalog','tools','account-actions'];
  for(const theme of ['light','dark'])for(const viewport of [{width:375,height:812},{width:768,height:1024},{width:1024,height:768},{width:1440,height:900},{width:1920,height:1080}]){
   await page.setViewportSize(viewport);await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   for(const id of pages){await page.evaluate(s=>window.WechatAdmin.nav(s),id);await page.waitForTimeout(30);const result=await page.evaluate(()=>{const p=document.querySelector('.content>.active-page');return {horizontal:document.documentElement.scrollWidth>innerWidth+2||p.scrollWidth>p.clientWidth+2,bodyHeight:document.body.scrollHeight>innerHeight+2}});assert(!result.horizontal,`${theme}/${viewport.width}/${id}: horizontal overflow`);assert(!result.bodyHeight,`${theme}/${viewport.width}x${viewport.height}/${id}: unbounded document`)}
  }
  await page.setViewportSize({width:512,height:384});for(const id of ['overview','agent','memory','logs','plugins','provider']){await page.evaluate(s=>window.WechatAdmin.nav(s),id);assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2)),id+' at 200% equivalent viewport');}
  await page.setViewportSize({width:375,height:812});await page.evaluate(()=>window.WechatAdmin.nav('overview'));await page.locator('#menu-toggle').click();assert(await page.locator('#drawer-scrim').isVisible());await page.locator('.main-nav[data-section="memory"]').click();assert(await page.locator('#memory').isVisible());assert(!(await page.locator('#drawer-scrim').isVisible()));
  if(screens){await fs.mkdir(screens,{recursive:true});await page.setViewportSize({width:1440,height:900});for(const theme of ['dark','light']){await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);for(const id of ['overview','agent','memory','logs','voices']){await page.evaluate(s=>window.WechatAdmin.nav(s),id);await page.waitForTimeout(180);await page.screenshot({path:path.join(screens,`${id}-${theme}.png`)})}}}
  assert.deepEqual(errors,[]);console.log(JSON.stringify({fiveNavigationGroups:true,fullBusinessText:true,boundedPanes:true,documentRevision:true,editorDraftPreserved:true,conversationRefreshPreserved:true,roundModal:true,rawDataLast:true,settingsPreserved:true,themes:2,viewports:5,pages:19,pageErrors:errors,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
