/* Opt-in browser acceptance: synthetic records, mocked writes, zero WeChat sends. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['localhost','127.0.0.1','[::1]'].includes(new URL(base).hostname));
const prose='这是一段需要完整保留的业务正文，包含来源、处理过程与发送结果。🐳\n'.repeat(140)+'【正文末尾完整保留】';
const filename='一份名字很长的角色使用说明与项目文档'.repeat(7)+'.md';
const scope={id:'synthetic-room',thread:'synthetic@chatroom',label:'合成测试群：'+('长名称'.repeat(15)),message_count:60,last_interaction:'2026-10-29T08:00:00Z'};
const dateKey=new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Shanghai'}).format(new Date());
const log=(id,content,event='message_received')=>({id,time:dateKey+'T08:00:00Z',message_id:'synthetic-message',thread_id:scope.thread,source:scope.label,source_kind:'group',category:event==='message_received'?'received':'sent',event,status:event==='message_received'?'received':'accepted',content});
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password);
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});let context,page;
 try{
  context=await browser.newContext({viewport:{width:1440,height:900}});assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const config=await(await context.request.get(base+'/api/config')).json(),agent=await(await context.request.get(base+'/api/agent')).json();
  // Keep the structural configuration for compatibility assertions, but never
  // put real people, account identifiers or persona text in visual fixtures.
  config.direct_ids=['synthetic-owner'];config.group_rules=[{id:scope.thread,name:'合成测试群',mode:'mention',prefix:'',persona:'',force_reply_when_mentioned:true}];
  Object.assign(config.companion,{name:'蓝色大肥鱼',persona:'友好、准确的工作伙伴。根据当前会话和核实来源回应。',style:'自然简洁，说明进度与结果。',appearance:'蓝色主题',relationship:'合成验收角色',proactive_threads:[],proactive_rules:[]});
  page=await context.newPage();const errors=[],writes=[],screens=process.env.WECHAT_ADMIN_UI_SCREENSHOTS;let documentText=prose,revision='synthetic-r1',preferences='',preferencesRevision=0;let packageRuntimeState='stopped',packageInstallRevision=1;let packageRevision=0,packageValues={endpoint:'https://provider.example/v1'};const packageInfo={id:'synthetic-service',name:'合成语音插件',version:'1.0.0',status:'ready',permissions:['network'],dependencies:[]},packageCatalog=[packageInfo,...[['speech','语音生成'],['music','音乐与换声'],['media-understanding','媒体理解'],['share-reader','分享内容读取'],['visual-generation','图像与视频生成'],['project-development','项目开发'],['mobile-control','手机控制'],['external-tools','外部工具连接']].map(([id,name])=>({id,name,version:'1.0.0',status:id==='visual-generation'?'template':'ready',permissions:[],dependencies:[]}))];const accountRows=[{id:'synthetic-account',name:'蓝色大肥鱼 · 合成账号',primary:true,native:{connection:'online'},bot:{state:'running'}},{id:'synthetic-second',name:'备用合成账号',label:'备用合成账号',native:{connection:'disconnected'},bot:{state:'disabled'}}];
  let storageGate, storageCount=440, openedFolder;
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url());if(url.origin!==base)return route.abort();
   if(url.pathname.startsWith('/api/')){
    if(url.pathname==='/api/storage'){
      if(storageGate) await storageGate;
      return route.fulfill({json:{total_bytes:512,can_open_folder:true,areas:[{id:'logs',name:'活跃日志',files:2,bytes:512,locations:['C:/synthetic/logs']}],limits:[{name:'活跃日志',count:storageCount,limit:1000}]}});
    }
    if(url.pathname==='/api/storage/open'){openedFolder=request.postDataJSON();return route.fulfill({json:{opened:true}})}
    if(url.pathname==='/api/storage/archive-logs'){const archived=storageCount-200;storageCount=200;return route.fulfill({json:{archived}})}
    if(url.pathname==='/api/accounts')return route.fulfill({json:{items:accountRows,primary:'synthetic-account',supported:true}});
    if(url.pathname==='/api/accounts/synthetic-second/manage'){const body=request.postDataJSON();if(body.action==='purge')accountRows.splice(1,1);else accountRows[1].removed=body.action==='remove';return route.fulfill({json:{ok:true}})}
    if(url.pathname==='/api/billing')return route.fulfill({json:{started_at:'2026-10-30T00:00:00Z',active_id:'synthetic-model',active_model:'synthetic',priced:true,prices:{currency:'CNY',input_per_million:1,cache_hit_per_million:.02,output_per_million:4},today:{input_tokens:12000,output_tokens:3000,cache_hit_tokens:1000,estimated_costs:{CNY:.04}},total:{input_tokens:12000,output_tokens:3000,cache_hit_tokens:1000,estimated_costs:{CNY:1,USD:2}},models:[]}});
    if(url.pathname==='/api/billing/balance')return route.fulfill({json:{state:'ok',data:{is_available:true,balance_infos:[{currency:'CNY',total_balance:'80.00',granted_balance:'30.00',topped_up_balance:'50.00'}]}}});
    if(url.pathname==='/api/status')return route.fulfill({json:{native:{authenticated:true,connection:'online'},bot:{state:'running'},account_name:'蓝色大肥鱼 · 合成账号',key_configured:true}});
    if(url.pathname==='/api/canvas'&&request.method()==='GET')return route.fulfill({json:{document:JSON.parse(await fs.readFile(path.join(__dirname,'../internal/admin/static/flow-default.json'),'utf8')),revision:'synthetic-canvas-v1',saved:true}});
    if(url.pathname==='/api/tools/state')return route.fulfill({json:{capabilities:{mediaKinds:['image','voice','video','file']},recipients:[{id:scope.thread,name:'合成测试群'}],recent_sends:[],limits:{}}});
    if(url.pathname==='/api/library')return route.fulfill({json:{items:[]}});
    if(url.pathname==='/api/plugin-packages')return route.fulfill({json:{installed:[{package:packageInfo,permissions:['network'],enabled:packageRuntimeState==='running',removed:false,revision:packageInstallRevision}],catalog:packageCatalog,runtime_connected:true,runtimes:{'synthetic-service':{state:packageRuntimeState}}}});
    if(url.pathname==='/api/plugin-packages/synthetic-service')return route.fulfill({json:{installed:{package:packageInfo,permissions:['network'],enabled:packageRuntimeState==='running',removed:false,revision:packageInstallRevision},schema:{type:'object',properties:{endpoint:{type:'string',title:'服务地址'},key:{type:'string',title:'服务密钥','x-secret':true}},required:['endpoint']},configuration:{revision:packageRevision,values:packageValues,configured_secrets:['key']},runtime_connected:true,runtime:{state:packageRuntimeState}}});
    if(url.pathname==='/api/plugin-packages/synthetic-service/runtime'){const body=request.postDataJSON();assert.equal(body.revision,packageInstallRevision);packageInstallRevision++;packageRuntimeState=body.action==='start'?'running':'stopped';writes.push({path:url.pathname,body});return route.fulfill({json:{runtime:{state:packageRuntimeState}}})}
    if(url.pathname==='/api/plugin-packages/synthetic-service/configuration'){const body=request.postDataJSON();assert.equal(body.revision,packageRevision);packageRevision++;packageValues=body.values;writes.push({path:url.pathname,body});return route.fulfill({json:{revision:packageRevision,values:packageValues,configured_secrets:['key']}})}
    if(url.pathname==='/api/plugin-packages/synthetic-service/jobs')return route.fulfill({json:{items:[{id:'synthetic-dispatch',state:'failed',diagnostic:{stage:'dispatch',code:'plugin_invocation_failed'}},{id:'synthetic-authority',state:'needs_review',diagnostic:{stage:'authority',code:'plugin_invocation_failed'}},{id:'synthetic-validation',state:'failed',diagnostic:{stage:'validation',code:'plugin_invocation_failed'}},{id:'synthetic-result-mode',state:'failed',diagnostic:{stage:'result_mode',code:'plugin_invocation_failed'}},{id:'synthetic-legacy',state:'needs_review'}],next:0,has_more:false,limit:30}});
    if(url.pathname==='/api/plugins')return route.fulfill({json:{installed:[{id:'synthetic-plugin',enabled:false,secret_keys:[],manifest:{id:'synthetic-plugin',name:'文字转语音',tools:[{name:'synthetic_speak',url:'https://provider.example/v1/tts',media_kind:'voice'}]}}],templates:[{id:'text_to_speech',name:'文字转语音',description:'将文字转成语音',tools:[{name:'synthetic_speak',url:'https://provider.example/v1/tts',media_kind:'voice'}]},{id:'external_mcp',name:'连接工具服务',description:'接入已配置的服务',mcp:{transport:'http',url:'https://provider.example/mcp'}}]}});
    if(url.pathname==='/api/reply-audits')return route.fulfill({json:{items:[{state:'final_check_failed',at:dateKey+'T08:00:00Z',review_failure:{code:'review_units_missing',stage:'review_protocol',review_state:'final_check_failed',detail:'复核响应缺少逐句核验结果',request:{Attempts:1,MaxAttempts:2,HTTPStatus:200,FormatAttempt:2,MaxFormatAttempts:2,JSONValid:true}}}],has_more:false,next_offset:1}});
    if(url.pathname==='/api/companion/state')return route.fulfill({json:{scopes:[scope]}});
    if(url.pathname==='/api/companion/memory/'+scope.id+'/preferences'){
     const body=request.postDataJSON();assert.equal(body.revision,preferencesRevision);preferences=body.text;preferencesRevision++;writes.push({path:url.pathname,body});return route.fulfill({json:{text:preferences,revision:preferencesRevision}});
    }
    if(url.pathname==='/api/companion/memory/'+scope.id)return route.fulfill({json:{...scope,preferences,preferences_revision:preferencesRevision,notes:[],messages:[{message_id:'synthetic-message',role:'user',sender:'synthetic-user',sender_name:'合成用户',time:'2026-10-29T08:00:00Z',text:prose}]}});
    if(url.pathname==='/api/logs'){
     const rows=[log('receive',prose),log('sent','这是完整发送内容。','send_accepted'),{...log('tool','', 'tool_call_failed'),category:'error',status:'failed',tool:'server_exec',reason:'合成执行器不可用',tool_code:'synthetic_engine_error',tool_stage:'请求解析'}, {...log('effect','', 'tool_call_succeeded'),category:'tool',status:'succeeded',tool:'server_write',effect:'已创建文件：会话根目录/output/result.txt'}, {...log('grounding','', 'character_decision'),category:'error',status:'failed',reason:'grounding_review_protocol_invalid',review_failure:{code:'grounding_review_protocol_invalid',stage:'model_decode',review_state:'initial_check_failed',detail:'复核返回不符合协议，已停止发送未验证的回复',request:{HTTPStatus:200,Attempts:1,MaxAttempts:2,FormatAttempt:2,MaxFormatAttempts:2,FinishReason:'stop',ContentBytes:321,JSONValid:false}}}];
     if(!url.searchParams.has('date'))return route.fulfill({json:{items:rows}});
     if(url.searchParams.get('date')!==dateKey)return route.fulfill({json:{items:[],complete:true}});
     const continuation=url.searchParams.has('after'),extra=Array.from({length:continuation?25:50},(_,i)=>({...log('date-'+(i+(continuation?50:0)),'当天的合成回合 '+i),message_id:'date-message-'+(i+(continuation?50:0)),time:dateKey+'T07:00:00Z'}));
     return route.fulfill({json:{items:continuation?extra:[...rows,...extra],next:continuation?'':'synthetic-next',complete:continuation,archive_batches:10}});
    }
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
    if(request.method()==='GET'){const target=new URL(url);target.searchParams.delete('account');return route.fulfill({response:await context.request.get(target.href)});}
    return route.fulfill({status:403,json:{error:'合成界面验收禁止真实写入'}});
   }
   const file=path.join(__dirname,'../internal/admin/static',url.pathname==='/'?'index.html':url.pathname.split('/').pop());
   try{return route.fulfill({body:await fs.readFile(file),contentType:({'.html':'text/html','.css':'text/css','.js':'text/javascript','.png':'image/png','.json':'application/json'})[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base);await page.locator('#overview.active-page').waitFor();assert.equal(await page.locator('.main-nav').count(),6);assert.deepEqual(await page.locator('.main-nav span').allTextContents(),['总览','会话','活动','插件','角色','设置']);
  await page.evaluate(()=>window.WechatAdmin.nav('settings'));
  const sidebarRefresh=page.getByRole('button',{name:'刷新当前页面',exact:true});
  assert(await sidebarRefresh.evaluate(b=>b.parentElement.classList.contains('sidebar-bottom') && Math.abs(b.getBoundingClientRect().x+b.offsetWidth/2-(b.parentElement.getBoundingClientRect().x+b.parentElement.clientWidth/2))<2));
  await page.locator('#storage-panel summary').first().click();
  await page.getByText('已统计 512 B',{exact:true}).waitFor();
  let finishScan;storageGate=new Promise(resolve=>{finishScan=resolve});
  await sidebarRefresh.click();await page.getByText('正在扫描…',{exact:true}).waitFor();
  assert.equal(await sidebarRefresh.getAttribute('aria-busy'),'true');assert(await sidebarRefresh.isDisabled());
  assert(await sidebarRefresh.locator('svg').evaluate(e=>getComputedStyle(e).animationName==='sidebar-spin'));
  finishScan();storageGate=null;await page.locator('#sidebar-refresh:not([aria-busy])').waitFor();
  await page.locator('#storage-areas summary').click();await page.getByRole('button',{name:'打开文件夹'}).click();
  assert.deepEqual(openedFolder,{area:'logs',index:0});
  await page.getByRole('button',{name:'归档较早的 240 条日志'}).click();await page.getByText('已归档 240 条，最近 200 条保留在活跃日志中。').waitFor();
  if(screens){await fs.mkdir(screens,{recursive:true});await page.screenshot({path:path.join(screens,'storage-sidebar.png')})}
  await page.evaluate(()=>window.WechatAdmin.nav('overview'));
  // Mobile control is now rendered from the installed package detail, rather
  // than a permanent console page. Guard both the source and live navigation
  // so a stale standalone page/script cannot quietly return.
  const indexSource=await fs.readFile(path.join(__dirname,'../internal/admin/static/index.html'),'utf8');
  assert(!/id=["']mobile["']/.test(indexSource),'legacy #mobile page remains in index.html');
  assert(!/assets\/mobile\.js/.test(indexSource),'legacy mobile.js remains loaded by index.html');
  assert.equal(await page.locator('#mobile,[data-section="mobile"],script[src$="/mobile.js"]').count(),0,'legacy mobile navigation or script remains in the live console');
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
  await page.locator('#memory-content .long-content').click();assert((await page.locator('dialog.console-dialog .readable-full-text').textContent()).includes('【正文末尾完整保留】'));await page.keyboard.press('Escape');await page.locator('#sidebar-refresh').click();await page.waitForTimeout(250);assert(await page.locator('#memory-content .long-content').isVisible());
  await page.evaluate(()=>window.WechatAdmin.nav('logs'));await page.waitForFunction(()=>document.querySelectorAll('.round-card').length===76);assert.equal(await page.locator('#log-date').inputValue(),dateKey);assert.equal(await page.locator('#log-more').count(),0);await page.locator('.round-summary').first().click();assert(await page.locator('dialog.console-dialog').isVisible());const roundText=await page.locator('dialog.console-dialog').textContent();assert(roundText.includes('调用工具 · 在隔离环境执行命令'));assert(roundText.includes('失败原因：合成执行器不可用'));assert(roundText.includes('会话根目录/output/result.txt'));assert(roundText.includes('首次复核未完成'));await page.locator('dialog .conversation-summary').getByText('复核状态：最终复核未完成',{exact:false}).waitFor();assert(roundText.includes('解析模型响应'));assert(roundText.includes('HTTP 200'));assert(roundText.includes('格式校验轮次 2/2 次'));assert(!roundText.includes('角色选择保持安静'));assert.equal(await page.locator('dialog .round-technical,dialog .raw-details').count(),0);
  assert.equal(await page.locator('dialog.console-dialog .technical-json:visible').count(),0,'raw code leaked into normal reading');
  await page.locator('dialog.console-dialog .long-content').first().click();assert((await page.locator('dialog.console-dialog').last().textContent()).includes('【正文末尾完整保留】'));await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  await page.locator('#log-date').fill('2020-01-01');await page.locator('#log-date').dispatchEvent('change');await page.getByText('这一天没有匹配的执行回合。',{exact:true}).waitFor();assert.equal(await page.locator('.round-card').count(),0);await page.locator('#log-today').click();await page.waitForFunction(()=>document.querySelectorAll('.round-card').length===76);
  // Native selectors preserve complete values without custom overlay/scanners.
  assert.equal(await page.locator('.select-trigger,.select-popup,.skip-link').count(),0);
  await page.evaluate(()=>{const s=document.querySelector('#log-status');s.add(new Option('长选项'.repeat(60),'long'));});
  await page.locator('#log-status').selectOption('long');
  assert.equal(await page.locator('#log-status option:checked').textContent(),'长选项'.repeat(60));
  await page.locator('#log-status').selectOption({index:0});
  await page.evaluate(()=>window.WechatAdmin.nav('memory'));await page.locator('#conversation-close').click();
  assert(await page.locator('#conversation-inspector').isHidden());
  assert(await page.locator('#memory-panel').evaluate(e=>e.getBoundingClientRect().width/e.parentElement.getBoundingClientRect().width>.98),'unselected list must fill its area');
  await page.locator('.conversation-open').click();await page.locator('#memory-content .memory-message-body').waitFor();
  assert(await page.locator('.conversation-workspace').evaluate(e=>e.children[0].getBoundingClientRect().width<e.children[1].getBoundingClientRect().width),'conversation detail must get more room than directory');
  await page.locator('#memory-settings').click();await page.locator('#conversation-preferences').fill('本群收到视频号、快手分享时，默认总结实际内容后发送。');await page.locator('#save-conversation-preferences').click();await page.getByText('已保存，仅对本会话生效',{exact:true}).waitFor();await page.keyboard.press('Escape');await page.locator('#memory-settings').click();assert.equal(await page.locator('#conversation-preferences').inputValue(),preferences);await page.keyboard.press('Escape');
  await page.evaluate(()=>window.WechatAdmin.nav('provider'));
  assert(await page.locator('#provider').evaluate(e=>e.getBoundingClientRect().width/e.parentElement.getBoundingClientRect().width>.98),'settings card must fill its area');
  assert.equal(await page.locator('#config-form details').count(),0);
  assert(await page.locator('#max-tokens').isVisible());
  assert(await page.locator('#save-config').evaluate(e=>e.parentElement.id==='top-save-actions'),'save button must stay at the top right');
  assert(await page.locator('.sidebar-bottom>.theme-control').isVisible());assert.equal(await page.locator('.top-actions .theme-control').count(),0);
  assert.equal(await page.locator('#billing-save-prices').evaluate(e=>e.form?.id),'billing-price-form');assert.equal(await page.locator('#admin-password-save').evaluate(e=>e.form?.id),'admin-password-form');
  await page.getByRole('button',{name:'声音与歌曲',exact:true}).click();assert(await page.locator('[data-speech-plugin="music"]').isVisible());assert.equal(await page.locator('#speech-save').count(),0,'legacy service save control remains');await page.getByRole('button',{name:'分享内容读取',exact:true}).click();assert(await page.locator('[data-channels-plugin="share-reader"]').isVisible());assert.equal(await page.locator('#channels-enabled,#channels-save').count(),0,'legacy share settings remain writable');await page.getByRole('button',{name:'聊天模型',exact:true}).click();
  await page.evaluate(()=>{const popup=window.WechatDialogs.open({title:'合成保存表单'}),form=document.createElement('form'),input=document.createElement('input'),save=document.createElement('button');input.required=true;input.value='合成值';save.type='submit';save.textContent='保存测试表单';save.id='synthetic-dialog-save';form.append(input,save);form.addEventListener('submit',event=>{event.preventDefault();window.syntheticFormSubmitted=true});popup.body.append(form)});
  await page.waitForFunction(()=>document.querySelector('#synthetic-dialog-save').parentElement.classList.contains('dialog-save-actions'));await page.locator('#synthetic-dialog-save').click();assert(await page.evaluate(()=>window.syntheticFormSubmitted===true));await page.keyboard.press('Escape');
  await page.reload();await page.waitForFunction(()=>window.WechatAdmin?.isAuthenticated());await page.evaluate(()=>window.WechatAdmin.nav('plugins'));await page.getByRole('heading',{name:'插件中心',exact:true}).waitFor();await page.getByText('已安装能力',{exact:true}).waitFor();for(const title of ['语音与音乐','媒体与分享','创作','开发与设备','连接','其他能力'])assert(await page.getByText(title,{exact:true}).evaluateAll(nodes=>nodes.some(node=>getComputedStyle(node).display!=='none'&&node.getBoundingClientRect().width>0)),title+' must be visible in the overview');assert(await page.getByText('开发模板，尚不能运行。',{exact:true}).evaluateAll(nodes=>nodes.some(node=>getComputedStyle(node).display!=='none'&&node.getBoundingClientRect().width>0)));assert.deepEqual(await page.locator('.package-overview-actions button').allTextContents(),['通过 UI 新建插件','管理连接插件','导入本机插件']);const overviewLayout=await page.locator('.package-workspace').evaluate(workspace=>{const browser=workspace.querySelector('.package-browser'),editor=workspace.querySelector('.package-editor'),card=workspace.querySelector('.capability-overview-card'),style=getComputedStyle(card);return {browserDisplay:getComputedStyle(browser).display,workspaceWidth:workspace.getBoundingClientRect().width,editorWidth:editor.getBoundingClientRect().width,display:style.display,direction:style.flexDirection,color:style.color,background:style.backgroundColor}});assert.equal(overviewLayout.browserDisplay,'none','overview must not duplicate the directory');assert(overviewLayout.editorWidth>=overviewLayout.workspaceWidth-2,'overview editor must fill the available region: '+JSON.stringify(overviewLayout));assert.equal(overviewLayout.display,'flex');assert.equal(overviewLayout.direction,'column');assert.notEqual(overviewLayout.color,overviewLayout.background,'overview card text must remain readable');if(screens){await fs.mkdir(screens,{recursive:true});await page.setViewportSize({width:1440,height:900});await page.evaluate(()=>document.documentElement.dataset.theme='light');await page.screenshot({path:path.join(screens,'plugins-overview-light-1440.png')});await page.setViewportSize({width:375,height:812});await page.evaluate(()=>document.documentElement.dataset.theme='dark');const darkCard=await page.locator('.capability-overview-card').first().evaluate(card=>{const style=getComputedStyle(card);return {color:style.color,background:style.backgroundColor}});assert.notEqual(darkCard.color,darkCard.background,'dark overview card text must remain readable');await page.screenshot({path:path.join(screens,'plugins-overview-dark-375.png')});await page.setViewportSize({width:1440,height:900});await page.evaluate(()=>document.documentElement.dataset.theme='light')}const workspaceState=await page.locator('.package-workspace').evaluateAll(nodes=>nodes.map(node=>({hidden:node.hidden,display:getComputedStyle(node).display,width:node.getBoundingClientRect().width,text:node.textContent})));assert.equal(workspaceState.filter(item=>!item.hidden&&item.display!=='none'&&item.width>0).length,1,'only the package capability workspace is visible: '+JSON.stringify(workspaceState));await page.locator('.capability-overview-card').filter({hasText:'合成语音插件'}).click();for(const message of ['调用插件未完成。','核对权限时未能继续执行。','检查结果时未能通过核对。','结果格式不符合此操作。','结果需要核对；未自动重试。'])await page.getByText(message,{exact:true}).waitFor();const taskText=await page.locator('#package-jobs').textContent();assert(!taskText.includes('plugin_invocation_failed'),'raw plugin diagnostic code must not be shown');await page.locator('[data-config-field="endpoint"]').fill('https://provider.example/v2');await page.locator('[data-config-field="key"]').fill('synthetic-package-key');await page.locator('#package-config-save').click();await page.getByText('插件配置已保存，未启动服务。',{exact:true}).waitFor();assert.equal(await page.locator('[data-config-field="key"]').inputValue(),'');assert.equal(packageValues.endpoint,'https://provider.example/v2');assert.equal(packageRevision,1);await page.locator('[data-plugin-runtime-action="start"]').click();await page.locator('[data-plugin-runtime-action="stop"]').waitFor();assert(await page.locator('[data-config-field="endpoint"]').isDisabled());assert(await page.locator('#package-config-save').isDisabled());await page.locator('[data-plugin-runtime-action="stop"]').click();await page.locator('[data-plugin-runtime-action="start"]').waitFor();assert(!(await page.locator('[data-config-field="endpoint"]').isDisabled()));assert.equal(packageRuntimeState,'stopped');
  assert(await page.locator('.package-workspace:visible').evaluate(e=>{const a=e.children[0].getBoundingClientRect(),b=e.children[1].getBoundingClientRect();return a.width<b.width&&Math.abs(a.top-b.top)<2&&getComputedStyle(e.children[0]).borderTopStyle==='solid'}),'plugin directory and editor must align with more room for editing');
  await page.getByRole('button',{name:'连接与 MCP',exact:true}).click();await page.locator('#mcp').waitFor({state:'visible'});assert.equal(await page.locator('.main-nav[data-section="plugins"]').getAttribute('aria-current'),'page');
  await page.evaluate(()=>window.WechatAdmin.nav('wechat'));assert.equal(await page.locator('#account-list details').count(),0);await page.getByRole('button',{name:'移除账号',exact:true}).click();const removal=page.getByRole('dialog',{name:'是否删除该账号的所有数据？'});await removal.getByRole('button',{name:'同时删除数据'}).waitFor();await page.waitForFunction(d=>{const r=d.getBoundingClientRect();return Math.abs(r.left+r.width/2-innerWidth/2)<2&&Math.abs(r.top+r.height/2-innerHeight/2)<2},await removal.elementHandle());await removal.getByRole('button',{name:'仅移除账号'}).click();await page.getByRole('button',{name:'恢复账号',exact:true}).waitFor();assert.equal(await page.locator('#account-list .account-row').count(),1);await page.getByRole('button',{name:'恢复账号',exact:true}).click();await page.getByRole('button',{name:'移除账号',exact:true}).waitFor();assert.equal(await page.locator('#account-list .account-row').count(),2);
  await page.getByRole('button',{name:'移除账号',exact:true}).click();await page.getByRole('button',{name:'仅移除账号',exact:true}).click();
  await page.getByRole('button',{name:'删除数据',exact:true}).click();await page.getByRole('button',{name:'同时删除数据',exact:true}).click();
  await page.locator('#removed-accounts').waitFor({state:'hidden'});assert.equal(accountRows.length,1);

  await page.evaluate(()=>window.WechatAdmin.nav('overview'));await page.locator('#sidebar-refresh').click();await page.locator('#sidebar-refresh:not([aria-busy])').waitFor();
  await page.locator('#billing-cost').getByText('14.32327 元',{exact:true}).waitFor();
  assert.equal(await page.locator('#billing-balance').textContent(),'80 元');
  assert(!/美元|人民币|USD/.test(await page.locator('.billing-panel').innerText()));
  await page.emulateMedia({reducedMotion:'reduce'});
  assert.equal(await page.locator('#overview').evaluate(e=>getComputedStyle(e).animationName),'none');
  await page.emulateMedia({reducedMotion:'no-preference'});
  // Rapid navigation must settle on the latest target, with no persistent motion.
  await page.evaluate(()=>{for(const id of ['logs','memory','wechat','overview'])window.WechatAdmin.nav(id)});
  await page.waitForTimeout(300);
  assert(await page.locator('#overview.active-page').isVisible());
  assert(await page.locator('.nav-position').evaluate(el=>Math.abs(el.getBoundingClientRect().top-document.querySelector('.main-nav.active').getBoundingClientRect().top)<1));
  await page.emulateMedia({reducedMotion:'reduce'});
  assert.equal(await page.locator('.nav-position').evaluate(el=>getComputedStyle(el).transitionDuration),'0s');
  await page.emulateMedia({reducedMotion:'no-preference'});
  const pages=['overview','agent','workbench','memory','logs','surf','companion','voices','wechat','provider','plugins','mcp','permissions','settings','rules','catalog','tools','account-actions'];
  assert.equal(pages.length,18,'console page inventory must exclude the removed standalone mobile page');
  for(const theme of ['light','dark'])for(const viewport of [{width:375,height:812},{width:768,height:1024},{width:1024,height:768},{width:1440,height:900},{width:1920,height:1080}]){
   await page.setViewportSize(viewport);await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   for(const id of pages){await page.evaluate(s=>window.WechatAdmin.nav(s),id);await page.waitForTimeout(30);const result=await page.evaluate(()=>{const p=document.querySelector('.content>.active-page');return {visiblePages:[...document.querySelectorAll('.content>.page')].filter(e=>e.getClientRects().length).map(e=>e.id),horizontal:document.documentElement.scrollWidth>innerWidth+2||p.scrollWidth>p.clientWidth+2,bodyHeight:document.body.scrollHeight>innerHeight+2,misplacedSaves:[...document.querySelectorAll('button')].filter(b=>/^保存|^确认保存/.test(b.textContent.trim())&&b.getClientRects().length&&!b.closest('#top-save-actions,.dialog-save-actions')).map(b=>b.id)}});assert.deepEqual(result.visiblePages,[id],id+': inactive page leaked');assert(!result.horizontal,`${theme}/${viewport.width}/${id}: horizontal overflow`);assert(!result.bodyHeight,`${theme}/${viewport.width}x${viewport.height}/${id}: unbounded document`);assert.deepEqual(result.misplacedSaves,[],id+': save action outside header')}
  }
  // The plugin center is the primary service view; connections remain a real subpage.
  await page.evaluate(()=>window.WechatAdmin.nav('plugins'));await page.locator('.package-workspace:visible').waitFor();
  for(const width of [375,768,1440]){await page.setViewportSize({width,height:900});await page.waitForTimeout(50);assert(!(await page.locator('.package-workspace:visible').evaluate(e=>e.scrollWidth>e.clientWidth+2)),'plugin center overflow at '+width)}
  await page.setViewportSize({width:512,height:384});for(const id of ['overview','agent','memory','logs','plugins','provider']){await page.evaluate(s=>window.WechatAdmin.nav(s),id);assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2)),id+' at 200% equivalent viewport');}
  await page.setViewportSize({width:375,height:812});await page.evaluate(()=>window.WechatAdmin.nav('overview'));await page.locator('#menu-toggle').click();assert(await page.locator('#drawer-scrim').isVisible());await page.locator('.main-nav[data-section="memory"]').click();assert(await page.locator('#memory').isVisible());assert(!(await page.locator('#drawer-scrim').isVisible()));
  if(screens){await fs.mkdir(screens,{recursive:true});await page.setViewportSize({width:1440,height:900});for(const theme of ['dark','light']){await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);for(const id of pages){await page.evaluate(s=>window.WechatAdmin.nav(s),id);await page.waitForTimeout(260);await page.screenshot({path:path.join(screens,`${id}-${theme}.png`)})}}}
  if(process.env.WECHAT_ADMIN_VISUAL_AUDIT)await require('./audit_console_visual.cjs')(page,pages,process.env.WECHAT_ADMIN_VISUAL_AUDIT);
  assert.deepEqual(errors,[]);console.log(JSON.stringify({sixNavigationGroups:true,pluginCenterPrimary:true,fullBusinessText:true,boundedPanes:true,documentRevision:true,editorDraftPreserved:true,conversationRefreshPreserved:true,roundModal:true,rawDataLast:true,settingsPreserved:true,themes:2,viewports:5,pages:18,pageErrors:errors,realWechatSends:0}));
 }finally{if(page)await page.unrouteAll({behavior:'ignoreErrors'});await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
