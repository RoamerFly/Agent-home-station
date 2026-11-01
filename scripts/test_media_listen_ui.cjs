/* Offline UI contract for media.listen configuration. No server, model, plugin
   install/runtime action, or WeChat call is allowed in this synthetic test. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
 const executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const root=path.resolve('internal/admin/static'),pluginRoot=path.resolve('plugins/media-understanding');
  const [manifest,schema,ui]=await Promise.all(['plugin.json','config.schema.json','ui.schema.json'].map(async name=>JSON.parse(await fs.readFile(path.join(pluginRoot,name),'utf8'))));
  const page=await browser.newPage(),errors=[],calls=[];page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{const url=new URL(route.request().url());assert.equal(url.origin,'http://synthetic.local');const name=url.pathname==='/'?'index.html':path.basename(url.pathname);let body=await fs.readFile(path.join(root,name));if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'})});
  await page.goto('http://synthetic.local');
  await page.evaluate(({manifest,schema,ui})=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!=='plugins';node.classList.toggle('active-page',node.id==='plugins')});
   window.calls=[];window.account='synthetic-media';window.holdSave=false;window.saved={revision:4,values:{audio_review_endpoint:'https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions',audio_review_model:'qwen3.8-omni-flash'},configured_secrets:['audio_review_key']};
   const installed={package:manifest,enabled:false,removed:false,revision:3,permissions:['artifact.read','model.invoke']};window.available={catalog:[manifest],installed:[installed],runtimes:{'media-understanding':{state:'stopped'}}};
   window.WechatAccountContext={get id(){return window.account}};
   window.WechatAdmin={isAuthenticated:()=>true,notice:(text,error)=>{window.notice={text,error}},nav:section=>{document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!==section;node.classList.toggle('active-page',node.id===section)});window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section}}))},api:async(url,opt={})=>{
    window.calls.push({url,method:opt.method||'GET',json:opt.json});
    if(url==='/api/plugin-packages')return window.available;
    if(url==='/api/plugin-packages/media-understanding')return {installed,schema,ui_schema:ui,configuration:window.saved,runtime_connected:false,runtime:{state:'stopped'}};
    if(url==='/api/plugin-packages/media-understanding/jobs?limit=30&after=0')return {items:[],next:0,has_more:false};
    if(url==='/api/plugin-packages/media-understanding/configuration'&&opt.method==='POST'){
     window.lastSave=opt.json;
     if(window.holdSave)return new Promise(resolve=>window.resolveSave=()=>resolve({revision:5,values:opt.json.values,configured_secrets:['audio_review_key']}));
     return {revision:5,values:opt.json.values,configured_secrets:['audio_review_key']};
    }
    throw new Error('unexpected synthetic API '+url);
   }};
  },{manifest,schema,ui});
  for(const script of ['dialogs.js','save-actions.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.WechatAdmin.nav('plugins'));
  await page.locator('.package-choice').getByText('媒体理解',{exact:true}).click();
  await page.locator('.package-editor').getByRole('heading',{name:'媒体理解',exact:true}).waitFor();
  const key=page.locator('#package-field-audio_review_key');
  assert.equal(await key.inputValue(),'','host-only listening key was rendered into the DOM');
  assert.equal(await key.getAttribute('placeholder'),'已配置，留空保留','host-only listening key configuration state is unclear');
  assert(await page.getByText('音频内容观察接口地址',{exact:true}).isVisible());assert(await page.getByText('音频内容观察模型',{exact:true}).isVisible());
  assert.equal(await page.locator('#top-save-actions>#package-config-save').count(),1,'plugin save is not in the shared top save area');
  await page.locator('#package-field-audio_review_model').fill('draft-listen-model');
  await page.evaluate(()=>window.holdSave=true);await page.locator('#package-config-save').click();await page.waitForFunction(()=>typeof window.resolveSave==='function');
  await page.locator('#package-field-audio_review_model').fill('newer-listen-draft');await page.evaluate(()=>{window.holdSave=false;window.resolveSave()});
  await page.getByText('已保存提交的配置，后续编辑尚未保存。',{exact:true}).waitFor();assert.equal(await page.locator('#package-field-audio_review_model').inputValue(),'newer-listen-draft','late configuration response discarded the listen draft');
  const saved=await page.evaluate(()=>window.lastSave);assert.equal(saved.values.audio_review_endpoint,'https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions');assert.equal(saved.values.audio_review_model,'draft-listen-model');assert.equal(saved.secrets.audio_review_key,'');assert(!JSON.stringify(saved).includes('synthetic-private'),'host-only secret reached synthetic save payload');
  for(const [width,theme,file] of [[1440,'light','media-listen-light-1440.png'],[375,'dark','media-listen-dark-375.png']]){
   await page.setViewportSize({width,height:900});await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.querySelector('#package-field-audio_review_endpoint').scrollIntoView({block:'center'})},theme);await page.waitForTimeout(60);
   const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2||document.querySelector('.package-workspace').scrollWidth>document.querySelector('.package-workspace').clientWidth+2);assert(!overflow,`media listen detail overflows at ${width}px`);
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,file)})}
  }
  assert.equal(await page.evaluate(()=>window.calls.filter(call=>/\/runtime$|\/install$|\/prepare$|\/actions\//.test(call.url)).length),0,'synthetic UI audit changed runtime, installation, or device state');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({mediaListenSchema:true,hostOnlyKeyRedacted:true,draftPreserved:true,topSave:true,viewports:2,paidCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
