/* Offline migration workflow: synthetic API only, never starts a service or sends WeChat. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
 const executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const page=await browser.newPage(),errors=[],calls=[];page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  const root=path.resolve('internal/admin/static');
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());assert.equal(url.origin,'http://synthetic.local');
   const name=url.pathname==='/'?'index.html':path.basename(url.pathname);let body=await fs.readFile(path.join(root,name));
   if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));
   await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'});
  });
  await page.goto('http://synthetic.local');
  await page.evaluate(()=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(page=>{page.hidden=page.id!=='plugins';page.classList.toggle('active-page',page.id==='plugins')});
   window.WechatAccountContext={id:'synthetic-one'};window.calls=[];
   const pkg={id:'speech',name:'默认云端语音',version:'0.1.3',status:'ready',permissions:['model.invoke'],http_routes:[]};
   const project={id:'project-development',name:'项目开发',version:'0.2.0',status:'ready',permissions:['workspace.read','workspace.write','process.execute'],http_routes:[]};
	   const external={id:'external-tools',name:'外部工具连接',version:'0.3.0',status:'ready',permissions:['network'],http_routes:[]};
   const ready={plugin:'speech',status:'ready_for_administrator_review',required_missing:[],unmapped_legacy_fields:['asr_*','music_*','yue_*'],apply_source_hash:'source-hash-one',permissions:['model.invoke'],package:{version:'0.1.3',expected_package_digest:'package-digest-one'},values:{tts_model:'should-not-render'},secrets:{tts_key:{present:true,sha256:'private-secret-not-rendered'}}};
   const blocked={plugin:'share-reader',status:'blocked',required_missing:['yuanbao_cookie_or_assistant_cookie','prepared_package_digest','channels-config.json:object_bridge_url'],unmapped_legacy_fields:[],apply_source_hash:'blocked-source',permissions:['network'],package:{version:'0.1.0'}};
   const projectReady={plugin:'project-development',status:'ready_for_administrator_review',required_missing:[],unmapped_legacy_fields:['agent-config.json:host.roots','agent-config.json:host.isolation','agent-config.json:host.windows'],apply_source_hash:'project-source-hash',permissions:['workspace.read','workspace.write','process.execute'],package:{version:'0.2.0',expected_package_digest:'project-package-digest'},values:{},secrets:{}};
	   const externalBlocked={plugin:'external-tools',status:'blocked',required_missing:['no_matching_sync_external_tools'],unmapped_legacy_fields:['plugins.json:host_retained_declarations_and_credentials'],apply_source_hash:'external-blocked-source',permissions:['network'],package:{version:'0.3.0',expected_package_digest:'external-package-digest'},values:{},secrets:{}};
   window.WechatAdmin={isAuthenticated:()=>true,api:async(url,opt={})=>{
    calls.push({url,json:opt.json});
    if(url==='/api/plugin-packages')return {catalog:[pkg,project,external],installed:[]};
    if(url==='/api/plugin-migrations/preview')return {kind:'plugin_migration_preview',read_only:true,accounts:[{account:'synthetic-one',plugins:[ready,projectReady,blocked,externalBlocked]}]};
    if(url==='/api/plugin-migrations/apply')return {installed:{enabled:false},configuration:{values:{}},migration:{id:'synthetic-migration'},enabled:false};
    throw new Error('unexpected API '+url);
   }};
  });
  for(const script of ['dialogs.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.getByRole('button',{name:'迁移旧设置',exact:true}).click();
  const dialog=page.locator('dialog.console-dialog');await dialog.getByText('默认云端语音',{exact:true}).waitFor();
  await dialog.getByText('项目开发',{exact:true}).waitFor();
	  const externalStep=dialog.locator('section.package-migration-step').filter({hasText:'外部工具连接'});
	  await externalStep.getByText('no_matching_sync_external_tools',{exact:true}).waitFor();
	  assert.equal(await externalStep.getByRole('button',{name:'确认并应用迁移',exact:true}).count(),0,'MCP or async-only external migration unexpectedly became applicable');
  assert.equal(await dialog.locator('[data-migration-permission="workspace.read"]').count(),1,'project migration did not present the reviewed read grant');
  assert.equal(await dialog.locator('[data-migration-permission="workspace.write"]').count(),1,'project migration did not present the reviewed write grant');
  assert.equal(await dialog.locator('[data-migration-permission="process.execute"]').count(),1,'project migration did not present the reviewed process grant');
  assert.equal(await dialog.getByText('其他设置保留原处：agent-config.json:host.roots、agent-config.json:host.isolation、agent-config.json:host.windows。',{exact:true}).count(),1,'host-owned project scope was not clearly retained');
  assert.equal(await page.evaluate(()=>calls.filter(call=>call.url==='/api/plugin-migrations/apply').length),0,'preview applied a migration');
  assert.equal(await dialog.getByText('private-secret-not-rendered',{exact:true}).count(),0,'secret marker rendered');
  assert(await dialog.getByText('其他设置保留原处：asr_*、music_*、yue_*。',{exact:true}).isVisible());
  const speechStep=dialog.locator('section.package-migration-step').filter({hasText:'默认云端语音'});
  await speechStep.getByRole('button',{name:'确认并应用迁移',exact:true}).waitFor();
  const apply=speechStep.getByRole('button',{name:'确认并应用迁移',exact:true});assert(await apply.isDisabled(),'apply did not require acknowledgement');
  for(const theme of ['light','dark'])for(const width of [375,1440]){
   await page.setViewportSize({width,height:900});await page.evaluate(value=>document.documentElement.dataset.theme=value,theme);
   assert(!(await dialog.evaluate(node=>node.scrollWidth>node.clientWidth+2)),`dialog overflow ${theme}/${width}`);
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,`plugin-migration-${theme}-${width}.png`)});}
  }
  await page.setViewportSize({width:1440,height:900});
  const permission=dialog.locator('[data-migration-permission="model.invoke"]');await permission.uncheck();
  const acknowledge=speechStep.locator('[data-migration-confirm]');await acknowledge.check();assert(await apply.isEnabled());
  await permission.check();assert(await apply.isDisabled(),'permission change retained previous acknowledgement');
  await acknowledge.check();await apply.click();
  const request=await page.evaluate(()=>calls.find(call=>call.url==='/api/plugin-migrations/apply').json);
  assert.deepEqual(request,{account:'synthetic-one',plugin:'speech',apply_source_hash:'source-hash-one',prepared_package_digest:'package-digest-one',permissions:['model.invoke'],confirm:true});
  assert.equal(await speechStep.getByRole('button',{name:'确认并应用迁移',exact:true}).count(),0,'successful migration remained actionable');
  assert(await dialog.getByText('迁移已保存为未启用插件；请在插件详情中检查配置后手动启动。',{exact:true}).isVisible());
  assert(await dialog.getByText('缺少分享读取登录信息',{exact:true}).isVisible());
  assert.equal(await dialog.getByText('channels-config.json:object_bridge_url',{exact:true}).count(),1,'blocked legacy field was hidden');
  await dialog.locator('button').filter({hasText:'关闭'}).click().catch(()=>{});
  await page.getByRole('button',{name:'迁移旧设置',exact:true}).click();await speechStep.getByRole('button',{name:'确认并应用迁移',exact:true}).waitFor();
  await speechStep.locator('[data-migration-confirm]').check();await page.evaluate(()=>{window.WechatAccountContext.id='synthetic-two'});await speechStep.getByRole('button',{name:'确认并应用迁移',exact:true}).click();
  assert.equal(await page.evaluate(()=>calls.filter(call=>call.url==='/api/plugin-migrations/apply').length),1,'cross-account confirmation submitted');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({previewReadOnly:true,perPluginApproval:true,accountSourceDigestPermissionsBound:true,permissionChangeInvalidates:true,blockedReasonsShown:true,notAutomaticallyEnabled:true,themes:2,widths:[375,1440],paidCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
