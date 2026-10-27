/* Opt-in real-browser QA. Uses only read-only management requests; local UI
 * assets and synthetic execution records are overlaid. Real mutations and
 * external browser requests are blocked. Requires Playwright and a local login.
 * No credentials or production conversation exports are written to evidence.
 */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname),'UI check requires a local management endpoint');
const at=n=>new Date(Date.UTC(2026,9,27,1,0,n)).toISOString();
let entries=[];
for(let i=0;i<90;i++){
 const common={thread_id:i%2?'group-a@chatroom':'group-b@chatroom',message_id:'fixture-'+i,source:i%2?'鲸鱼测试群':'日常分享群',source_kind:'group',sender:'fixture-person'};
 for(const [j,event,category,content,status]of [[0,'message_received','received',i===89?'帮我生成一首下班听的中文歌':'这是一条合成的测试消息 '+i,'received'],[1,'tool_call_succeeded','tool','workspace_read','succeeded'],[2,'message_sent','sent','[voice] 大肥鱼原创歌曲-下班路上.wav','accepted']])entries.push({...common,id:i+'-'+j,time:at(i*4+j),event,category,content,status,tool:category==='tool'?'workspace_read':undefined,send_id:category==='sent'?'send-'+i:undefined});
}
entries.push({id:'sys1',time:at(0),event:'bot_starting',category:'system'},{id:'sys2',time:at(2),event:'eligible_message_received',category:'system'});
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password,'Set WECHAT_ADMIN_TEST_PASSWORD without printing or committing it');
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const r=await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}});assert(r.ok(),'login failed');
  const page=await context.newPage(),errors=[],blocked=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());
   if(url.origin!==base)return route.abort();
   if(url.pathname==='/api/logs')return route.fulfill({json:{items:entries}});
   if(url.pathname==='/api/billing/balance')return route.fulfill({json:{available:true,provider:'fixture',balances:[{currency:'CNY',total_balance:'88.00',granted_balance:'0.00',topped_up_balance:'88.00'}]}});
   if(url.pathname.startsWith('/api/')){
    if(req.method()!=='GET'){blocked.push(url.pathname);return route.fulfill({status:403,json:{error:'测试阻止真实修改'}})}
    return route.continue();
   }
   const filename=url.pathname==='/'?'index.html':url.pathname.split('/').pop();
   const file=path.join(__dirname,'../internal/admin/static',filename);
   const types={'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.png':'image/png'};
   try{return route.fulfill({body:await fs.readFile(file),contentType:types[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base+'/#logs');await page.locator('.round-card').first().waitFor();await page.locator('.round-extra-filters > summary').click();await page.selectOption('#log-view','rounds');await page.locator('.round-extra-filters > summary').click();
  assert.equal(await page.locator('.main-nav').count(),5);
  assert.equal(await page.locator('.round-card').count(),60);assert.equal(await page.locator('.round-step').count(),0,'collapsed rounds must not render steps');
  await page.locator('.round-card').first().locator('.round-summary').first().click();assert.equal(await page.locator('.round-step').count(),3);
  await fs.mkdir('research/evidence/round-console',{recursive:true});
  await page.screenshot({path:'research/evidence/round-console/logs-light.png'});await page.locator('dialog[open]').last().locator('.dialog-close').click();
  const pane=page.locator('#log-list');await pane.evaluate(el=>el.scrollTop=500);const before=await pane.evaluate(el=>el.scrollTop);
  entries.push({...entries[0],id:'new-1',message_id:'new-message',time:at(500),content:'新的测试消息'});
  await page.evaluate(()=>window.dispatchEvent(new Event('wechat-admin:logs-refresh')));await page.locator('#log-new').waitFor({state:'visible'});
  assert.equal(await pane.evaluate(el=>el.scrollTop),before,'poll moved scroll');
  await page.locator('#log-new').click();assert(Math.abs(await pane.evaluate(el=>el.scrollTop)-before)<130,'load did not preserve reading anchor');
  assert.equal(await page.locator('.round-step').count(),0,'closed dialogs leaked steps into the list');
  await page.locator('#log-search').fill('workspace_read');await page.waitForTimeout(200);assert.equal(await page.locator('#log-count').textContent(),'90 个执行回合');
  await page.locator('#log-search').fill('');await page.waitForTimeout(200);
  await page.locator('.round-extra-filters > summary').click();await page.locator('#log-view').selectOption('system');assert.equal(await page.locator('.round-card').count(),1);
  await page.locator('#log-view').selectOption('rounds');await page.locator('.round-extra-filters > summary').click();
  const sections=['overview','logs','catalog','memory','rules','tools','companion','agent','surf','plugins','provider','mcp','permissions','mobile','wechat','account-actions','settings'];
  for(const theme of ['light','dark']){
   await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
   for(const width of [375,768,1440]){
    await page.setViewportSize({width,height:1000});
    for(const section of sections){
     await page.evaluate(s=>window.WechatAdmin.nav(s),section);await page.waitForTimeout(70);
     assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1)),theme+'/'+width+'/'+section+' overflow');
    }
   }
   await page.setViewportSize({width:1440,height:1000});await page.evaluate(()=>window.WechatAdmin.nav('logs'));
   await pane.evaluate(el=>el.scrollTop=0);await page.screenshot({path:'research/evidence/round-console/logs-'+theme+'.png'});
   await page.evaluate(()=>window.WechatAdmin.nav('overview'));await page.screenshot({path:'research/evidence/round-console/overview-'+theme+'.png'});
  }
  assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);
  console.log(JSON.stringify({pages:sections.length,themes:2,widths:[375,768,1440],roundGrouping:true,lazyDetails:true,scrollStable:true,largeDialogDetails:true,searchAndSystemView:true,pageErrors:errors,realMutations:0,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
