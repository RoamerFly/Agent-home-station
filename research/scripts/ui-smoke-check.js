async page => {
  await page.locator('#app-view').waitFor({state:'visible'});
  const checks={pages:[],widths:[],consoleErrors:[]};
  page.on('pageerror',error=>checks.consoleErrors.push(error.message));
  for(const id of ['overview','wechat','rules','account-actions','catalog','tools','companion','provider','plugins','mcp','permissions','memory','logs']){
    const button=page.locator('.nav-link[data-section="'+id+'"]').first();
    if(!await button.isVisible()){
      const group=({rules:'wechat','account-actions':'wechat',tools:'catalog',provider:'companion',mcp:'plugins',permissions:'plugins'})[id];
      if(group)await page.locator('.main-nav[data-section="'+group+'"]').click();
    }
    await button.click();await page.locator('#'+id+'.active-page').waitFor({state:'visible'});
    if(await page.locator('.main-nav.active').count()!==1)throw new Error('Invalid main navigation state '+id);
    checks.pages.push(id);
  }
  await page.locator('.main-nav[data-section="companion"]').click();
  await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo({top:0,behavior:'instant'})});
  await page.screenshot({animations:'disabled',path:'research/evidence/ui-refresh-20261024-role-light.png',fullPage:true});
  await page.getByRole('button',{name:'深色',exact:true}).click();
  await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo({top:0,behavior:'instant'})});
  await page.screenshot({animations:'disabled',path:'research/evidence/ui-refresh-20261024-role-dark.png',fullPage:true});
  await page.getByRole('button',{name:'浅色',exact:true}).click();
  await page.locator('.main-nav[data-section="plugins"]').click();
  await page.locator('.plugin-installed[data-id="text_to_speech"]').waitFor();
  await page.locator('.plugin-installed[data-id="text_to_speech"]').click();
  if(await page.locator('#plugin-display-name').inputValue()!=='文字转语音 API')throw new Error('Quick form did not hydrate');
  if(await page.locator('#plugin-api-key').isDisabled())throw new Error('API key placeholder not detected');
  await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo({top:0,behavior:'instant'})});
  await page.screenshot({animations:'disabled',path:'research/evidence/ui-refresh-20261024-plugins-light.png',fullPage:true});
  for(const width of [1440,1024,768,375]){
    await page.setViewportSize({width,height:900});
    const layout=await page.evaluate(()=>({width:innerWidth,body:document.documentElement.scrollWidth,client:document.documentElement.clientWidth}));
    if(layout.body>layout.client+1)throw new Error('Horizontal overflow '+JSON.stringify(layout));
    checks.widths.push(layout);
  }
  await page.getByRole('button',{name:'打开菜单',exact:true}).click();
  await page.locator('.sidebar.open').waitFor();
  if(await page.locator('#drawer-scrim').isHidden())throw new Error('Missing mobile scrim');
  await page.keyboard.press('Escape');
  await page.locator('#drawer-scrim').waitFor({state:'hidden'});
  if(!await page.locator('#menu-toggle').evaluate(el=>el===document.activeElement))throw new Error('Menu focus not restored');
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.evaluate(()=>{document.activeElement?.blur();window.scrollTo({top:0,behavior:'instant'})});
  await page.screenshot({animations:'disabled',path:'research/evidence/ui-refresh-20261024-plugins-mobile.png',fullPage:true});
  checks.sidebarWidth=await page.locator('.sidebar').evaluate(el=>el.getBoundingClientRect().width);
  await page.setViewportSize({width:1440,height:1040});
  await page.emulateMedia({reducedMotion:'no-preference'});
  return checks;
}
