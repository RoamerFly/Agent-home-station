/* Opt-in local browser regression. Synthetic catalogs and refresh operations;
 * no real catalog writes, configuration changes or WeChat messages. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const assert=require('node:assert/strict'),fs=require('node:fs/promises'),path=require('node:path');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname));
(async()=>{
 const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password,'Set the local UI test password in the environment');
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:900}});
  assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const page=await context.newPage(),errors=[],writes=[];page.on('pageerror',e=>errors.push(e.message));
  let mode='empty',job={state:'idle',message:'尚未同步'},pending=[],queries=0;
  await page.clock.install();
  await page.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());if(url.origin!==base)return route.abort();
   if(url.pathname==='/api/tools/state')return route.fulfill({json:{recipients:[{id:'one@chatroom',name:'合成群一'},{id:'two@chatroom',name:'合成群二'}]}});
   if(url.pathname==='/api/catalog/sync'){
    if(req.method()==='POST'){const body=req.postDataJSON();writes.push(body);job={id:'fixture-refresh',kind:'members',thread:body.thread,state:'succeeded',message:'当前群成员资料已刷新。',records:2};mode='members';return route.fulfill({status:202,json:{...job,state:'running'}})}
    return route.fulfill({json:job});
   }
   if(url.pathname==='/api/catalog'){
    queries++;
    if(mode==='pending'){pending.push(route);return}
    if(mode==='failed')return route.fulfill({status:502,json:{error:'目录分页游标已失效，请从首批重新查询'}});
    const items=mode==='empty'?[]:[{id:'synthetic-person',roomDisplayName:url.searchParams.get('thread')==='two@chatroom'?'第二群成员':'第一群成员'}];
    return route.fulfill({json:{items,next_cursor:mode==='members'?'fixture-next':null}});
   }
   if(url.pathname.startsWith('/api/')){
    if(req.method()!=='GET')return route.fulfill({status:403,json:{error:'真实写入已被测试阻止'}});
    return route.continue();
   }
   const filename=url.pathname==='/'?'index.html':url.pathname.split('/').pop(),file=path.join(__dirname,'../internal/admin/static',filename);
   try{return route.fulfill({body:await fs.readFile(file),contentType:({'.html':'text/html','.js':'text/javascript','.css':'text/css','.png':'image/png'})[path.extname(file)]||'text/plain'})}catch{return route.continue()}
  });
  await page.goto(base+'/#catalog');await page.locator('#catalog-thread option[value="one@chatroom"]').waitFor({state:'attached'});
  await page.locator('#catalog-kind').selectOption('members');await page.locator('#catalog-thread').selectOption('one@chatroom');
  await page.locator('#catalog-query').click();await page.waitForFunction(()=>document.querySelector('#catalog-state').textContent.includes('本地尚未同步'));
  assert(await page.locator('#catalog-query').isEnabled());assert.equal(await page.locator('#catalog-list').getAttribute('aria-busy'),'false');
  await page.locator('#catalog-refresh-members').click();await page.clock.runFor(1300);
  await page.waitForFunction(()=>document.querySelector('#catalog-state').textContent.startsWith('已显示'));
  assert.equal(writes.length,1);assert.deepEqual(writes[0],{kind:'members',thread:'one@chatroom'});
  assert.equal(await page.locator('.catalog-item').count(),1);
  mode='failed';await page.locator('#catalog-more').click();await page.waitForFunction(()=>document.querySelector('#catalog-state').textContent.includes('游标'));
  assert.equal(await page.locator('.catalog-item').count(),1,'page failure discarded existing results');assert(await page.locator('#catalog-query').isEnabled());
  mode='pending';await page.locator('#catalog-query').click();await page.waitForFunction(()=>document.querySelector('#catalog-query').disabled);
  mode='members';await page.locator('#catalog-thread').selectOption('two@chatroom');assert(await page.locator('#catalog-query').isEnabled());
  await page.locator('#catalog-query').click();await page.locator('.catalog-item h3').filter({hasText:'第二群成员'}).waitFor();
  for(const route of pending.splice(0))await route.fulfill({json:{items:[{id:'old',roomDisplayName:'不应显示的旧成员'}]}}).catch(()=>{});
  await page.clock.runFor(100);assert(!(await page.locator('#catalog-list').textContent()).includes('旧成员'));
  mode='pending';await page.locator('#catalog-query').click();await page.clock.runFor(15100);
  await page.waitForFunction(()=>document.querySelector('#catalog-state').textContent.includes('查询超时'));
  assert(await page.locator('#catalog-query').isEnabled());assert.equal(await page.locator('#catalog-list').getAttribute('aria-busy'),'false');
  for(const route of pending.splice(0))await route.abort().catch(()=>{});
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({emptyEndsLoading:true,targetedMemberRefresh:true,autoQueryAfterRefresh:true,paginationFailureKeepsResults:true,selectionCancelsStaleQuery:true,timeoutEndsLoading:true,pageErrors:errors,syntheticRefreshes:writes.length,realWechatSends:0,queries}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
