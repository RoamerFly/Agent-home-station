/* Offline UI contract for the named local speech profile.  It uses only a
   fake browser API: no plugin lifecycle call, provider/model request, or
   WeChat operation is permitted here. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
 const executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const root=path.resolve('internal/admin/static'),pluginRoot=path.resolve('plugins/speech');
  const [manifest,schema,ui]=await Promise.all(['plugin.json','config.schema.json','ui.schema.json'].map(async name=>JSON.parse(await fs.readFile(path.join(pluginRoot,name),'utf8'))));
  const page=await browser.newPage(),errors=[],calls=[];page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{const url=new URL(route.request().url());assert.equal(url.origin,'http://synthetic.local');const name=url.pathname==='/'?'index.html':path.basename(url.pathname);let body=await fs.readFile(path.join(root,name));if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'})});
  await page.goto('http://synthetic.local');
  await page.evaluate(({manifest,schema,ui})=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!=='plugins';node.classList.toggle('active-page',node.id==='plugins')});
   window.calls=[];window.account='synthetic-named-speech';window.holdSave=false;window.revision=6;
   window.saved={revision:window.revision,values:{local_profile_id:'gpt-sovits'},configured_secrets:[]};
   window.installed={package:manifest,enabled:false,removed:false,revision:3,permissions:['artifact.read','model.invoke']};
   window.available={catalog:[manifest],installed:[window.installed],runtimes:{speech:{state:'stopped'}}};
   window.WechatAccountContext={get id(){return window.account}};
   window.WechatAdmin={isAuthenticated:()=>true,notice:(text,error)=>{window.notice={text,error}},nav:section=>{document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!==section;node.classList.toggle('active-page',node.id===section)});window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section}}))},api:async(url,opt={})=>{
    window.calls.push({url,method:opt.method||'GET',json:opt.json});
    if(url==='/api/plugin-packages')return window.available;
    if(url==='/api/plugin-packages/speech')return {installed:window.installed,schema,ui_schema:ui,configuration:window.saved,runtime_connected:false,runtime:{state:'stopped'}};
    if(url==='/api/plugin-packages/speech/jobs?limit=30&after=0')return {items:[],next:0,has_more:false};
    if(url==='/api/plugin-packages/speech/configuration'&&opt.method==='POST'){
     window.lastSave=opt.json;const result={revision:++window.revision,values:opt.json.values,configured_secrets:window.saved.configured_secrets};
     if(window.holdSave)return new Promise(resolve=>window.resolveSave=()=>{window.saved=result;resolve(result)});
     window.saved=result;return result;
    }
    throw new Error('unexpected synthetic API '+url);
   }};
  },{manifest,schema,ui});
  for(const script of ['dialogs.js','save-actions.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.WechatAdmin.nav('plugins'));
  await page.locator('.package-choice').getByText('语音生成',{exact:true}).click();
  await page.locator('.package-editor').getByRole('heading',{name:'语音生成',exact:true}).waitFor();
  const profile=page.locator('#package-field-local_profile_id');
  assert.equal(await profile.inputValue(),JSON.stringify('gpt-sovits'),'local profile was not rendered from the real speech schema');
  assert.equal(await page.locator('#package-field-tts_key').inputValue(),'','a cloud key was rendered from local-only configuration');
  assert.equal(await page.locator('#top-save-actions>#package-config-save').count(),1,'speech save is not in the shared top save area');
  await page.locator('#package-config-save').click();
  await page.getByText('工具配置已保存，未启动服务。',{exact:true}).waitFor();
  let saved=await page.evaluate(()=>window.lastSave);
  assert.equal(saved.values.local_profile_id,'gpt-sovits','local-only selection was not saved');
  assert.equal(saved.secrets.tts_key,'','local-only save fabricated a cloud credential');
  assert(!JSON.stringify(saved).includes('synthetic-private'),'a host-only cloud credential leaked into the save payload');

  // A later cloud configuration may coexist with the named profile.  The
  // configured secret remains redacted even when these ordinary cloud fields
  // are present.
  await page.evaluate(()=>{window.saved={revision:window.revision,values:{local_profile_id:'gpt-sovits',tts_endpoint:'https://speech.example/v1',tts_model:'cloud-tts',default_voice:'cloud-voice',default_instruction:'清晰自然'},configured_secrets:['tts_key']}});
  await page.evaluate(()=>window.WechatPluginPackages.open('speech'));
  await page.locator('#package-field-tts_endpoint').waitFor();
  assert.equal(await profile.inputValue(),JSON.stringify('gpt-sovits'),'loading cloud settings lost the named profile');
  const key=page.locator('#package-field-tts_key');
  assert.equal(await key.inputValue(),'','configured cloud key was re-shown');
  assert.equal(await key.getAttribute('placeholder'),'已配置，留空保留','configured cloud key state is unclear');
  assert.equal(await page.locator('#package-field-tts_model').inputValue(),'cloud-tts');
  await page.locator('#package-field-default_instruction').fill('提交中的旧草稿');
  await page.evaluate(()=>window.holdSave=true);await page.locator('#package-config-save').click();await page.waitForFunction(()=>typeof window.resolveSave==='function');
  await page.locator('#package-field-default_instruction').fill('新的命名语音草稿');await page.evaluate(()=>{window.holdSave=false;window.resolveSave()});
  await page.getByText('已保存提交的配置，后续编辑尚未保存。',{exact:true}).waitFor();
  assert.equal(await page.locator('#package-field-default_instruction').inputValue(),'新的命名语音草稿','late save response discarded the newer local/cloud draft');
  saved=await page.evaluate(()=>window.lastSave);
  assert.deepEqual({profile:saved.values.local_profile_id,endpoint:saved.values.tts_endpoint,model:saved.values.tts_model,voice:saved.values.default_voice},{profile:'gpt-sovits',endpoint:'https://speech.example/v1',model:'cloud-tts',voice:'cloud-voice'},'cloud and named values did not coexist in one save');
  assert.equal(saved.secrets.tts_key,'','configured key should stay host-only');

  for(const [width,theme,file] of [[1440,'light','named-speech-light-1440.png'],[375,'dark','named-speech-dark-375.png']]){
   await page.setViewportSize({width,height:900});await page.evaluate(t=>{document.documentElement.dataset.theme=t;document.querySelector('#package-field-local_profile_id').scrollIntoView({block:'center'})},theme);await page.waitForTimeout(60);
   const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2||document.querySelector('.package-workspace').scrollWidth>document.querySelector('.package-workspace').clientWidth+2);assert(!overflow,`named speech detail overflows at ${width}px`);
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,file)})}
  }

  // A not-installed catalog entry is explanatory only: no configuration form,
  // lifecycle action or accidental install is presented.
  await page.evaluate(()=>{window.dispatchEvent(new Event('wechat-admin:logout'));window.available={catalog:[window.installed.package],installed:[],runtimes:{}};window.WechatAdmin.isAuthenticated=()=>true;window.WechatPluginPackages.open('speech')});
  await page.getByText('此工具尚未安装。导入并确认权限后可配置。',{exact:true}).waitFor();
  assert.equal(await page.locator('#package-config-form,[data-plugin-runtime-action]').count(),0,'uninstalled speech state exposed editable or runnable controls');
  assert.equal(await page.evaluate(()=>window.calls.filter(call=>/\/(runtime|install|prepare)$|\/actions\//.test(call.url)).length),0,'synthetic UI audit changed runtime or installation state');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({namedSpeechSchema:true,localOnlySave:true,cloudAndNamedCoexist:true,hostOnlyKeyRedacted:true,draftPreserved:true,topSave:true,uninstalledEmptyState:true,viewports:2,paidCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
