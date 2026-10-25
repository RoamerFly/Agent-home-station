async page => {
 await page.unroute('**/api/config');await page.unroute('**/api/companion/targets');
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 const actual=await page.evaluate(async()=>{const r=await fetch('/api/config');if(!r.ok)throw new Error('config unavailable');return r.json()});
 let config={...actual,enabled:false,private_enabled:true,direct_ids:['fixture-friend'],group_rules:[{id:'fixture-room@chatroom',name:'问候测试群',mode:'mention',prefix:''}],companion:{...actual.companion,enabled:true,proactive_idle_minutes:60,proactive_max_per_day:3,proactive_threads:[],proactive_rules:[],permissions:{...actual.companion.permissions,proactive:true}}};
 let saves=0,payload;
 await page.route('**/api/config',async r=>{if(r.request().method()==='POST'){saves++;payload=r.request().postDataJSON();config={...payload,api_key:undefined,key_configured:true}}await r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(config)})});
 await page.route('**/api/companion/targets',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({today:0,max_per_day:3,items:[{id:'fixture-room@chatroom',name:'问候测试群',kind:'group',state:{reason:'no_reliable_context',today:0}},{id:'fixture-friend',name:'好友备注测试',kind:'friend',state:{reason:'idle_wait',today:0}},{id:'filehelper',name:'文件传输助手',kind:'filehelper',state:{reason:'not_selected',today:0}}]})}));
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});await page.locator('.main-nav[data-section="plugins"]').click();await page.locator('.nav-link[data-section="permissions"]').click();
 const group=page.locator('.proactive-target[data-thread="fixture-room@chatroom"]'),friend=page.locator('.proactive-target[data-thread="fixture-friend"]');
 await page.getByRole('checkbox',{name:'问候测试群 主动问候',exact:true}).check();if(!await page.getByRole('checkbox',{name:'问候测试群 主动问候',exact:true}).evaluate(x=>x===document.activeElement))throw new Error('Checkbox lost keyboard focus');await page.getByRole('checkbox',{name:'好友备注测试 主动问候',exact:true}).check();
 await group.getByRole('spinbutton',{name:'问候测试群 闲置分钟'}).fill('15');await group.getByRole('spinbutton',{name:'问候测试群 每日机会'}).fill('2');await friend.getByRole('spinbutton',{name:'好友备注测试 闲置分钟'}).fill('120');
 await page.locator('#proactive-search').fill('好友备注');if(await group.count())throw new Error('Search failed');await page.locator('#proactive-search').fill('');
 if(await group.getByRole('spinbutton',{name:'问候测试群 闲置分钟'}).inputValue()!=='15')throw new Error('Search discarded target draft');
 await page.locator('.main-nav[data-section="logs"]').click();await page.locator('.main-nav[data-section="plugins"]').click();await page.locator('.nav-link[data-section="permissions"]').click();
 if(await friend.getByRole('spinbutton',{name:'好友备注测试 闲置分钟'}).inputValue()!=='120'||saves)throw new Error('Navigation lost draft or saved automatically');
 await page.locator('#save-proactive').click();await page.waitForFunction(()=>document.querySelector('#unsaved-bar').hidden);
 if(saves!==1||payload.companion.proactive_threads.length!==2||!payload.companion.proactive_threads.includes('fixture-friend'))throw new Error('Selected targets not saved');
 const rules=payload.companion.proactive_rules;if(rules.find(x=>x.thread==='fixture-room@chatroom')?.idle_minutes!==15||rules.find(x=>x.thread==='fixture-room@chatroom')?.max_per_day!==2||rules.find(x=>x.thread==='fixture-friend')?.idle_minutes!==120)throw new Error('Per-target settings not saved');
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});await page.locator('.main-nav[data-section="plugins"]').click();await page.locator('.nav-link[data-section="permissions"]').click();await friend.waitFor();
 if(!(await friend.getByRole('checkbox').isChecked())||await friend.getByRole('spinbutton',{name:'好友备注测试 闲置分钟'}).inputValue()!=='120')throw new Error('Reload lost persisted settings');
 await page.locator('#proactive-search').fill('找不到的目标');if(!(await page.locator('#unsaved-bar').isHidden())||!await page.locator('#proactive-targets').textContent().then(x=>x.includes('没有匹配')))throw new Error('Search marks settings dirty or lacks empty state');await page.locator('#proactive-search').fill('');
 await group.getByRole('spinbutton',{name:'问候测试群 闲置分钟'}).fill('0');await page.locator('#save-proactive').click();if(saves!==1||await page.locator('#proactive-targets [aria-invalid="true"]').count()!==1)throw new Error('Invalid idle not prevented inline');
 await group.getByRole('spinbutton',{name:'问候测试群 闲置分钟'}).fill('15');await page.locator('#save-proactive').click();await page.waitForFunction(()=>document.querySelector('#unsaved-bar').hidden);
 const widths=[1440,768,375];for(const width of widths){await page.setViewportSize({width,height:1000});await page.locator('#proactive-panel').scrollIntoViewIfNeeded();if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1))throw new Error('Overflow at '+width)}
 await page.setViewportSize({width:1440,height:1100});for(const theme of ['light','dark']){await page.locator('.top-actions [data-theme-value="'+theme+'"]').click();await page.locator('#proactive-panel').screenshot({path:'research/evidence/ui-goal1-step3-proactive-'+theme+'.png',animations:'disabled'})}
 // Revoked reply rules must remain visible for deselection instead of silently sending elsewhere.
 await page.locator('.main-nav[data-section="wechat"]').click();await page.locator('.nav-link[data-section="rules"]').click();await page.locator('#private-enabled').uncheck();await page.locator('.main-nav[data-section="plugins"]').click();await page.locator('.nav-link[data-section="permissions"]').click();
 if(!await friend.textContent().then(x=>x.includes('恢复回复授权'))||!await friend.getByRole('spinbutton',{name:'好友备注测试 闲置分钟'}).isDisabled())throw new Error('Revoked target not marked');
 await friend.getByRole('checkbox').click();if((await page.evaluate(()=>window.WechatProactive.settings())).proactive_threads.includes('fixture-friend'))throw new Error('Revoked target cannot be removed');
 await page.locator('#save-proactive').click();await page.waitForFunction(()=>document.querySelector('#unsaved-bar').hidden);
 // Restore server-owned form snapshot by reload; all POSTs were intercepted.
 await page.unroute('**/api/config');await page.unroute('**/api/companion/targets');await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 return {group_and_friend_checkbox_selection:true,checkbox_keeps_keyboard_focus:true,per_target_idle_and_daily_opportunities:true,draft_preserved_through_filter_and_navigation:true,search_does_not_dirty_settings:true,save_and_reload_roundtrip:true,invalid_idle_prevented_inline:true,revoked_target_visible_and_removable:true,themes:['light','dark'],responsive_widths:widths,intercepted_config_posts:saves,real_config_writes:0,wechat_sends:0};
}
