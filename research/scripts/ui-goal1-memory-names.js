async page => {
 await page.reload();await page.locator('#app-view').waitFor({state:'visible'});
 const scope={id:'fixture-memory-scope',channel:'wechat',thread:'fixture@chatroom',label:'记忆展示测试群',message_count:5};
 let messages=[{role:'user',sender:'wxid-one',sender_name:'好友备注甲',sender_name_source:'remark',text:'这是备注优先的发言。'},{role:'user',sender:'wxid-two',sender_name:'同名群友',sender_name_source:'room',text:'这是群内昵称。'},{role:'user',sender:'wxid-three',sender_name:'同名群友',sender_name_source:'nickname',text:'名字相同，身份仍然不同。'},{role:'assistant',sender_name:'deepseek蓝色大肥鱼',sender_name_source:'character',text:'我会分别记住大家说的话。'},{role:'user',sender:'wxid-unknown',text:'没有同步名称时保留身份。'}];
 let writes=0;page.on('request',r=>{if(r.method()!=='GET'&&r.url().includes('/api/companion/'))writes++;});
 await page.route('**/api/companion/state',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({scopes:[scope]})}));
 await page.route('**/api/companion/memory/fixture-memory-scope',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({...scope,messages,notes:[]})}));
 await page.locator('.main-nav[data-section="memory"]').click();await page.getByRole('button',{name:/记忆展示测试群/}).click();
 await page.waitForFunction(()=>document.querySelectorAll('.memory-message').length===5);
 if(await page.locator('#memory h4,#memory-title').count())throw new Error('Duplicate conversation heading remains');
 if(await page.getByRole('button',{name:/记忆展示测试群/}).getAttribute('aria-pressed')!=='true'||await page.locator('#memory-detail').getAttribute('aria-label')!=='记忆展示测试群的记忆')throw new Error('Current conversation lost accessible selection');
 if(await page.locator('#page-title').textContent()!=='记忆管理'||await page.locator('#page-description').textContent()!==''||!await page.locator('#page-description').isHidden()||await page.locator('#memory').textContent().then(x=>x.includes('按会话整理关系备注，查看和清除本地记忆。')))throw new Error('Repeated memory title-description block remains');
 if(await page.locator('#memory h2').count()||await page.locator('#memory').getAttribute('aria-label')!=='记忆管理')throw new Error('Duplicated heading or missing accessible memory label');
 const labels=await page.locator('.memory-message b').allTextContents();
 if(JSON.stringify(labels)!==JSON.stringify(['好友备注甲','同名群友','同名群友','deepseek蓝色大肥鱼','wxid-unknown']))throw new Error('Readable speaker labels incorrect');
 const second=await page.locator('.memory-message[data-sender-id="wxid-two"]').textContent(),third=await page.locator('.memory-message[data-sender-id="wxid-three"]').textContent();
 if(!second.includes('群内昵称')||!second.includes('wxid-two')||!third.includes('昵称')||!third.includes('wxid-three'))throw new Error('Same-name speakers lost unique identities');
 if((await page.locator('.memory-message[data-sender-id="wxid-unknown"]').textContent()).split('wxid-unknown').length!==2)throw new Error('Unknown ID redundantly repeated');
 const widths=[1440,768,375];
 for(const width of widths){await page.setViewportSize({width,height:1000});const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);if(overflow)throw new Error('Memory layout overflows '+width);}
 await page.setViewportSize({width:1440,height:1000});
 for(const theme of ['light','dark']){await page.locator('.top-actions [data-theme-value="'+theme+'"]').click();await page.screenshot({path:'research/evidence/ui-goal1-step4-memory-'+theme+'.png',fullPage:false,animations:'disabled'});}
 messages=[{role:'user',sender:'wxid-tag',sender_name:'<img src=x onerror=alert(1)>',sender_name_source:'remark',text:'名称只是资料'}];
 await page.getByRole('button',{name:/记忆展示测试群/}).click();await page.waitForFunction(()=>document.querySelectorAll('.memory-message').length===1);
 if(await page.locator('.memory-message img').count()||await page.locator('.memory-message b').textContent()!=='<img src=x onerror=alert(1)>')throw new Error('Name interpreted as HTML');
 if(writes)throw new Error('Memory view changed real settings or messages');
 await page.unroute('**/api/companion/state');await page.unroute('**/api/companion/memory/fixture-memory-scope');
 return {readable_labels:true,same_name_different_ids:true,assistant_and_unknown_fallback:true,name_source_visible:true,untrusted_names_are_text:true,duplicate_conversation_heading_removed:true,redundant_memory_title_description_removed:true,selected_scope_accessible:true,responsive_widths:widths,themes:['light','dark'],real_mutations:writes,fixture_only:true};
}
