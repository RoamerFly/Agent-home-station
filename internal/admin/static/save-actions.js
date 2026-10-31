/* Keep original controls and handlers; move only their presentation. */
(() => {
 'use strict';
 const top=document.querySelector('.top-actions'),host=document.createElement('div');
 host.id='top-save-actions';host.className='top-save-actions';host.setAttribute('aria-label','当前页面保存操作');top.append(host);
 const theme=top.querySelector('.theme-control');document.querySelector('.sidebar-bottom').insertBefore(theme,document.getElementById('logout'));
 let serial=0,frame=0,entries=[];const known=new WeakSet();
 const isSave=button=>/^保存|^确认保存/.test(button.textContent.trim());
 function register(button){
  if(known.has(button)||!isSave(button))return;known.add(button);
  const form=button.form;if(form){if(!form.id)form.id='save-form-'+(++serial);button.setAttribute('form',form.id)}
  const marker=document.createComment('save action '+(button.id||++serial));button.before(marker);entries.push({button,marker});
 }
 function visible(owner){
  for(let e=owner;e;e=e.parentElement){if(e.hidden)return false;if(e.tagName==='DETAILS'&&!e.open)return false}
  return true;
 }
 function dialogHost(dialog){
  const header=dialog.querySelector(':scope>header');if(!header)return null;
  let actions=header.querySelector('.dialog-save-actions');if(!actions){actions=document.createElement('div');actions.className='dialog-save-actions';header.insertBefore(actions,header.querySelector('.dialog-close,#flow-inspector-close'))}return actions;
 }
 function sync(){
  frame=0;const main=[];
  entries=entries.filter(({button,marker})=>button.isConnected||marker.isConnected);
  for(const entry of [...entries.filter(x=>x.button.id!=='save-floating'),...entries.filter(x=>x.button.id==='save-floating')]){
   const {button,marker}=entry,owner=marker.parentElement,dialog=owner?.closest('dialog'),page=owner?.closest('.page');
   let target=null;
   if(owner&&visible(owner)){
    if(dialog?.open)target=dialogHost(dialog);
    else if(page?.classList.contains('active-page')&&!page.closest('dialog'))target=host;
    else if(button.id==='save-floating'&&!owner.closest('[hidden]')&&!main.some(b=>['save-config','save-permissions','save-rules','save-companion','save-proactive'].includes(b.id)))target=host;
   }
   if(target){if(button.parentElement!==target)target.append(button);if(target===host)main.push(button)}
   else if(marker.isConnected&&button.previousSibling!==marker)marker.after(button);
   else if(!marker.isConnected)button.remove();
  }
  const empty=!host.children.length;if(host.hidden!==empty)host.hidden=empty;
  for(const bar of document.querySelectorAll('.page-toolbar-card'))bar.classList.toggle('save-empty',!bar.querySelector('button,input,select,a'));
 }
 function schedule(){if(!frame)frame=requestAnimationFrame(sync)}
 function collect(root){if(root.nodeType!==1)return;if(root.matches('button'))register(root);for(const button of root.querySelectorAll('button'))register(button)}
 collect(document.body);
 // Observe new controls and disclosure visibility, with one update per frame.
 new MutationObserver(records=>{let relevant=false;for(const record of records){if(record.type==='attributes'){relevant=true;continue}for(const node of record.addedNodes){if(node.nodeType===1){collect(node);relevant=true}}if(record.removedNodes.length)relevant=true}if(relevant)schedule()}).observe(document.body,{childList:true,subtree:true,attributes:true,attributeFilter:['hidden','open']});
 document.addEventListener('toggle',schedule,true);
 window.addEventListener('wechat-admin:navigate',schedule);window.addEventListener('wechat-admin:logout',schedule);
 window.WechatSaveActions={sync:schedule};schedule();
})();
