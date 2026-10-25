async page => {
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 let settings={enabled:false,interval_minutes:60,max_sessions_per_day:4,topics:['海洋科学'],threads:['old-friend']};
 let allowed=['fixture-room@chatroom','fixture-friend','filehelper'];let quiet=true,posts=0,payload;let failedNames=false;
 const directory=()=>[{id:'fixture-room@chatroom',name:'冲浪测试群',kind:'group'},{id:'fixture-friend',name:'好友备注测试',kind:'friend'},{id:'filehelper',name:'文件传输助手',kind:'filehelper'}].filter(x=>allowed.includes(x.id));
 await page.route('**/api/surf',async route=>{if(route.request().method()==='POST'){posts++;payload=route.request().postDataJSON();settings={...payload};await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(settings)});return}await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({settings,allowed_threads:allowed,quiet_now:quiet,quiet_start:'23:00',quiet_end:'08:00',timezone:'Asia/Shanghai',sessions:2,share_permission:true,next:'2026-10-26T00:00:00Z',items:[]})})});
 await page.route('**/api/companion/targets',r=>r.fulfill({status:failedNames?502:200,contentType:'application/json',body:JSON.stringify(failedNames?{error:'测试目录暂不可用'}:{items:directory(),today:0,max_per_day:2})}));
 await page.locator('.main-nav[data-section="surf"]').click();await page.locator('.surf-settings summary').click();
 await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).waitFor();
 if(await page.locator('#surf-threads').count())throw new Error('Raw ID target input still present');
 const old=page.locator('.surf-target-choice[data-thread="old-friend"]');if(!await old.textContent().then(x=>x.includes('恢复回复授权')))throw new Error('Legacy revoked selection disappeared');
 await page.locator('#surf-save').click();if(posts||!await page.locator('#surf-save-state').textContent().then(x=>x.includes('取消已失去')))throw new Error('Revoked target was saved');
 await old.getByRole('checkbox').click();await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).check();await page.getByRole('checkbox',{name:'好友备注测试 冲浪分享',exact:true}).check();
 await page.locator('#surf-topics').fill('海洋科学\n轻松音乐');
 await page.locator('#surf-target-search').fill('好友备注');if(await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).count())throw new Error('Target filter failed');await page.locator('#surf-target-search').fill('');
 await page.locator('#surf-refresh').click();if(!await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).isChecked()||await page.locator('#surf-topics').inputValue()!=='海洋科学\n轻松音乐')throw new Error('Activity refresh discarded draft');
 await page.locator('.main-nav[data-section="logs"]').click();await page.locator('.main-nav[data-section="surf"]').click();if(!await page.getByRole('checkbox',{name:'好友备注测试 冲浪分享',exact:true}).isChecked()||posts)throw new Error('Navigation lost draft or saved automatically');
 await page.locator('#surf-save').click();await page.waitForFunction(()=>document.querySelector('#surf-save-state').textContent.startsWith('已保存'));
 if(posts!==1||payload.threads.length!==2||!payload.threads.includes('fixture-friend')||payload.topics.length!==2)throw new Error('Multi-target payload incorrect');
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});await page.locator('.main-nav[data-section="surf"]').click();await page.locator('.surf-settings summary').click();await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).waitFor();
 if(!await page.getByRole('checkbox',{name:'好友备注测试 冲浪分享',exact:true}).isChecked())throw new Error('Saved choice lost on reload');
 // Enabled + quiet shows the pause and keeps the existing count; no stale next time.
 settings.enabled=true;await page.locator('#surf-refresh').click();await page.waitForFunction(()=>document.querySelector('#surf-schedule').textContent.includes('不占次数'));
 const schedule=await page.locator('#surf-schedule').textContent(),hint=await page.locator('#surf-share-state').textContent();if(!schedule.includes('2 次')||schedule.includes('最早可安排')||!hint.includes('23:00–08:00')||!hint.includes('Asia/Shanghai'))throw new Error('Quiet pause/count/timezone not clear');
 failedNames=true;await page.locator('#surf-target-refresh').click();if(!await page.locator('#surf-target-state').textContent().then(x=>x.includes('保留已有选择'))||!await page.getByRole('checkbox',{name:'好友备注测试 冲浪分享',exact:true}).isChecked())throw new Error('Directory error discarded choice');failedNames=false;await page.locator('#surf-target-refresh').click();
 const widths=[1440,768,375];for(const width of widths){await page.setViewportSize({width,height:1000});await page.locator('.surf-settings').scrollIntoViewIfNeeded();if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw new Error('Surf overflow at '+width)}
 await page.setViewportSize({width:1440,height:1100});for(const theme of ['light','dark']){await page.locator('.top-actions [data-theme-value="'+theme+'"]').click();await page.locator('.surf-settings').screenshot({path:'research/evidence/ui-goal1-step4-surf-'+theme+'.png',animations:'disabled'})}
 // Selection can be cleared to deliberately browse without sharing.
 await page.getByRole('checkbox',{name:'冲浪测试群 冲浪分享',exact:true}).uncheck();await page.getByRole('checkbox',{name:'好友备注测试 冲浪分享',exact:true}).uncheck();await page.locator('#surf-save').click();await page.waitForFunction(()=>document.querySelector('#surf-save-state').textContent.startsWith('已保存'));
 if(payload.threads.length||posts!==2)throw new Error('Browse-only selection not saved');
 await page.unroute('**/api/surf');await page.unroute('**/api/companion/targets');await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 return {group_and_friend_multiselect:true,no_raw_id_target_input:true,legacy_revoked_target_visible_and_removable:true,filter_refresh_and_navigation_preserve_draft:true,save_reload_roundtrip:true,directory_failure_preserves_selection:true,browse_only_empty_selection:true,quiet_pause_zero_extra_count_and_configured_timezone_visible:true,themes:['light','dark'],responsive_widths:widths,intercepted_surf_posts:posts,real_config_writes:0,wechat_sends:0};
}
