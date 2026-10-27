/* Local opt-in canvas QA; configuration writes are intercepted. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';assert(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname));
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password);
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const page=await context.newPage(),errors=[],writes=[];page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());if(url.origin!==base)return route.abort();
   if(url.pathname==='/api/logs')return route.fulfill({json:{items:[{id:'fixture-in',time:'2026-10-27T04:00:00Z',thread_id:'fixture@chatroom',message_id:'fixture-message',source:'合成回合群',source_kind:'group',category:'received',event:'message_received',content:'合成节点测试',status:'received'},{id:'fixture-tool',time:'2026-10-27T04:00:01Z',thread_id:'fixture@chatroom',message_id:'fixture-message',source:'合成回合群',source_kind:'group',category:'tool',event:'tool_call_succeeded',tool:'web_search',status:'succeeded'}]}});
   if(url.pathname==='/api/billing/balance')return route.fulfill({json:{balances:[],available:false}});
   if(url.pathname.startsWith('/api/')){
    if(req.method()==='GET')return route.continue();
    if(url.pathname==='/api/config'){const body=req.postDataJSON();writes.push(body);return route.fulfill({json:{...body,key_configured:true}})}
    return route.fulfill({status:403,json:{error:'真实写入已被测试阻止'}});
   }
   const filename=url.pathname==='/'?'index.html':url.pathname.split('/').pop(),file=path.join(__dirname,'../internal/admin/static',filename);
   try{return route.fulfill({body:await fs.readFile(file),contentType:({'.html':'text/html','.js':'text/javascript','.css':'text/css','.png':'image/png'})[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base+'/#workbench');await page.locator('.flow-node').first().waitFor();assert.equal(await page.locator('.flow-node').count(),12);assert.equal(await page.locator('.main-nav').count(),6);
  const node=page.locator('#flow-node-receive'),before=await node.evaluate(e=>[parseFloat(e.style.left),parseFloat(e.style.top)]),box=await node.boundingBox();
  await page.mouse.move(box.x+box.width/2,box.y+30);await page.mouse.down();await page.mouse.move(box.x+box.width/2+65,box.y+70,{steps:8});await page.mouse.up();
  const after=await node.evaluate(e=>[parseFloat(e.style.left),parseFloat(e.style.top)]);assert(after[0]>before[0]+20&&after[1]>before[1]+10,'node not draggable');
  const viewport=await page.locator('#flow-viewport').boundingBox(),transform=await page.locator('#flow-world').getAttribute('style');await page.mouse.move(viewport.x+6,viewport.y+6);await page.mouse.down();await page.mouse.move(viewport.x+46,viewport.y+36);await page.mouse.up();assert.notEqual(await page.locator('#flow-world').getAttribute('style'),transform);
  const zoom=await page.locator('#flow-zoom').textContent();await page.locator('#flow-zoom-in').click();assert.notEqual(await page.locator('#flow-zoom').textContent(),zoom);
  await page.locator('#flow-jump').selectOption('persona');await page.locator('#companion-style').waitFor({state:'visible'});const original=await page.locator('#companion-style').inputValue();await page.locator('#companion-style').fill(original+'\n合成画布草稿');
  await page.locator('#flow-jump').selectOption('model');await page.locator('#model').waitFor({state:'visible'});assert.equal(new URL(page.url()).hash,'#workbench');
  await page.locator('#flow-jump').selectOption('persona');assert((await page.locator('#companion-style').inputValue()).includes('合成画布草稿'));await page.locator('#save-companion').click();await page.waitForFunction(()=>document.querySelector('#notice').textContent==='配置已保存并应用。');assert.equal(writes.length,1);
  await page.locator('#flow-move').selectOption('right');const layout=await page.evaluate(()=>JSON.parse(localStorage.getItem('wechat_agent_canvas_v1')));assert.deepEqual(Object.keys(layout).sort(),['pan','positions','scale','version']);assert(layout.positions.persona.x>40);
  await page.locator('#flow-jump').selectOption('model');await page.evaluate(()=>window.WechatAdmin.nav('provider'));assert(await page.locator('#provider').isVisible());assert.equal(await page.locator('#provider').evaluate(e=>e.parentElement.id),'main-content');
  await page.evaluate(()=>window.WechatAdmin.nav('workbench'));await page.locator('#flow-trace option').filter({hasText:'合成回合群'}).waitFor({state:'attached'});const trace=await page.locator('#flow-trace option').last().getAttribute('value');await page.locator('#flow-trace').selectOption(trace);assert((await page.locator('#flow-node-tools .flow-node-state').textContent()).includes('1 次工具'));
  await page.locator('#flow-trace-open').click();assert((await page.locator('.console-dialog').last().textContent()).includes('合成节点测试'));await page.keyboard.press('Escape');
  await page.locator('#flow-jump').selectOption('model');assert(await page.locator('#flow-edges path[data-emphasis="active"]').count()>0);assert(await page.locator('#flow-edges path[data-emphasis="muted"]').count()>0);await page.locator('#flow-inspector-close').click();
  for(const theme of ['light','dark']){await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);for(const width of [375,768,1440]){await page.setViewportSize({width,height:1000});for(const id of ['rules','observe','model','agent','tools','memory','send']){await page.locator('#flow-jump').selectOption(id);await page.waitForTimeout(45);assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1)),theme+'/'+width+'/'+id+' outer overflow');assert(!(await page.locator('#flow-inspector-body').evaluate(e=>e.scrollWidth>e.clientWidth+2)),theme+'/'+width+'/'+id+' inspector overflow')}await page.locator('#flow-inspector-close').click()}}
  await fs.mkdir('research/evidence/node-workbench',{recursive:true});
  for(const [width,height,theme,name] of [[375,812,'light','canvas-mobile-light'],[844,390,'light','canvas-landscape-light'],[1440,1000,'dark','canvas-dark'],[1440,1000,'light','canvas-light']]){await page.setViewportSize({width,height});await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);await page.waitForTimeout(220);await page.evaluate(()=>scrollTo(0,0));if(width>520)await page.locator('#flow-reset').click();const bounds=await page.locator('#flow-viewport').boundingBox(),nodeBounds=await page.locator('.flow-node').evaluateAll(es=>es.map(e=>{const r=e.getBoundingClientRect();return {left:r.left,right:r.right}}));assert(nodeBounds.every(r=>r.left>=bounds.x-1&&r.right<=bounds.x+bounds.width+1),'fit must show all node columns');if(width<=850){assert((await page.locator('.sidebar').boundingBox()).x+180<=1,'closed drawer must be off-screen');await page.locator('#menu-toggle').click();await page.waitForTimeout(220);assert(await page.locator('.sidebar').evaluate(e=>!e.inert),'drawer must open');await page.keyboard.press('Escape');await page.waitForTimeout(220)}await page.screenshot({path:'research/evidence/node-workbench/'+name+'.png'})}
  await page.evaluate(()=>document.documentElement.dataset.theme='dark');await page.locator('#flow-jump').selectOption('model');await page.screenshot({path:'research/evidence/node-workbench/inspector-dark.png'});
  assert.equal(await page.evaluate(()=>{const ids=[...document.querySelectorAll('[id]')].map(e=>e.id);return ids.length-new Set(ids).size}),0,'duplicate IDs after inspector moves');assert.deepEqual(errors,[]);
  console.log(JSON.stringify({dragNodes:true,panAndZoom:true,localLayoutOnly:true,embeddedConfigWithoutNavigation:true,draftRetained:true,mockedSave:true,formsRestored:true,realEvidenceProjection:true,widths:[375,768,1440],themes:2,pageErrors:errors,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
