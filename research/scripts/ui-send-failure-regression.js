async page => {
 const trace={schema_version:2,thread_id:'fixture@chatroom',source:'发送诊断界面测试',source_kind:'group',message_id:'fixture-send'};
 const items=[{...trace,id:'received-one',time:'2026-10-24T14:00:00Z',event:'message_received',category:'received',content:'测试接收正文',sender:'fixture-sender',status:'received'},{...trace,id:'failed-one',time:'2026-10-24T14:00:10Z',event:'send_failed',category:'error',content:'测试尝试发送正文',reason:'submission_outcome_unknown',status:'unknown',send_id:'fixture-native-intent',origin:'bot'}];
 await page.route('**/api/logs',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({items})}));
 await page.locator('.main-nav[data-section="logs"]').click();await page.locator('#refresh-logs').click();
 await page.waitForFunction(()=>!document.querySelector('#refresh-logs').disabled);
 if(await page.locator('#log-new').isVisible())await page.locator('#log-new').click();
 await page.locator('#log-category').selectOption('error');
 const content=await page.locator('#log-list').textContent();
 for(const text of ['收到消息：测试接收正文','尝试发送（结果不确定）：测试尝试发送正文','未取得可验证回执','fixture-native-intent'])if(!content.includes(text))throw new Error('Missing evidence field '+text);
 if(content.includes('微信已接受'))throw new Error('Unknown send presented as accepted');
 if(await page.locator('.activity-line').count()!==1)throw new Error('Trace not consolidated');
 await page.screenshot({path:'research/evidence/ui-send-failure-demo.png',fullPage:false,animations:'disabled'});
 await page.unroute('**/api/logs');await page.locator('#log-category').selectOption('all');
 return {attempted_message_visible:true,source_and_received_message_visible:true,native_reason_visible:true,native_intent_visible:true,unknown_not_presented_as_accepted:true,fixture_only:true};
}
