async page=>{
 const base={schema_version:2,time:'2026-10-25T06:30:00Z',source:'音乐日志回归',source_kind:'group',thread_id:'fixture@chatroom',message_id:'one'};
 const items=[
  {...base,id:'r',event:'message_received',category:'received',content:'生成一分钟的音乐',status:'received'},
  {...base,id:'a',event:'message_sent',category:'sent',content:'正在做',status:'accepted',send_id:'send-one',reason:'delivery_confirmed_by_receipt_or_sync'},
  {...base,id:'b',time:'2026-10-25T06:30:00.100Z',event:'message_sent',category:'sent',content:'正在做',status:'accepted',reason:'semantic_plan_bubble_accepted'},
  {...base,id:'c',time:'2026-10-25T06:30:00.200Z',event:'message_sent',category:'sent',content:'正在做',status:'accepted',send_id:'send-one',reason:'semantic_plan_bubble_accepted'},
  {...base,id:'d',time:'2026-10-25T06:30:01Z',event:'message_sent',category:'sent',content:'正在做',status:'accepted',send_id:'send-two'},
  {...base,id:'e',message_id:'two',event:'send_failed',category:'error',content:'结果说明',status:'unknown',send_id:'send-three'},
  {...base,id:'f',message_id:'two',time:'2026-10-25T06:30:02Z',event:'message_sent',category:'sent',content:'结果说明',status:'accepted',send_id:'send-three'}
 ];
 await page.route('**/api/logs',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({items})}));
 await page.evaluate(()=>window.WechatAdmin.nav('logs'));await page.locator('#log-search').fill('');await page.locator('#log-category').selectOption('all');await page.locator('#log-source').selectOption('all');await page.locator('#refresh-logs').click();
 await page.waitForTimeout(200);if(await page.locator('#log-new').isVisible())await page.locator('#log-new').click();
 await page.waitForFunction(()=>document.querySelector('#log-list').textContent.includes('生成一分钟的音乐'));
 const text=await page.locator('#log-list').innerText();
 if((text.match(/发送消息：正在做/g)||[]).length!==2)throw new Error('Receipt/plan duplicates not merged, or distinct sends hidden: '+text);
 if((text.match(/发送消息：结果说明/g)||[]).length!==1||text.includes('尝试发送'))throw new Error('Pending receipt not upgraded to accepted: '+text);
 await page.locator('#log-list').evaluate(e=>e.scrollTop=120);const before=await page.locator('#log-list').evaluate(e=>e.scrollTop);
 await page.locator('#refresh-logs').click();await page.waitForTimeout(200);const after=await page.locator('#log-list').evaluate(e=>e.scrollTop);if(before!==after)throw new Error('Log scroll moved');
 await page.unroute('**/api/logs');await page.locator('#refresh-logs').click();
 return {same_send_receipt_plan_deduplicated:true,different_sends_with_same_text_preserved:true,unknown_to_accepted_upgraded:true,scroll_preserved:true,fixture_only:true};
}
