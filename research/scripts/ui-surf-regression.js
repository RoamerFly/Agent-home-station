async page => {
 await page.setViewportSize({width:1440,height:1040});await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 const before=await(await page.request.get('http://127.0.0.1:8081/api/surf')).json();let payload=null;
 await page.locator('.main-nav[data-section="surf"]').click();await page.locator('#surf-schedule').filter({hasText:'自主浏览'}).waitFor();
 if(await page.locator('#page-title').textContent()!=='AI 网上冲浪')throw new Error('Not a standalone page');
 await page.locator('.surf-settings').evaluate(el=>el.open=true);await page.locator('#surf-topics').fill('海洋科学\n公开音乐');await page.locator('#surf-interval').fill('90');
 await page.route('**/api/surf',async route=>{if(route.request().method()!=='POST')return route.continue();payload=route.request().postDataJSON();await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(payload)})});
 await page.locator('#surf-save').click();await page.waitForFunction(()=>document.querySelector('#surf-save-state').textContent.includes('已保存'));
 if(payload?.interval_minutes!==90||payload.topics.length!==2)throw new Error('Surf settings lost');await page.unroute('**/api/surf');
 let items=Array.from({length:45},(_,i)=>({id:'fixture-'+i,time:'2026-10-24T15:00:'+String(i).padStart(2,'0')+'Z',event:'web_activity',origin:i%2?'surf':'chat_tool',tool:i%3?'web_fetch':'web_media_download',target_url:'https://example.org/verified/'+i,status:i%5?'succeeded':'failed',query:'冲浪界面回归 '+i,duration_ms:100+i}));
 await page.route('**/api/surf',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({...before,items})}));
 await page.locator('#surf-refresh').click();await page.locator('#surf-new').waitFor({state:'visible'});await page.locator('#surf-new').click();
 await page.locator('.surf-settings').evaluate(el=>el.open=false);await page.locator('#surf-list').evaluate(el=>el.scrollTop=300);const pos=await page.locator('#surf-list').evaluate(el=>el.scrollTop);
 items=[...items,{...items[0],id:'new-one',query:'新活动'}];await page.locator('#surf-refresh').click();await page.locator('#surf-new').waitFor({state:'visible'});
 if(await page.locator('#surf-list').evaluate(el=>el.scrollTop)!==pos||await page.locator('.surf-activity').count()!==45)throw new Error('Polling moved reading view');
 await page.locator('#surf-origin').selectOption('surf');if(await page.locator('.surf-activity').count()!==22)throw new Error('Origin filter broken');await page.locator('#surf-status').selectOption('failed');if(await page.locator('.surf-activity').count()!==4)throw new Error('Status filter broken');
 await page.locator('#surf-origin').selectOption('all');await page.locator('#surf-status').selectOption('all');
 for(const width of[375,768,1024,1440]){await page.setViewportSize({width,height:1040});const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>window.innerWidth+2);if(overflow)throw new Error('Surf page overflow '+width)}
 await page.setViewportSize({width:1440,height:1040});await page.evaluate(()=>window.scrollTo(0,0));await page.screenshot({path:'research/evidence/ui-surf-060-demo.png',animations:'disabled'});
 await page.unroute('**/api/surf');const after=await(await page.request.get('http://127.0.0.1:8081/api/surf')).json();if(JSON.stringify(before.settings)!==JSON.stringify(after.settings))throw new Error('Real surf settings changed');
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});await page.screenshot({path:'research/evidence/ui-surf-060-live.png',animations:'disabled'});
 return {standalone_surf_page:true,settings_serialized:true,trigger_and_status_filters:true,polling_preserves_scroll_and_rows:true,responsive_widths:[375,768,1024,1440],fixture_screenshot_separate:true,real_settings_unchanged:true,real_writes:0};
}
