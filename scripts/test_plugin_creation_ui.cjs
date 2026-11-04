/* Offline plugin creation wizard: synthetic API only, never starts a service or sends WeChat. */
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
   window.items=[];
   window.WechatAdmin={isAuthenticated:()=>true,api:async(url,opt={})=>{
    calls.push({url,json:opt.json});
    if(url==='/api/plugin-packages')return {catalog:[],installed:[]};
    if(url==='/api/plugins')return {installed:items,templates:[]};
    if(url==='/api/plugins/install'){items.push({id:opt.json.manifest.id,manifest:opt.json.manifest,enabled:false,secret_keys:[]});return {}}
    throw new Error('unexpected API '+url)
   }};
  });
  for(const script of ['dialogs.js','plugins.js','plugin-packages.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section:'plugins'}})));
  for(const kind of ['http','mcp']){
   await page.getByRole('button',{name:'通过 UI 新建插件',exact:true}).filter({visible:true}).click();
   const dialog=page.locator('dialog[open]');
   await dialog.getByLabel('插件类型').selectOption(kind);
   await dialog.getByLabel('显示名称').fill('合成工具');
   await dialog.getByLabel('唯一标识').fill('demo-'+kind);
   await dialog.getByText('下一步',{exact:true}).click();
   await dialog.getByLabel('服务地址').fill('https://synthetic.example/tool');
   await dialog.getByLabel('用途说明').fill('合成测试');
   await dialog.getByText('下一步',{exact:true}).click();
   await dialog.getByText('上一步',{exact:true}).click();
   assert.equal(await dialog.getByLabel('服务地址').inputValue(),'https://synthetic.example/tool');
   await dialog.getByText('下一步',{exact:true}).click();
   await dialog.getByText('确认创建',{exact:true}).click();
   await page.waitForFunction(()=>!document.querySelector('dialog[open]'));
   assert.equal(await page.locator('#plugin-select').inputValue(),'demo-'+kind);
   await page.getByRole('button',{name:'返回能力目录'}).click();
  }
  const requests=await page.evaluate(()=>calls.filter(c=>c.url==='/api/plugins/install').map(c=>c.json));
  assert.equal(requests.length,2);assert(requests.every(r=>r.enabled===false));
  assert.equal(requests[0].manifest.tools[0].body_template.text,'{{arg.text}}');
  assert.equal(requests[1].manifest.mcp.transport,'http');
  assert.deepEqual(errors,[]);
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
