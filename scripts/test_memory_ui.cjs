'use strict';
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const assert=require('node:assert/strict');
const base=process.env.WECHAT_ADMIN_UI_BASE;assert(base&&new URL(base).hostname==='127.0.0.1');
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHANNEL?{channel:process.env.PLAYWRIGHT_CHANNEL}:{})});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:800}});
  const response=await context.request.post(base+'/api/login',{headers:{Origin:base},data:{password:'synthetic-browser-password-2026'}});assert(response.ok());
  const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base+'/#memory');await page.locator('.conversation-open').first().click();
  await page.getByRole('button',{name:'长期记忆与来源',exact:true}).click();
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
