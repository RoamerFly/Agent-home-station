(() => {
 'use strict';
 let serial=0;
 function open({title,subtitle='',content,onClose}={}){
  const opener=document.activeElement,openerScope=opener?.closest('[data-scope-id]')?.dataset.scopeId,openerID=opener?.id,dialog=document.createElement('dialog');dialog.className='console-dialog';
  const heading=document.createElement('header');heading.className='dialog-heading';
  const titles=document.createElement('div'),h=document.createElement('h2');h.id='dialog-title-'+(++serial);h.textContent=title;dialog.setAttribute('aria-labelledby',h.id);titles.append(h);
  if(subtitle){const p=document.createElement('p');p.textContent=subtitle;titles.append(p)}
  const closeButton=document.createElement('button');closeButton.type='button';closeButton.className='quiet dialog-close';closeButton.textContent='关闭';closeButton.setAttribute('aria-label','关闭'+title);closeButton.addEventListener('click',()=>dialog.close());
  heading.append(titles,closeButton);const body=document.createElement('div');body.className='dialog-body';if(content)body.append(content);
  dialog.append(heading,body);document.body.append(dialog);
  dialog.addEventListener('close',()=>{
   onClose?.();dialog.remove();document.documentElement.classList.toggle('has-dialog',!!document.querySelector('dialog[open]'));
   let target=opener?.isConnected?opener:null;
   if(!target&&openerID)target=document.getElementById(openerID);
   if(!target&&openerScope)target=[...document.querySelectorAll('.conversation-row')].find(row=>row.dataset.scopeId===openerScope)?.querySelector('.conversation-open');
   if(target?.getClientRects().length)target.focus({preventScroll:true});
  },{once:true});
  dialog.addEventListener('click',e=>{if(e.target!==dialog)return;const r=dialog.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)dialog.close()});
  dialog.showModal();document.documentElement.classList.add('has-dialog');
  return {dialog,body,close:()=>dialog.open&&dialog.close(),alive:()=>dialog.isConnected&&dialog.open};
 }
 function closeAll(){for(const dialog of [...document.querySelectorAll('dialog[open]')].reverse())dialog.close()}
 window.WechatDialogs={open,closeAll};
 window.addEventListener('wechat-admin:logout',closeAll);
 window.addEventListener('wechat-admin:navigate',e=>{if(!e.detail?.embedded)closeAll()});
})();
