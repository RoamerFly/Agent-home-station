const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs/promises'),path=require('node:path'),assert=require('node:assert/strict');
const root=path.resolve('internal/admin/static'),origin='http://127.0.0.1:47921',out=path.resolve('temp/workspace-refresh-ui');
(async()=>{
 await fs.mkdir(out,{recursive:true});const browser=await chromium.launch({headless:true,channel:'msedge'});
 const page=await browser.newPage({viewport:{width:1440,height:900}}),errors=[],results=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',async r=>{const u=new URL(r.request().url());assert.equal(u.origin,origin);assert(!u.pathname.startsWith('/api/'),'no live API');const name=u.pathname==='/'?'index.html':path.basename(u.pathname);let b=await fs.readFile(path.join(root,name));if(name==='index.html')b=b.toString().replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,'');await r.fulfill({body:b,contentType:({'.html':'text/html','.css':'text/css','.js':'text/javascript','.png':'image/png'})[path.extname(name)]||'text/plain'});});
 await page.goto(origin);
 await page.evaluate(()=>{
 document.getElementById('login-view').hidden=true;document.getElementById('app-view').hidden=false;
 const groups={workbench:'overview',overview:'overview',agent:'overview',wechat:'wechat',settings:'wechat',rules:'memory','account-actions':'wechat',catalog:'memory',tools:'memory',companion:'companion',voices:'companion',mobile:'wechat',provider:'wechat',plugins:'wechat',mcp:'wechat',permissions:'wechat',memory:'memory',logs:'logs',surf:'logs'};
 window.WechatAdmin={isAuthenticated:()=>false,nav:s=>{document.querySelectorAll('.page').forEach(e=>e.classList.toggle('active-page',e.id===s));document.querySelectorAll('[data-nav-group]').forEach(e=>e.hidden=e.dataset.navGroup!==groups[s]);document.querySelectorAll('.main-nav').forEach(e=>e.classList.toggle('active',e.dataset.section===groups[s]));document.getElementById('page-title').textContent=document.querySelector('.context-nav [data-section="'+s+'"]')?.textContent||s;window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section:s}}));}};
 window.WechatActivity={rounds:()=>[],time:t=>t,status:()=>''};
 document.querySelectorAll('[data-section]').forEach(b=>b.addEventListener('click',()=>window.WechatAdmin.nav(b.dataset.section)));
 for(const [id,text] of Object.entries({'native-state':'已登录','bot-state':'运行中','bot-detail':'等待新消息','key-state':'已配置','account-state':'合成账号','version-state':'本地版本','billing-today':'12,300','billing-total':'0.12 元','billing-status':'合成展示','overview-tasks':'暂无待办','overview-activity':'暂无活动'})){const e=document.getElementById(id);if(e)e.textContent=text}
 });
 for(const f of ['console.js','save-actions.js'])await page.addScriptTag({url:origin+'/assets/'+f});
 const sections=await page.locator('.content>.page').evaluateAll(es=>es.map(e=>e.id));
 for(const width of [375,768,1024,1440])for(const theme of ['light','dark']){
 await page.setViewportSize({width,height:900});await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
 for(const section of sections){await page.evaluate(s=>window.WechatAdmin.nav(s),section);await page.waitForTimeout(40);
 const check=await page.evaluate(()=>{const p=document.querySelector('.content>.active-page'),c=document.querySelector('.content');return {body:document.documentElement.scrollWidth>innerWidth+2,content:c.scrollWidth>c.clientWidth+2,page:p.scrollWidth>p.clientWidth+2}});
 results.push({width,theme,section,...check});
 if(width===1440||Object.values(check).some(Boolean))await page.screenshot({path:path.join(out,`${section}-${theme}-${width}.png`)});
 const workflow=page.locator('#'+section+' .workflow-link');
 for(let tab=0;tab<await workflow.count();tab++){
  await workflow.nth(tab).click();await page.waitForTimeout(25);
  const state=await page.evaluate(()=>{const p=document.querySelector('.active-page'),pane=p.querySelector('.workflow-pane:not([hidden])');return {overflow:p.scrollWidth>p.clientWidth+2,controls:[...pane.querySelectorAll('input,select,textarea,button')].filter(e=>e.getBoundingClientRect().height>0).length}});
  assert(!state.overflow&&state.controls>0,section+' workflow '+tab+' hidden or overflowing');
  if(width===1440)await page.screenshot({path:path.join(out,`${section}-tab${tab}-${theme}-${width}.png`)});
 }
 if(await workflow.count())await workflow.first().click();

 }
 }
 await page.setViewportSize({width:1440,height:900});await page.evaluate(()=>window.WechatAdmin.nav('overview'));
 await page.getByRole('button',{name:'流程画布',exact:true}).click();assert.equal(await page.locator('#workbench').evaluate(e=>e.classList.contains('active-page')),true);
 await page.evaluate(()=>window.WechatAdmin.nav('provider'));await page.waitForTimeout(80);assert(await page.locator('#top-save-actions button').count()>0,'save action at top');
 await page.emulateMedia({reducedMotion:'reduce'});assert.equal(await page.locator('#provider').evaluate(e=>getComputedStyle(e).animationName),'none');
 const failures=results.filter(r=>r.body||r.content||r.page);await fs.writeFile(path.join(out,'result.json'),JSON.stringify({cases:results.length,errors,failures,checks:['all-sections','four-widths','light-dark','direct-navigation','top-save','reduced-motion']},null,2));
 await browser.close();assert.deepEqual(errors,[]);assert.deepEqual(failures,[]);console.log('PASS '+results.length+' layout states');
})().catch(e=>{console.error(e);process.exitCode=1});
