async page => {
	await page.setViewportSize({width:1440,height:1040});
	await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 const config=await(await page.request.get('http://127.0.0.1:8081/api/config')).json();
 await page.locator('.main-nav[data-section="companion"]').click();
 for(const [id,key]of [['read-images','read_images'],['read-videos','read_videos'],['quote-replies','quote_replies'],['random-delay','random_delay'],['recover-backlog','recover_backlog'],['retry-uncertain','retry_uncertain']])if(await page.locator('#behavior-'+id).isChecked()!==config.behavior[key])throw new Error('Checkbox not hydrated '+key);
 let captured=null;await page.route('**/api/config',async route=>{if(route.request().method()!=='POST')return route.continue();captured=route.request().postDataJSON();const result={...captured,key_configured:true};delete result.api_key;await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(result)})});
 await page.locator('#behavior-delay-min-seconds').fill('7');await page.locator('#behavior-delay-max-seconds').fill('28');await page.locator('#behavior-retry-uncertain').uncheck();await page.locator('#save-behavior').click();
 await page.waitForFunction(()=>document.querySelector('#unsaved-bar').hidden);
 if(captured?.behavior.delay_min_seconds!==7||captured?.behavior.delay_max_seconds!==28||captured.behavior.retry_uncertain!==false||!captured.behavior.read_images)throw new Error('Behavior form lost settings');
 await page.unroute('**/api/config');await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 let overflow=[];for(const width of[375,768,1024,1440]){await page.setViewportSize({width,height:1040});await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo(0,0)});const bad=await page.locator('.behavior-settings').evaluate(el=>[...el.querySelectorAll('input,button,.field-grid')].some(x=>x.getBoundingClientRect().right>el.getBoundingClientRect().right+1));if(bad)overflow.push(width)}
 if(overflow.length)throw new Error('Behavior panel overflow '+overflow);
 await page.setViewportSize({width:1440,height:1040});await page.locator('.behavior-settings').scrollIntoViewIfNeeded();await page.screenshot({path:'research/evidence/ui-behavior-050.png',animations:'disabled'});
 const after=await(await page.request.get('http://127.0.0.1:8081/api/config')).json();if(JSON.stringify(config)!==JSON.stringify(after))throw new Error('Real config changed');
 return {all_behavior_controls_hydrated:true,settings_serialized:true,explicit_false_preserved:true,widths_without_panel_overflow:[375,768,1024,1440],real_configuration_unchanged:true,real_configuration_writes:0};
}
