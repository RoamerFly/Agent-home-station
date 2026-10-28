/* Opt-in browser check. Account/log fixtures are synthetic; all real writes
 * except establishing the local admin session are blocked. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const assert=require('node:assert/strict'),fs=require('node:fs/promises'),path=require('node:path');
const base=process.env.WECHAT_ADMIN_UI_BASE||'http://127.0.0.1:8081';
assert(['127.0.0.1','localhost','[::1]'].includes(new URL(base).hostname));
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const password=process.env.WECHAT_ADMIN_TEST_PASSWORD;assert(password);
  assert((await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password}})).ok());
  const config=await(await context.request.get(base+'/api/config')).json();
  const page=await context.newPage(),errors=[],requests=[],writes=[];page.on('pageerror',e=>errors.push(e.message));
  const accounts=[{id:'synthetic-primary',name:'蓝色大肥鱼',primary:true,native:{connection:'online'},bot:{state:'running'}},{id:'synthetic-second',name:'测试小鲸鱼',native:{connection:'online'},bot:{state:'running'}}];
  await page.route('**/*',async route=>{
   const req=route.request(),url=new URL(req.url());if(url.origin!==base)return route.abort();
   if(url.pathname.startsWith('/api/')){
    const account=req.headers()['x-wechat-account']||url.searchParams.get('account');requests.push({path:url.pathname,account});
    if(req.method()!=='GET')writes.push(url.pathname);
    if(url.pathname==='/api/session')return route.fulfill({json:{authenticated:true,csrf:'synthetic-csrf'}});
    if(url.pathname==='/api/accounts'){
     if(req.method()==='POST'){accounts.push({id:'synthetic-added',name:'未登录的小号',native:{connection:'disconnected'},bot:{state:'disabled'}});return route.fulfill({status:201,json:{id:'synthetic-added'}})}
     return route.fulfill({json:{items:accounts,primary:accounts[0].id,supported:true}});
    }
    if(url.pathname==='/api/status')return route.fulfill({json:{account,account_name:accounts.find(x=>x.id===account)?.name,native:{authenticated:true,connection:'online'},bot:{state:'running'},key_configured:true}});
    if(url.pathname==='/api/config')return route.fulfill({json:{...config,group_rules:[{id:'synthetic-room@chatroom',name:'合成测试群',mode:'social'}]}});
    if(url.pathname==='/api/companion/state')return route.fulfill({json:{scopes:[]}});
    if(url.pathname==='/api/wechat/login')return route.fulfill({json:{id:'synthetic-operation',state:'awaiting_scan',qr_available:true,qr_generation:1}});
    if(url.pathname==='/api/wechat/login/synthetic-operation')return route.fulfill({json:{id:'synthetic-operation',state:'awaiting_scan',qr_available:true,qr_generation:1}});
    if(url.pathname.endsWith('/qr'))return route.fulfill({body:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==','base64'),contentType:'image/png'});
    if(url.pathname==='/api/logs')return route.fulfill({json:{items:[{id:'fixture-log',time:'2026-10-28T08:00:00+08:00',event:'message_received',category:'received',source:'合成测试群',source_kind:'group',thread_id:'synthetic-room@chatroom',sender:'wxid_fixture',sender_name:'合成好友',message_id:'fixture-message',content:'合成消息',status:'received'}]}});
    if(req.method()!=='GET')return route.fulfill({status:403,json:{error:'真实写入已被测试阻止'}});
    return route.fulfill({json:{items:[]}});
   }
   const filename=url.pathname==='/'?'index.html':url.pathname.split('/').pop();
   try{return route.fulfill({body:await fs.readFile(path.join(__dirname,'../internal/admin/static',filename)),contentType:({'.html':'text/html','.js':'text/javascript','.css':'text/css','.png':'image/png','.json':'application/json'})[path.extname(filename)]||'text/plain'})}catch{return route.abort()}
  });
  await page.goto(base+'/#wechat');await page.locator('.account-row').first().waitFor();
  assert.equal(await page.locator('.account-row').count(),2);
  assert.equal(await page.locator('#account-select').inputValue(),'synthetic-primary');
  await page.locator('#account-select').selectOption('synthetic-second');
  await page.waitForFunction(()=>document.querySelector('#account-select')?.value==='synthetic-second'&&document.querySelector('#account-current-name')?.textContent==='测试小鲸鱼');
  assert(new URL(page.url()).searchParams.get('account')==='synthetic-second');
  await page.locator('#start-login').click();await page.locator('#qr-box').waitFor({state:'visible'});
  assert(new URL(await page.locator('#qr-image').getAttribute('src'),base).searchParams.get('account')==='synthetic-second');
  assert(requests.filter(x=>['/api/config','/api/status'].includes(x.path)).some(x=>x.account==='synthetic-second'));
  for(const theme of ['light','dark'])for(const width of [1440,768,390]){
   await page.setViewportSize({width,height:1000});await page.locator('.topbar [data-theme-value="'+theme+'"]').click();
   assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1)),`${theme}/${width} overflow`);
   for(const selector of ['#account-list','#start-login']){const box=await page.locator(selector).boundingBox();assert(box&&box.width>0)}
  }
  await page.setViewportSize({width:1440,height:1000});
  await page.evaluate(()=>window.WechatAdmin.nav('logs'));await page.locator('.round-source').waitFor();
  assert.equal(await page.locator('.round-source').first().textContent(),'合成测试群');
  await page.evaluate(()=>window.WechatAdmin.nav('wechat'));await page.locator('#add-account').click();
  await page.waitForFunction(()=>document.querySelector('#account-select')?.value==='synthetic-added');
  assert.equal(await page.locator('.account-row').count(),3);
  assert.deepEqual(errors,[]);
  assert(writes.every(x=>x==='/api/accounts'||x==='/api/wechat/login'));
  const screenshot=process.env.WECHAT_ADMIN_UI_SCREENSHOT;if(screenshot)await page.screenshot({path:screenshot,fullPage:true});
  console.log(JSON.stringify({accountSwitch:true,scopedHeaders:true,scopedQR:true,syntheticAdd:true,nicknameFirst:true,lightDarkResponsive:true,pageErrors:errors,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
