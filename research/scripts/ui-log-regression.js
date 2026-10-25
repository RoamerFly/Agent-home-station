async page => {
 const make=(i,category,extra={})=>({schema_version:2,id:i+'-'+category,time:'2026-10-24T12:'+String(30+Math.floor(i/60)).padStart(2,'0')+':'+String(i%60).padStart(2,'0')+'Z',category,event:category==='received'?'message_received':category==='sent'?'message_sent':'character_decision',source:'机器人测试群（界面回归）',source_kind:'group',thread_id:'fixture@chatroom',message_id:'fixture-'+i,...extra});
 let items=[];
 for(let i=0;i<40;i++){items.push(make(i,'received',{content:'界面回归收到消息 '+i,status:'received'}));items.push(make(i,'decision',{status:'silent',reason:'model_chose_silence'}));if(i%3!==0)items.push(make(i,'sent',{content:'界面回归发送消息 '+i,status:'accepted'}))}
 items.push(make(50,'decision',{origin:'proactive',reason:'proactive_trigger'}),make(50,'sent',{content:'界面回归主动消息',status:'accepted'}));
 await page.route('**/api/logs',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({items})}));
 await page.setViewportSize({width:1440,height:1040});await page.locator('.main-nav[data-section="logs"]').click();
 await page.waitForFunction(()=>!document.querySelector('#log-new').hidden||document.querySelectorAll('.activity-line').length>=40);
 if(await page.locator('#log-new').isVisible())await page.locator('#log-new').click();
 if(await page.locator('.activity-line').count()!==41)throw new Error('Message trace not consolidated');
 if(!await page.locator('#log-list').textContent().then(t=>t.includes('主动触发')&&t.includes('收到消息：')&&t.includes('发送消息：')))throw new Error('Missing flow fields');
 const timestamp=await page.locator('.activity-line time').first().textContent();if(!/^2026-10-24-20:\d{2}:\d{2}$/.test(timestamp))throw new Error('Wrong timezone/format '+timestamp);
 await page.locator('#log-source').selectOption('group');await page.locator('#log-search').fill('界面回归');await page.waitForFunction(()=>document.querySelectorAll('.activity-line').length===41);
 await page.locator('#log-list').evaluate(el=>el.scrollTop=450);const before=await page.locator('#log-list').evaluate(el=>({position:el.scrollTop,first:el.firstElementChild.dataset.logId}));
 items=[...items,make(51,'received',{content:'界面回归新消息',status:'received'})];await page.locator('#refresh-logs').click();await page.locator('#log-new').waitFor({state:'visible'});
 const after=await page.locator('#log-list').evaluate(el=>({position:el.scrollTop,first:el.firstElementChild.dataset.logId}));if(JSON.stringify(before)!==JSON.stringify(after))throw new Error('Polling moved the reading view');
 if(await page.locator('#log-source').inputValue()!=='group'||await page.locator('#log-search').inputValue()!=='界面回归')throw new Error('Filters reset');
 await page.locator('#log-new').click();if(await page.locator('.activity-line').count()!==42)throw new Error('Pending update not applied');
 await page.locator('#log-category').selectOption('sent');if(await page.locator('.activity-line').count()!==27)throw new Error('Send filter incorrect');
 await page.locator('#log-category').selectOption('all');await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo({top:0,behavior:'instant'})});
 await page.screenshot({path:'research/evidence/ui-refresh-20261024-logs-demo.png',fullPage:false,animations:'disabled'});
 await page.unroute('**/api/logs');await page.locator('#log-search').fill('');await page.locator('#log-source').selectOption('all');
 return {message_flows_joined:true,proactive_flow:true,local_timestamp_format:true,polling_keeps_scroll_and_dom:true,filters_preserved:true,pending_updates_explicit:true,fixture_only:true};
}
