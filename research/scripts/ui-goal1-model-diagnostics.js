async page => {
  await page.reload(); await page.locator('#app-view').waitFor({state:'visible'});
  const trace=(id,time,event,category,extra={})=>({schema_version:2,id,thread_id:'fixture@chatroom',message_id:id.split('-')[0],source:'亲子群（界面回归）',source_kind:'group',time:'2026-10-24T14:40:'+time+'Z',event,category,...extra});
  const diag={model_attempts:2,model_max_attempts:2,model_http_status:503,model_stage:'http_status',duration_ms:1300};
  const items=[trace('failure-in','55','message_received','received',{content:'还在加功能[呲牙]',status:'received'}),trace('failure-error','57','model_failed','error',{...diag,status:'failed',reason:'model_service_unavailable'}),trace('question-in','58','message_received','received',{content:'你目前可调用的mcp工具有哪些',status:'received'}),trace('question-review','58','message_processing','decision',{reason:'direct_question_review',status:'processing'}),trace('question-decision','59','character_decision','decision',{reason:'model_chose_silence',status:'silent'}),trace('recovered-in','50','message_received','received',{content:'连接恢复后的单次回复测试',status:'received'}),trace('recovered-retry','51','model_retry','system',{...diag,model_attempts:1,status:'retrying',reason:'model_service_unavailable'}),trace('recovered-ok','52','model_recovered','system',{model_attempts:2,model_max_attempts:2,duration_ms:1200,status:'recovered'}),trace('recovered-sent','53','message_sent','sent',{content:'连接已恢复，只回复一次',status:'accepted'})];
  await page.route('**/api/logs',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({items})}));
  await page.locator('.main-nav[data-section="logs"]').click(); await page.locator('#refresh-logs').click();
  await page.waitForFunction(()=>!document.querySelector('#refresh-logs').disabled);
  if(await page.locator('#log-new').isVisible()) await page.locator('#log-new').click();
  await page.locator('#log-category').selectOption('error');
  if(await page.locator('.activity-line').count()!==1)throw new Error('Recovered request incorrectly categorized as failed');
  const text=await page.locator('#log-list').textContent();
  for(const value of ['收到消息：还在加功能[呲牙]','模型服务暂时不可用','模型请求 2/2 次','HTTP 503','服务端状态'])if(!text.includes(value))throw new Error('Missing model diagnostic '+value);
  await page.locator('.activity-line details').evaluate(el=>el.open=true);
  await page.screenshot({path:'research/evidence/ui-goal1-step1-model-error.png',fullPage:false,animations:'disabled'});
  await page.locator('#log-category').selectOption('all');
  if(await page.locator('.activity-line').count()!==3)throw new Error('Message traces were split');
  await page.locator('#log-search').fill('HTTP 503');await page.waitForFunction(()=>document.querySelectorAll('.activity-line').length===2);
  await page.locator('#log-search').fill('');await page.waitForFunction(()=>document.querySelectorAll('.activity-line').length===3);
  const all=await page.locator('#log-list').textContent();
  if(!all.includes('收到消息：你目前可调用的mcp工具有哪些')||!all.includes('角色选择保持安静'))throw new Error('Question judgement missing');
  const recovered=page.locator('.activity-line').filter({hasText:'连接恢复后的单次回复测试'});
  if(!(await recovered.textContent()).includes('微信已接受')||await recovered.locator('.activity-flow-sent').count()!==1)throw new Error('Recovery presented as duplicate sends or failure');
  await page.unroute('**/api/logs');await page.locator('#log-category').selectOption('all');
  return {failure_details_include_status_and_attempts:true,recovered_request_not_failed:true,question_decision_visible:true,message_traces_joined:true,real_mutations:0,fixture_only:true};
}
