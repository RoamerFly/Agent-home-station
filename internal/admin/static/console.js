/* Application chrome, bounded workspaces and read-only overview projections. */
(() => {
 'use strict';
 const $=id=>document.getElementById(id),positions=new Map();
 const heading=$('page-title').parentElement;
 heading.classList.add('page-heading');
 for(const navigation of document.querySelectorAll('.context-nav'))heading.append(navigation);
 // Page-level navigation, filters and actions share one owned toolbar.
 for(const page of document.querySelectorAll('.content>.page')){
  const controls=[...page.children].filter(e=>e.matches('.local-nav,.page-toolbar,.page-actions')&&e.querySelector('button,input,select,a'));
  if(controls.length){const bar=document.createElement('div');bar.className='page-toolbar-card';page.insertBefore(bar,controls[0]);for(const control of controls)bar.append(control)}
 }
 for(const [id,status] of [['memory','conversation-state'],['voices','voice-state']]){
  const page=$(id),control=page.querySelector('.conversation-toolbar,.page-toolbar-card'),bar=control.classList.contains('page-toolbar-card')?control:document.createElement('div');
  if(bar!==control){bar.className='page-toolbar-card';page.insertBefore(bar,control);bar.append(control)}bar.append($(status));
 }
 for(const id of ['wechat','mcp','permissions','settings','catalog','tools','account-actions','surf'])$(id).classList.add('page-surface');
 document.querySelector('.plugin-browser').append(document.querySelector('.plugin-guide'));

 // Group controls by the task being performed. Original nodes/handlers and
 // form ownership survive; navigation does not reset values or move focus.
 function expandPanel(panel){
  if(panel?.tagName!=='DETAILS')return panel;
  const article=document.createElement('article');for(const a of panel.attributes)if(a.name!=='open')article.setAttribute(a.name,a.value);
  article.classList.remove('disclosure');
  for(const child of [...panel.childNodes]){if(child.nodeName==='SUMMARY'){const h=document.createElement('h3');h.textContent=child.textContent;article.append(h)}else article.append(child)}panel.replaceWith(article);return article;
 }
 function workflow(page,groups){
  const nav=document.createElement('nav');nav.className='workflow-nav';nav.setAttribute('aria-label','功能分组');
  const body=document.createElement('div');body.className='workflow-body';page.classList.add('workflow-page');
  const pairs=[];
  for(const [label,nodes] of groups){
   const pane=document.createElement('div');pane.className='workflow-pane';pane.id=page.id+'-workflow-'+pairs.length;
   for(let n of nodes.filter(Boolean))pane.append(expandPanel(n));
   const button=document.createElement('button');button.type='button';button.textContent=label;button.setAttribute('aria-controls',pane.id);button.className='workflow-link';
   button.addEventListener('click',()=>{for(const p of pairs){p.pane.hidden=p.button!==button;p.button.setAttribute('aria-pressed',String(p.button===button))}window.WechatSaveActions?.sync()});
   pairs.push({button,pane});nav.append(button);body.append(pane);
  }
  const toolbar=page.querySelector(':scope>.page-toolbar-card');if(toolbar)toolbar.after(nav);else page.prepend(nav);nav.after(body);
  pairs.forEach((p,i)=>{p.pane.hidden=i!==0;p.button.setAttribute('aria-pressed',String(i===0))});
 }
 const permissionPage=$('permissions'),authority=permissionPage.querySelector('.agent-authority'),proactive=$('proactive-panel');
 const others=[...permissionPage.children].filter(e=>e.matches('details.panel')&&e!==authority&&e!==proactive&&e.id!=='channels-settings-panel');
 const quiet=others.find(e=>e.querySelector('#quiet-start')),operations=others.find(e=>e.querySelector('#agent-operation-list'));
 workflow($('provider'),[['聊天模型',[$('config-form')]],['声音与歌曲',[$('provider').querySelector('.speech-panel')]],['分享内容读取',[$('channels-settings-panel')]]]);
 workflow(permissionPage,[['聊天与发送',[$('permissions-panel')]],['自主行为',[quiet,proactive]],['工作区与账号授权',[authority,operations]]]);
 const preview=document.querySelector('#companion .companion-grid>aside>details');if(preview)expandPanel(preview);
 const overview=$('overview'),billing=overview.querySelector('.billing-panel');overview.querySelector('.metric-grid').after(billing);
 for(const grid of document.querySelectorAll('.field-grid'))for(const cell of [...grid.children])if(!cell.children.length&&!cell.textContent.trim())cell.remove();

 let section='',accountEpoch=0,tasksBusy=false,tasks=[],lastTaskRead=0,taskSignature='',activitySignature='';
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
  const box=$('overview-tasks');if(!box)return;const signature=JSON.stringify([accountEpoch,tasks]);if(signature===taskSignature)return;taskSignature=signature;const position=box.scrollTop;box.replaceChildren();
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
  const recent=rounds.filter(r=>!r.system).slice(0,10),signature=JSON.stringify([accountEpoch,recent]);if(signature===activitySignature)return;activitySignature=signature;
  const box=$('overview-activity'),position=box.scrollTop;box.replaceChildren();
  if(!recent.length)box.append(node('p','还没有执行记录。收到消息后会在这里显示。','empty'));
  for(const round of recent){const row=node('button',undefined,'dashboard-row');row.type='button';const meta=node('span',undefined,'row-meta');
   meta.append(node('strong',window.WechatNames?.display(round.thread_id,round.source)||round.source),node('time',window.WechatActivity.time(round.time)));
   row.append(meta,node('small',window.WechatActivity.status(round)));
   const summary=window.WechatActivity.activitySummary(round);
   if(summary)row.append(node('p',summary));
   else for(const item of round.received||[])if(item.content)row.append(node('p',item.content));
   for(const item of round.sent||[])if(item.content)row.append(node('p','发送：'+item.content));
   if(!summary&&!round.received?.length&&!round.sent?.length)row.append(node('p','查看处理过程'));
   row.addEventListener('click',()=>window.WechatActivity.openRound(round));box.append(row);
  }
  box.scrollTop=position;
 }
 // Keep native selects. Document options change only when the workspace loads.
 function syncSelects(){syncDocuments()}
 const documentSelect=$('agent-doc-select');
 documentSelect.addEventListener('change',syncDocuments);
 new MutationObserver(syncDocuments).observe(documentSelect,{childList:true,subtree:true});
 syncDocuments();
 window.addEventListener('wechat-admin:activity-updated',event=>renderActivity(event.detail.rounds));
 window.addEventListener('wechat-admin:rule-target',event=>{
  const target=[...document.querySelectorAll('.rule-row')].find(row=>row.dataset.threadId===event.detail.thread);
  for(const row of document.querySelectorAll('.rule-row'))row.classList.toggle('rule-target',row===target);
  if(target)target.classList.add('rule-target')
 });
 window.addEventListener('wechat-admin:navigate',event=>{
  if(event.detail.embedded)return;const next=event.detail.section;
  if(section){const page=$(section);if(page)positions.set(section,page.scrollTop)}section=next;
  const page=$(next);if(page)page.scrollTop=positions.get(next)||0;
  document.body.dataset.page=next;
  const active=next==='account-actions'?'wechat':next==='mcp'?'plugins':next==='tools'?'memory':next;
  for(const link of document.querySelectorAll('.context-nav .nav-link,.local-nav .nav-link')){const selected=link.dataset.section===(link.closest('.local-nav')?next:active);link.classList.toggle('active',selected);if(selected)link.setAttribute('aria-current','page');else link.removeAttribute('aria-current')}
  if(next==='overview'){loadTasks();renderActivity(window.WechatActivity?.rounds()||[])}syncSelects();
 });
 window.addEventListener('wechat-admin:logout',()=>{accountEpoch++;lastTaskRead=0;tasks=[];positions.clear();workspaceTab('tasks');for(const box of [$('overview-tasks'),$('overview-activity')])box.replaceChildren()});
 setInterval(()=>{if(!document.hidden&&$('overview').classList.contains('active-page'))loadTasks()},15000);
 window.WechatConsole={workspaceTab,syncSelects};
})();
