'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const base=process.env.WECHAT_ADMIN_UI_BASE;assert(base&&new URL(base).hostname==='127.0.0.1');
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:800}});
  const response=await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password:'synthetic-browser-password-2026'}});assert(response.ok());
  const page=await context.newPage(),errors=[],shots=process.env.WECHAT_ADMIN_UI_SCREENSHOTS;page.on('pageerror',e=>errors.push(e.message));
  async function screenshot(name){if(!shots)return;fs.mkdirSync(shots,{recursive:true});await page.screenshot({path:require('node:path').join(shots,name),fullPage:true})}
  await page.goto(base+'/#memory');await page.locator('.conversation-open').first().click();
  const scope=await page.locator('.conversation-row[data-selected="true"]').getAttribute('data-scope-id');assert(scope);
  const experienceResponse=await context.request.get(base+'/api/companion/memory/'+encodeURIComponent(scope)+'/experiences?limit=20');assert(experienceResponse.ok());
  const experiencePage=await experienceResponse.json(),experience=experiencePage.items?.find(x=>x.detail==='周日一起画海边明信片');assert(experience,'fixture experience missing');
  await page.getByRole('button',{name:'会话记忆',exact:true}).click();await page.locator('dialog[open]').getByRole('button',{name:'共同经历',exact:true}).click();
  await page.locator('dialog[open]').getByRole('heading',{name:'共同约定',exact:true}).waitFor();
  await page.locator('.memory-experience-item').filter({hasText:'周日一起画海边明信片'}).waitFor();
  const initialPostcard=page.locator('.memory-experience-item').filter({hasText:'周日一起画海边明信片'}),initialWedding=page.locator('.memory-experience-item').filter({hasText:'今天参加同事家的喜酒'});await initialPostcard.getByText(/发言者：合成用户/).waitFor();await initialWedding.getByText(/发言者：小林/).waitFor();await screenshot('memory-experiences-desktop.png');
  await initialPostcard.getByRole('button',{name:'查看来源',exact:true}).click();await page.locator('dialog[open]').last().locator('pre').filter({hasText:'我们约好周日一起画海边明信片。'}).waitFor();await page.keyboard.press('Escape');
  await page.setViewportSize({width:375,height:812});await screenshot('memory-experiences-narrow.png');assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2)));
  await page.setViewportSize({width:1280,height:800});
  await initialPostcard.getByRole('button',{name:'更正',exact:true}).click();await page.getByLabel('更正内容',{exact:true}).fill('周一一起画海边明信片');await page.getByRole('button',{name:'从当前会话选择原话',exact:true}).click();await page.getByLabel('搜索原话',{exact:true}).fill('改到周一');await page.locator('dialog[open]').last().getByRole('button',{name:'搜索',exact:true}).click();const reschedule=page.locator('dialog[open]').last().locator('.conversation-summary').filter({hasText:'改到周一一起画海边明信片。'});await reschedule.getByRole('button',{name:'选用这条',exact:true}).click();await page.getByRole('button',{name:'保存更正',exact:true}).click();
  await page.locator('.memory-experience-item').filter({hasText:'周一一起画海边明信片'}).waitFor();
  const postcard=page.locator('.memory-experience-item').filter({hasText:'周一一起画海边明信片'});await postcard.getByRole('button',{name:'遗忘',exact:true}).click();await page.getByRole('button',{name:'确认遗忘',exact:true}).click();
  const wedding=page.locator('.memory-experience-item').filter({hasText:'今天参加同事家的喜酒'});await wedding.waitFor();await wedding.getByRole('button',{name:'遗忘',exact:true}).click();await page.getByRole('button',{name:'确认遗忘',exact:true}).click();await page.getByText('暂时没有共同经历。之后形成的约定和可核对经历会显示在这里。',{exact:true}).waitFor();
  await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'会话记忆',exact:true}).click();await page.locator('dialog[open]').getByRole('button',{name:'全部记忆',exact:true}).click();
  await page.getByLabel('搜索内容',{exact:true}).fill('红茶');await page.locator('dialog[open]').getByRole('button',{name:'搜索',exact:true}).click();
  await page.locator('dialog[open]').getByRole('heading',{name:'偏好 · 有效',exact:true}).waitFor();
  await page.getByRole('button',{name:'查看来源',exact:true}).click();await page.locator('dialog[open]').last().locator('pre').filter({hasText:'我喜欢红茶，不喝咖啡。'}).waitFor();await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'固定',exact:true}).click();await page.getByRole('button',{name:'取消固定',exact:true}).waitFor();
  await page.getByRole('button',{name:'遗忘',exact:true}).click();await page.getByRole('button',{name:'确认遗忘',exact:true}).click();await page.getByText('没有匹配记忆。自动整理完成后可在这里检查来源。',{exact:true}).waitFor();
  await page.getByLabel('搜索原始消息',{exact:true}).check();await page.locator('dialog[open]').getByRole('button',{name:'搜索',exact:true}).click();await page.getByText('没有匹配记忆。自动整理完成后可在这里检查来源。',{exact:true}).waitFor();
  await page.keyboard.press('Escape');await page.evaluate(()=>window.WechatAdmin.nav('wechat'));await page.getByRole('button',{name:'记忆设置',exact:true}).click();
  await page.getByLabel('批量整理消息数',{exact:true}).fill('14');await page.getByRole('button',{name:'保存记忆设置',exact:true}).click();await page.getByText('已保存，后续维护与召回使用新设置。',{exact:true}).waitFor();
  await page.keyboard.press('Escape');await page.getByRole('button',{name:'记忆设置',exact:true}).click();assert.equal(await page.getByLabel('批量整理消息数',{exact:true}).inputValue(),'14');
  await page.setViewportSize({width:375,height:812});assert(!(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2)));const dialog=page.locator('dialog[open]');assert(!(await dialog.evaluate(e=>e.scrollWidth>e.clientWidth+2)));
  assert.deepEqual(errors,[]);console.log(JSON.stringify({sources:true,pinning:true,forgettingBlocksOriginals:true,settingsPersist:true,mobile:true,pageErrors:errors,realWechatSends:0}));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
