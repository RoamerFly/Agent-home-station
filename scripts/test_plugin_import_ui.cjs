/* Isolated import flow: actual UI scripts, synthetic API, no running server. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,channel:process.env.PLAYWRIGHT_CHANNEL||'msedge'});
 try{
  const page=await browser.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
  const root=path.resolve('internal/admin/static');
  await page.route('**/*',async r=>{
   const u=new URL(r.request().url());assert.equal(u.origin,'http://synthetic.local');
   const name=u.pathname==='/'?'index.html':path.basename(u.pathname);let body=await fs.readFile(path.join(root,name));
   if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));
   await r.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.svg')?'image/svg+xml':'text/html'});
  });
  await page.goto('http://synthetic.local');
  await page.evaluate(()=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(p=>{p.hidden=p.id!=='plugins';p.classList.toggle('active-page',p.id==='plugins')});
   window.WechatAccountContext={id:'synthetic-one'};window.calls=[];
   const pkg={id:'synthetic',name:'离线合成工具',version:'1.0',status:'ready',permissions:['network'],http_routes:[{endpoint:'https://provider.example/api/private/source/read',credential:'session'}]};
   window.WechatAdmin={isAuthenticated:()=>true,api:async(url,opt={})=>{
    calls.push({url,json:opt.json,kind:opt.headers?.['Content-Type'],file:opt.body?.name,size:opt.body?.size});
    if(url==='/api/plugin-packages')return {catalog:[pkg],installed:[]};
    if(url==='/api/plugin-packages/import'||url==='/api/plugin-packages/prepare')return {package:pkg,token:'synthetic-confirmation',digest:'synthetic-digest'};
    if(url==='/api/plugin-packages/install')return {enabled:false};
    throw new Error('unexpected API '+url);
   }};
  });
  for(const script of ['refresh.js','dialogs.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  const open=()=>page.getByRole('button',{name:'导入本机工具',exact:true}).click();
  const upload=()=>page.locator('dialog.console-dialog input[type=file]').setInputFiles({name:'synthetic.zip',mimeType:'application/zip',buffer:Buffer.from('synthetic fixture; HTTP contract covered by Go ZIP test')});
  await open();await upload();await page.getByRole('button',{name:'检查工具',exact:true}).click();
  await page.getByRole('button',{name:'确认安装',exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>calls.filter(c=>c.url.endsWith('/install')).length),0,'checking installed package');
  const submitted=await page.evaluate(()=>calls.find(c=>c.url.endsWith('/import')));assert.equal(submitted.kind,'application/zip');assert.equal(submitted.file,'synthetic.zip');assert(await page.getByText('https://provider.example/api/private/source/read',{exact:true}).isVisible());
  for(const theme of ['light','dark'])for(const width of [375,1440]){
   await page.setViewportSize({width,height:900});await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   assert(!(await page.locator('dialog.console-dialog').evaluate(d=>d.scrollWidth>d.clientWidth+2)),'import dialog overflow');
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,`import-${theme}-${width}.png`)});}
  }
  await page.locator('dialog.console-dialog input[type=checkbox]').uncheck();
  await page.getByRole('button',{name:'确认安装',exact:true}).click();await page.locator('dialog.console-dialog').waitFor({state:'detached'});
  const installed=await page.evaluate(()=>calls.find(c=>c.url.endsWith('/install')).json);assert.equal(installed.confirm,true);assert.deepEqual(installed.permissions,[]);
  // Changing the source removes the old confirmation; switching account can
  // never submit the previously prepared token under the new account.
  await open();await upload();await page.getByRole('button',{name:'检查工具',exact:true}).click();await page.getByRole('button',{name:'确认安装',exact:true}).waitFor();
  await page.getByPlaceholder('未选择 ZIP 时，使用此目录').fill('changed-source');assert.equal(await page.getByRole('button',{name:'确认安装',exact:true}).count(),0);
  await page.getByRole('button',{name:'检查工具',exact:true}).click();await page.getByRole('button',{name:'确认安装',exact:true}).waitFor();
  await page.evaluate(()=>window.WechatAccountContext.id='synthetic-two');await page.getByRole('button',{name:'确认安装',exact:true}).click();
  assert.equal(await page.evaluate(()=>calls.filter(c=>c.url.endsWith('/install')).length),1,'cross-account confirmation submitted');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({zipSelection:true,explicitApproval:true,permissionsRespected:true,changedSourceInvalidates:true,accountChangeDenied:true,themes:2,widths:[375,1440],paidCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
