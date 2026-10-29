/* Application chrome, bounded workspaces and read-only overview projections. */
(() => {
 'use strict';
 const $=id=>document.getElementById(id),positions=new Map();
 let section='',accountEpoch=0,tasksBusy=false,tasks=[],lastTaskRead=0;
 const labels={pending:'待处理',running:'执行中',waiting:'等待继续',completed:'已完成',cancelled:'已取消',failed:'需处理',needs_review:'等待核查',interrupted:'已中断',timed_out:'执行超时'};
 const node=(tag,text,klass)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(klass)e.className=klass;return e};
 function workspaceTab(name,focus=false){
  for(const tab of document.querySelectorAll('[data-workspace-tab]')){
   const selected=tab.dataset.workspaceTab===name;tab.setAttribute('aria-selected',String(selected));tab.tabIndex=selected?0:-1;
   $('workspace-'+tab.dataset.workspaceTab).hidden=!selected;if(selected&&focus)tab.focus();
  }
 }
 for(const tab of document.querySelectorAll('[data-workspace-tab]')){
  tab.addEventListener('click',()=>workspaceTab(tab.dataset.workspaceTab));
  tab.addEventListener('keydown',event=>{const names=['tasks','documents','processes'],index=names.indexOf(tab.dataset.workspaceTab);let next;
   if(event.key==='ArrowRight')next=names[(index+1)%3];if(event.key==='ArrowLeft')next=names[(index+2)%3];if(event.key==='Home')next=names[0];if(event.key==='End')next=names[2];
   if(next){event.preventDefault();workspaceTab(next,true)}
  });
 }
 const directory=document.querySelector('.document-directory'),utilities=node('details',undefined,'workspace-utilities');
 utilities.append(node('summary','工作区工具'));
 for(const button of [...directory.querySelectorAll(':scope > button')])if(button.id!=='agent-doc-refresh')utilities.append(button);
 directory.append(utilities);
 const docList=node('div',undefined,'document-file-list');docList.setAttribute('aria-label','文档目录');
 $('agent-doc-select').parentElement.append(docList);let docSignature='';
 function syncDocuments(){
  const select=$('agent-doc-select'),signature=JSON.stringify([...select.options].map(o=>[o.value,o.textContent,o.selected]));if(signature===docSignature)return;docSignature=signature;
  const position=docList.scrollTop;docList.replaceChildren();
  for(const option of select.options){if(!option.value)continue;const button=node('button',option.textContent,'document-file');button.type='button';button.setAttribute('aria-pressed',String(option.selected));button.addEventListener('click',()=>{select.value=option.value;select.dispatchEvent(new Event('change',{bubbles:true}));syncDocuments()});docList.append(button)}
  if(!docList.children.length)docList.append(node('p','当前范围没有文档','hint'));docList.scrollTop=position;
 }
 function renderTasks(){
  const box=$('overview-tasks');if(!box)return;const position=box.scrollTop;box.replaceChildren();
  const active=tasks.filter(t=>!['completed','cancelled'].includes(t.state));
  if(!active.length)box.append(node('p','目前没有待办，新的任务会显示在这里。','empty'));
  for(const task of active){const row=node('button',undefined,'dashboard-row');row.type='button';const meta=node('span',undefined,'row-meta');meta.append(node('strong',task.title||'未命名任务'),node('small',labels[task.state]||task.state));row.append(meta);
   if(task.checkpoint)row.append(node('p',task.checkpoint));row.addEventListener('click',()=>{window.WechatAdmin.nav('agent');workspaceTab('tasks')});box.append(row)}
  box.scrollTop=position;
 }
 async function loadTasks(force=false){
  if(tasksBusy||!window.WechatAdmin?.isAuthenticated()||!force&&Date.now()-lastTaskRead<15000)return;
  tasksBusy=true;const stamp=accountEpoch;
  try{const data=await window.WechatAdmin.api('/api/agent');if(stamp!==accountEpoch)return;tasks=data.tasks||[];lastTaskRead=Date.now();renderTasks()}
  catch(error){if(stamp===accountEpoch)$('overview-tasks').replaceChildren(node('p',error.message,'form-error'))}
  finally{tasksBusy=false}
 }
 function renderActivity(rounds){
  const box=$('overview-activity'),position=box.scrollTop;box.replaceChildren();
  const recent=rounds.filter(r=>!r.system).slice(0,10);
  if(!recent.length)box.append(node('p','还没有执行记录。收到消息后会在这里显示。','empty'));
  for(const round of recent){const row=node('button',undefined,'dashboard-row');row.type='button';const meta=node('span',undefined,'row-meta');
   meta.append(node('strong',window.WechatNames?.display(round.thread_id,round.source)||round.source),node('time',window.WechatActivity.time(round.time)));
   row.append(meta,node('small',window.WechatActivity.status(round)));
   for(const item of round.received||[])if(item.content)row.append(node('p',item.content));
   for(const item of round.sent||[])if(item.content)row.append(node('p','发送：'+item.content));
   if(!round.received?.length&&!round.sent?.length)row.append(node('p',round.proactive?'自主发起的一轮活动':'查看处理过程'));
   row.addEventListener('click',()=>window.WechatActivity.openRound(round));box.append(row);
  }
  box.scrollTop=position;
 }
 // Complete text menus retain native values/events so existing form adapters
 // and revision validation remain authoritative.
 const selects=new Map();let menu=null;
 function closeMenu(){if(!menu)return;menu.trigger.setAttribute('aria-expanded','false');menu.popup.remove();menu=null}
 function enhance(select){
  if(selects.has(select)||select.multiple||select.closest('#flow-world'))return;
  const trigger=node('button',undefined,'select-trigger');trigger.type='button';trigger.setAttribute('role','combobox');trigger.setAttribute('aria-haspopup','listbox');trigger.setAttribute('aria-expanded','false');trigger.setAttribute('aria-controls',select.id+'-options');
  select.insertAdjacentElement('afterend',trigger);select.classList.add('native-select-proxy');select.tabIndex=-1;select.setAttribute('aria-hidden','true');
  select.addEventListener('focus',()=>trigger.focus());
  const state={select,trigger};selects.set(select,state);
  const update=()=>{trigger.textContent=select.selectedOptions[0]?.textContent||select.options[0]?.textContent||'请选择';trigger.disabled=select.disabled;trigger.hidden=select.hidden;trigger.setAttribute('aria-label',select.getAttribute('aria-label')||document.querySelector('label[for="'+select.id+'"]')?.textContent||trigger.textContent)};
  state.update=update;select.addEventListener('change',update);select.addEventListener('invalid',event=>{event.preventDefault();trigger.focus();trigger.setAttribute('aria-invalid','true')});
  trigger.addEventListener('click',()=>{
   if(menu?.select===select){closeMenu();return}closeMenu();
   const popup=node('div',undefined,'select-popup');popup.id=select.id+'-options';popup.setAttribute('role','listbox');popup.setAttribute('aria-label',trigger.getAttribute('aria-label'));
   for(const option of select.options){const b=node('button',option.textContent,'select-option');b.type='button';b.setAttribute('role','option');b.setAttribute('aria-selected',String(option.selected));b.disabled=option.disabled;b.addEventListener('click',()=>{select.value=option.value;select.dispatchEvent(new Event('change',{bubbles:true}));select.dispatchEvent(new Event('input',{bubbles:true}));update();closeMenu();trigger.focus()});popup.append(b)}
   // Append to the nearest modal so focus cannot escape its native focus trap.
   (trigger.closest('dialog[open]')||document.body).append(popup);const rect=trigger.getBoundingClientRect();popup.style.width=Math.min(Math.max(rect.width,240),innerWidth-24)+'px';popup.style.left=Math.max(12,Math.min(rect.left,innerWidth-popup.offsetWidth-12))+'px';
   const available=innerHeight-rect.bottom-12;popup.style.maxHeight=Math.max(100,Math.min(innerHeight*.6,available<150?rect.top-20:available))+'px';
   popup.style.top=(available<150?Math.max(12,rect.top-popup.offsetHeight-5):rect.bottom+5)+'px';menu={select,trigger,popup};trigger.setAttribute('aria-expanded','true');(popup.querySelector('[aria-selected="true"]')||popup.querySelector('button'))?.focus();
  });
  new MutationObserver(update).observe(select,{childList:true,subtree:true,attributes:true});update();
 }
 document.addEventListener('keydown',event=>{
  if(!menu)return;
  if(event.key==='Escape'){event.preventDefault();event.stopPropagation();const trigger=menu.trigger;closeMenu();trigger.focus();return}
  if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)){
   event.preventDefault();const options=[...menu.popup.querySelectorAll('button:not(:disabled)')],index=options.indexOf(document.activeElement);
   const next=event.key==='Home'?0:event.key==='End'?options.length-1:Math.max(0,Math.min(options.length-1,index+(event.key==='ArrowDown'?1:-1)));options[next]?.focus();
  }
 },true);
 document.addEventListener('pointerdown',event=>{if(menu&&!menu.popup.contains(event.target)&&!menu.trigger.contains(event.target))closeMenu()});
 document.addEventListener('scroll',event=>{if(menu&&!menu.popup.contains(event.target))closeMenu()},true);window.addEventListener('resize',closeMenu);
 function syncSelects(){for(const select of document.querySelectorAll('select[id]'))enhance(select);for(const state of selects.values())state.update();syncDocuments()}
 syncSelects();setInterval(syncSelects,1000);
 window.addEventListener('wechat-admin:activity-updated',event=>renderActivity(event.detail.rounds));
 window.addEventListener('wechat-admin:rule-target',event=>{
  const target=[...document.querySelectorAll('.rule-row')].find(row=>row.dataset.threadId===event.detail.thread);
  for(const row of document.querySelectorAll('.rule-row'))row.classList.toggle('rule-target',row===target);
  if(target){target.scrollIntoView({block:'center'});target.querySelector('button,select,input')?.focus({preventScroll:true})}
 });
 window.addEventListener('wechat-admin:navigate',event=>{
  if(event.detail.embedded)return;const next=event.detail.section;closeMenu();
  if(section){const page=$(section);if(page)positions.set(section,page.scrollTop)}section=next;
  const page=$(next);if(page)page.scrollTop=positions.get(next)||0;
  document.body.dataset.page=next;
  const active=next==='account-actions'?'wechat':next==='mcp'||next==='mobile'?'plugins':next==='tools'?'memory':next;
  for(const link of document.querySelectorAll('.context-nav .nav-link,.local-nav .nav-link')){const selected=link.dataset.section===active;link.classList.toggle('active',selected);if(selected)link.setAttribute('aria-current','page');else link.removeAttribute('aria-current')}
  if(next==='overview'){loadTasks();renderActivity(window.WechatActivity?.rounds()||[])}syncSelects();
 });
 window.addEventListener('wechat-admin:logout',()=>{accountEpoch++;lastTaskRead=0;tasks=[];positions.clear();closeMenu();workspaceTab('tasks');for(const box of [$('overview-tasks'),$('overview-activity')])box.replaceChildren()});
 setInterval(()=>{if($('overview').classList.contains('active-page'))loadTasks()},15000);
 window.WechatConsole={workspaceTab,closeMenu,syncSelects};
})();
