/* Offline regression for the surf history panel. It loads the production
   renderer with only synthetic history data and never contacts a provider,
   public source, or WeChat. */
'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),fsSync=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');

const root=path.resolve('internal/admin/static');
const history={
 items:[],
 sessions:[
  {id:'legacy',at:'2026-11-05T03:00:00Z',mode:'recommendations',state:'not_shared',reason:'surf_browse_only',candidates:[{id:'legacy-item',title:'旧记录',url:'https://content.example/legacy',source:'合成来源',kind:'text'}],visited:['https://content.example/legacy']},
  {id:'ranges',at:'2026-11-05T04:00:00Z',mode:'related',state:'not_shared',reason:'surf_browse_only',candidates:[
   {id:'article',title:'正文候选',url:'https://content.example/article',source:'合成来源',kind:'text'},
   {id:'clip',title:'媒体候选',url:'https://content.example/clip',source:'合成来源',kind:'video'}
  ],observations:[
   {candidate_id:'article',state:'body_read',offset:20,returned_characters:20,total_characters:90},
   {candidate_id:'article',state:'read_failed',failure:'合成读取失败'},
   {candidate_id:'article',state:'read_unverified'},
   {candidate_id:'article',state:'legacy_unproven'},
   {candidate_id:'clip',state:'media_prepared'}
  ]}
 ]
};

(async()=>{
 const edge='C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
 const executablePath=process.env.PLAYWRIGHT_EXECUTABLE_PATH||(fsSync.existsSync(edge)?edge:'');
 const browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 try{
  const page=await browser.newPage(),errors=[];
  page.setDefaultTimeout(5000);page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());
   assert.equal(url.origin,'http://synthetic.local','offline replay attempted a non-synthetic request');
   const name=url.pathname==='/'?'index.html':path.basename(url.pathname);
   let body=await fs.readFile(path.join(root,name));
   if(name==='index.html')body=Buffer.from(body.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,''));
   await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'});
  });
  await page.goto('http://synthetic.local');
  await page.evaluate(data=>{
   document.querySelector('#login-view').hidden=true;document.querySelector('#app-view').hidden=false;
   document.querySelectorAll('.page').forEach(node=>{node.hidden=node.id!=='surf';node.classList.toggle('active-page',node.id==='surf')});
   window.calls=[];
   window.WechatRounds={activityTarget:()=>null};
   window.WechatAdmin={isAuthenticated:()=>true,notice:()=>{},refreshStatus:async()=>{},api:async(url,opt={})=>{
    const method=opt.method||'GET';window.calls.push({url,method});
    if(url==='/api/surf'&&method==='GET')return {settings:{enabled:false,force_share:false,topics:['合成'],threads:[],discovery:{sources:[{id:'fixture',name:'合成来源',type:'rss',url:'https://content.example/feed',kind:'text',enabled:true}],weights:{recommendations:1},cooldown_days:7,max_sources:1,max_depth:0}},quiet_now:false,next:'',sessions:0,quiet_start:'23:00',quiet_end:'08:00',timezone:'Asia/Shanghai',share_permission:false,allowed_threads:[],items:[]};
    if(url==='/api/companion/targets'&&method==='GET')return {items:[]};
    if(url==='/api/surf/history'&&method==='GET')return data;
    throw new Error('unexpected synthetic API '+method+' '+url);
   }};
  },history);
  for(const script of ['controls.js','refresh.js','surf.js'])await page.addScriptTag({path:path.join(root,script)});
  await page.evaluate(()=>window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section:'surf'}})));
  await page.locator('#surf-history-panel').evaluate(node=>{node.open=true;node.dispatchEvent(new Event('toggle'))});
  await page.locator('#surf-history-list .surf-history-session').count().then(count=>assert.equal(count,2,'synthetic history sessions did not render'));
  await page.locator('#surf-history-list .surf-history-session').evaluateAll(rows=>rows.forEach(row=>{row.open=true}));
  const text=await page.locator('#surf-history-list').innerText();
  assert.match(text,/发现候选/,'candidate discovery must not claim body read');
  assert.match(text,/已读取正文第 21–40 字（共 90 字）/,'partial body range must display exact returned coverage');
  assert.match(text,/读取失败/,'failed read must remain distinct from an unverified read');
  assert.match(text,/读取范围未核实/,'unverified read must remain visible');
  assert.match(text,/旧版工具未记录正文范围/,'older readers with no coverage must be labelled');
  assert.match(text,/已准备媒体/,'prepared media must remain distinct from body text');
  assert.match(text,/旧记录未保存读取范围/,'legacy visits need an explicit compatibility label');
  const initialCalls=await page.evaluate(()=>window.calls.slice());
  const allowedReadCalls=new Set(['/api/surf','/api/companion/targets','/api/surf/history']);
  assert(initialCalls.some(call=>call.url==='/api/surf/history'),'opening history did not request synthetic history');
  assert(initialCalls.every(call=>allowedReadCalls.has(call.url)),'opening history contacted more than its metadata endpoints');
  assert(initialCalls.every(call=>call.method==='GET'),'offline history view attempted to mutate state');
  for(const width of [1440,375])for(const theme of ['light','dark']){
   await page.setViewportSize({width,height:900});
   await page.evaluate(value=>{document.documentElement.dataset.theme=value;document.querySelector('#surf-history-panel').open=true},theme);
   await page.waitForTimeout(80);
   const overflow=await page.evaluate(()=>{
    const box=document.querySelector('#surf-history-list');
    return document.documentElement.scrollWidth>innerWidth+2||box.scrollWidth>box.clientWidth+2;
   });
   assert(!overflow,`surf history overflows at ${width}px in ${theme} theme`);
   if(process.env.WECHAT_ADMIN_UI_SCREENSHOTS){
    await fs.mkdir(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,{recursive:true});
    await page.screenshot({path:path.join(process.env.WECHAT_ADMIN_UI_SCREENSHOTS,`surf-history-${theme}-${width}.png`),fullPage:true});
   }
  }
  assert.deepEqual(errors,[],'production surf page raised browser errors: '+errors.join('; '));
  console.log(JSON.stringify({offline:true,observations:['candidate_metadata','partial_body','read_failed','read_unverified','legacy_unproven','media_prepared','legacy_visited'],viewports:2,themes:2,providerCalls:0,wechatSends:0}));
 }finally{await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
