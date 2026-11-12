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
 function text(parent,value,{title='完整内容',className='',limit=180}={}){
  const content=String(value??'');
  if(content.length<=limit){const span=document.createElement('span');span.className=className;span.textContent=content;parent.append(span);return span}
  const button=document.createElement('button');button.type='button';button.className='long-content '+className;
  button.setAttribute('aria-label','查看'+title);button.title='点击查看完整内容';
  const preview=document.createElement('span');preview.className='long-content-preview';preview.textContent=content;
  const cue=document.createElement('span');cue.className='long-content-cue';cue.textContent='查看完整内容';button.append(preview,cue);
  button.addEventListener('click',()=>{const popup=open({title});const body=document.createElement('pre');body.className='readable-full-text';body.textContent=content;popup.body.append(body)});
  parent.append(button);return button;
 }
 window.WechatDialogs={open,closeAll,text};
 window.addEventListener('wechat-admin:logout',closeAll);
 window.addEventListener('wechat-admin:navigate',e=>{if(!e.detail?.embedded)closeAll()});
})();
