async page => {
  await page.reload();
  await page.locator('#app-view').waitFor({state:'visible'});
  let phase='pacing'; let mutations=0;
  const status=()=>({id:'fixture-voice-paced',state:phase==='pacing'?'running':'needs_review',phase,kind:'voice',thread_id:'filehelper',reason:phase==='pacing'?'':'transport_unavailable',voice_failure_stage:phase==='pacing'?'':'voice_upload_exchange_outcome_unknown',voice_transport_kind:phase==='pacing'?'':'voice_transport_timeout',voice_failed_offset:0,voice_chunks:[{index:0,state:'accepted',duration_ms:40124,pacing_ms:0,has_receipt:true},{index:1,state:phase==='pacing'?'prepared':'needs_review',duration_ms:40124,pacing_ms:41999,has_receipt:false,prefix_retries:2}]});
  await page.route('**/api/tools/state',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({capabilities:{mediaKinds:['voice']},recipients:[{id:'filehelper',name:'文件传输助手'}],recent_sends:[{key:'fixture-paced-key',created:'2026-10-25',status:status()}]})}));
  await page.route('**/api/tools/sends/fixture-voice-paced',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(status())}));
  page.on('request',req=>{if(req.method()!=='GET'&&req.url().includes('/api/tools/'))mutations++;});
  await page.locator('.main-nav[data-section="catalog"]').click();
  await page.locator('.subnav-link[data-section="tools"]').click();
  await page.locator('#tools-recent-list').getByRole('button',{name:'恢复查询'}).click();
  await page.waitForFunction(()=>document.querySelector('#tools-result').textContent.includes('等待录音时长'));
  let text=await page.locator('#tools-result').textContent();
  for(const value of ['第 1 段 · 40.1 秒 · 微信已接受','第 2 段 · 40.1 秒 · 等待发送','发送间隔 42.0 秒'])if(!text.includes(value))throw new Error('Missing per-note evidence '+value);
  phase='submitting';await page.locator('#tools-lookup').click();
  await page.waitForFunction(()=>document.querySelector('#tools-result').textContent.includes('上传连接中断'));
  text=await page.locator('#tools-result').textContent();
  if(!text.includes('第 2 段 · 40.1 秒 · 送达情况待核实'))throw new Error('Uncertain segment displayed incorrectly');
  if(!(await page.locator('#tools-progress').textContent()).includes('本次发送已停止'))throw new Error('Stopped uncertain job shown as still waiting');
  for(const value of ['未完成数据块重试 2 次','等待连接或响应超时','上传位置 0 字节'])if(!text.includes(value))throw new Error('Missing bounded diagnostic '+value);
  if(mutations)throw new Error('Read-only status test submitted media');
  await page.screenshot({path:'research/evidence/ui-goal1-voice-paced.png',fullPage:false});
  await page.unroute('**/api/tools/state');await page.unroute('**/api/tools/sends/fixture-voice-paced');
  return {per_note_status:true,pacing_visible:true,uncertain_chunk_not_accepted:true,media_submissions:mutations,fixture_only:true};
}
