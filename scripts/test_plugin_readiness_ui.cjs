/* Offline regression for package readiness.  It runs the production package
   UI with only synthetic metadata responses: no provider, runtime, or WeChat
   operation is allowed. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
 const executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const root=path.resolve('internal/admin/static'),page=await browser.newPage(),errors=[];
  page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());assert.equal(url.origin,'http://synthetic.local');
   const name=url.pathname==='/'?'index.html':path.basename(url.pathname);
   let body=await fs.readFile(path.join(root,name));
   if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));
   await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'});
  });
  await page.goto('http://synthetic.local');
  await page.evaluate(()=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!=='plugins';node.classList.toggle('active-page',node.id==='plugins')});
   const ready={id:'ready-plugin',name:'按需工具',version:'1.0.0',status:'ready',permissions:[],http_routes:[]};
   const incomplete={id:'needs-config',name:'缺少配置工具',version:'1.0.0',status:'ready',permissions:[],http_routes:[]};
   const disabled={id:'disabled-plugin',name:'已停用工具',version:'1.0.0',status:'ready',permissions:[],http_routes:[]};
   window.calls=[];window.account='readiness-account';window.revision=4;
   window.packages={
    ready:{package:ready,enabled:true,removed:false,revision:1,permissions:[]},
    incomplete:{package:incomplete,enabled:false,removed:false,revision:2,permissions:[]},
    disabled:{package:disabled,enabled:false,removed:false,revision:3,permissions:[]},
   };
   window.saved={revision:window.revision,values:{endpoint:''},configured_secrets:[]};
   const readiness=()=>({
    'ready-plugin':{state:'ready',missing_required_fields:[],reasons:[]},
    'needs-config':window.saved.values.endpoint?{state:'ready',missing_required_fields:[],reasons:[]}:{state:'needs_configuration',missing_required_fields:[{id:'endpoint',label:'服务地址'}],reasons:[{code:'required',message:'请填写服务地址'}]},
    'disabled-plugin':{state:'ready',missing_required_fields:[],reasons:[]},
   });
   window.WechatAccountContext={get id(){return window.account}};
   window.WechatAdmin={isAuthenticated:()=>true,notice:(text,error)=>{window.notice={text,error}},nav:section=>{document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!==section;node.classList.toggle('active-page',node.id===section)});window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section}}))},api:async(url,opt={})=>{
    const method=opt.method||'GET';window.calls.push({url,method,json:opt.json});
    if(url==='/api/plugin-packages'&&method==='GET')return {builtins:[{id:'wechat-messages',name:'微信消息收发',state:'可发送文字',description:'收发当前会话消息。',permissions:['当前账号']}],catalog:Object.values(window.packages).map(item=>item.package),installed:Object.values(window.packages),runtimes:{'ready-plugin':{state:'stopped',auto_managed:true},'needs-config':{state:'stopped'},'disabled-plugin':{state:'stopped'}},readiness:readiness()};
    if(url==='/api/plugins'&&method==='GET')return {installed:[]};
    const id=url.match(/^\/api\/plugin-packages\/(ready-plugin|needs-config|disabled-plugin)$/)?.[1];
    if(id&&method==='GET'){
     const item=id==='ready-plugin'?window.packages.ready:id==='needs-config'?window.packages.incomplete:window.packages.disabled;
     return {installed:item,schema:{type:'object',properties:{endpoint:{type:'string',title:'服务地址'}},required:['endpoint']},configuration:id==='needs-config'?window.saved:{revision:1,values:{endpoint:'https://ready.example'},configured_secrets:[]},runtime_connected:true,runtime:id==='ready-plugin'?{state:'stopped',auto_managed:true}:{state:'stopped'},readiness:readiness()[id]};
    }
    if(url==='/api/plugin-packages/needs-config/configuration'&&method==='POST'){
     if(opt.json.values.endpoint!=='https://configured.example')throw new Error('configuration payload did not preserve the named required value');window.saved={revision:++window.revision,values:{endpoint:opt.json.values.endpoint},configured_secrets:[]};return window.saved;
    }
    if(/^\/api\/plugin-packages\/(ready-plugin|needs-config|disabled-plugin)\/jobs\?limit=30&after=0$/.test(url)&&method==='GET')return {items:[],next:0,has_more:false};
    throw new Error('unexpected synthetic API '+method+' '+url);
   }};
  });
  for(const script of ['refresh.js','dialogs.js','save-actions.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.WechatAdmin.nav('plugins'));
  await page.locator('.capability-overview-grid').waitFor();
  const text=async id=>page.locator(`.capability-overview-grid [data-package-id="${id}"]`).innerText();
  assert.match(await text('ready-plugin'),/按需待命/,'ready auto-managed package state is not shown');
  assert.match(await text('needs-config'),/待配置[\s\S]*服务地址/,'missing configuration does not name the required field');
  assert.match(await text('disabled-plugin'),/已停用/,'disabled package state is not shown');
  const overviewCalls=await page.evaluate(()=>window.calls.slice());
  assert.deepEqual(overviewCalls.map(call=>call.url).sort(),['/api/plugin-packages','/api/plugins'],'overview fetched package details or another endpoint');
  for(const width of [1440,375])for(const theme of ['light','dark']){
   await page.setViewportSize({width,height:900});await page.evaluate(value=>{document.documentElement.dataset.theme=value;document.querySelector('.package-editor').scrollTop=0},theme);await page.waitForTimeout(60);
   const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2||document.querySelector('.package-workspace').scrollWidth>document.querySelector('.package-workspace').clientWidth+2);assert(!overflow,`package readiness overview overflows at ${width}px`);
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,`plugin-readiness-overview-${theme}-${width}.png`)})}
  }

  await page.locator('.capability-overview-grid [data-package-id="ready-plugin"]').click();
  await page.locator('.package-editor').getByRole('heading',{name:'按需工具',exact:true}).waitFor();
  assert.match(await page.locator('.package-editor').innerText(),/按需待命/,'ready package detail lost on-demand state');
  await page.getByRole('button',{name:'返回工具概览',exact:true}).click();
  await page.locator('.capability-overview-grid').waitFor();

  await page.locator('.capability-overview-grid [data-package-id="needs-config"]').click();
  await page.locator('.package-editor').getByRole('heading',{name:'缺少配置工具',exact:true}).waitFor();
  const endpoint=page.locator('.package-editor [data-config-field="endpoint"]');
  const enable=page.locator('.package-editor [data-plugin-policy-action="enable_on_demand"]');
  assert(await enable.isDisabled(),'missing required configuration must block enable');
  assert.equal(await endpoint.count(),1,'the named missing field is not editable in configuration');
  assert.match(await page.locator('.package-editor').innerText(),/待配置[\s\S]*服务地址/,'detail does not retain named missing configuration state');
  await endpoint.fill('https://configured.example');await page.locator('#package-config-save').click();
  await page.getByText('工具配置已保存，未启动服务。',{exact:true}).waitFor();
  assert(await enable.isEnabled(),'saved configuration must be checked before offering to enable');
  await page.getByRole('button',{name:'返回工具概览',exact:true}).click();
  await page.locator('.capability-overview-grid').waitFor();
  assert.match(await text('needs-config'),/已停用/,'saved package did not return with its re-read readiness state');
  assert.doesNotMatch(await text('needs-config'),/待配置|服务地址/,'saved configuration still reports the old missing field');

  await page.locator('.capability-overview-grid [data-package-id="disabled-plugin"]').click();
  await page.locator('.package-editor').getByRole('heading',{name:'已停用工具',exact:true}).waitFor();
  assert.match(await page.locator('.package-editor').innerText(),/已停用/,'disabled detail does not retain its inactive state');
  await page.getByRole('button',{name:'返回工具概览',exact:true}).click();
  await page.locator('.capability-overview-grid').getByRole('button',{name:/微信消息收发/}).click();
  assert(await page.locator('#permissions').isVisible(),'built-in messaging card should open permissions');
  const calls=await page.evaluate(()=>window.calls);
  const prohibited=calls.filter(call=>/\/runtime(?:$|\/)|\/actions\/|\/install|\/prepare|\/provider|\/speech|wechat|\/api\/plugin-packages\/[^/]+\/jobs\?.*(?:after=30|after=\d{2,})/.test(call.url)||call.method!=='GET'&&call.url!=='/api/plugin-packages/needs-config/configuration');
  assert.deepEqual(prohibited,[],'readiness browsing mutated state, read all details, or contacted a provider/WeChat endpoint: '+JSON.stringify(prohibited));
  assert.deepEqual(errors,[]);console.log(JSON.stringify({overviewMetadataOnly:true,readyOnDemand:true,namedMissingConfiguration:true,disabledReadOnly:true,saveAndReturn:true,viewports:2,providerCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
