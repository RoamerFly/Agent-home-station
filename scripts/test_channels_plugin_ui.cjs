/* Offline channels migration UI: synthetic APIs only, never reads a card or sends WeChat. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const page=await browser.newPage(),errors=[],root=path.resolve('internal/admin/static');page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{const url=new URL(route.request().url());assert.equal(url.origin,'http://synthetic.local');const name=url.pathname==='/'?'index.html':path.basename(url.pathname);let body=await fs.readFile(path.join(root,name));if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'})});
  await page.goto('http://synthetic.local');
  await page.evaluate(()=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!=='permissions';node.classList.toggle('active-page',node.id==='permissions')});document.querySelector('#channels-settings-panel').open=true;
   window.account='synthetic-one';window.calls=[];window.available={installed:[],catalog:[{id:'share-reader',name:'分享内容读取',status:'ready',permissions:[]}],runtimes:{}};
   window.WechatAccountContext={get id(){return window.account}};
   window.WechatAdmin={isAuthenticated:()=>true,notice:(text,error)=>{window.notice={text,error}},nav:section=>{window.navs=(window.navs||[]).concat(section);document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!==section;node.classList.toggle('active-page',node.id===section)});window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section}}))},api:async(url,opt={})=>{window.calls.push({url,json:opt.json});if(url==='/api/plugin-packages')return window.available;if(url==='/api/plugin-migrations/preview')return {accounts:[{account:'synthetic-one',plugins:[{plugin:'share-reader',status:'ready_for_administrator_review',required_missing:[],unmapped_legacy_fields:[],apply_source_hash:'synthetic-source',permissions:[],package:{version:'0.1.0',expected_package_digest:'synthetic-digest'}}]}]};throw new Error('unexpected API '+url)}};
  });
  for(const script of ['dialogs.js','plugin-packages.js','channels.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.WechatAdmin.nav('permissions'));await page.getByText('尚未安装：可查看旧设置迁移预览；不会继续使用旧 Cookie 或桥接配置。',{exact:true}).waitFor();
  assert.equal(await page.locator('#channels-enabled,#channels-bridge,#channels-cookie,#channels-assistant-cookie,#channels-save,#channels-clear,#channels-state').count(),0,'legacy credentials or write controls remain');
  assert(await page.locator('#channels-card,#channels-bind,#channels-test').count()===3,'scoped card binding and manual read controls were removed');
  assert.equal(await page.evaluate(()=>window.calls.filter(call=>call.url==='/api/channels/settings').length),0,'permissions page read old settings');
  await page.locator('[data-channels-plugin="share-reader"]').click();await page.locator('.package-workspace').waitFor({state:'visible'});assert.equal(await page.evaluate(()=>window.navs.filter(item=>item==='plugins').length),1,'share entry did not enter the primary plugin page');const migration=page.locator('dialog.console-dialog');await migration.waitFor({state:'visible'});await migration.getByText('分享内容读取',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>window.calls.filter(call=>call.url==='/api/plugin-migrations/preview').length),1,'missing share reader did not open read-only migration preview');assert.equal(await page.evaluate(()=>window.calls.filter(call=>call.url==='/api/plugin-migrations/apply').length),0,'migration preview applied a plugin');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({legacyChannelsWritesRemoved:true,scopedBindingsRetained:true,missingUsesMigrationPreview:true,paidCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
