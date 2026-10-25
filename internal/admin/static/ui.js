(() => {
  'use strict';
  const side=document.querySelector('.sidebar'),menu=document.querySelector('#menu-toggle'),scrim=document.querySelector('#drawer-scrim'),main=document.querySelector('.content');
  const mobile=matchMedia('(max-width:850px)');let wasOpen=false;
  const focusables=()=>[...side.querySelectorAll('button,a,input,select')].filter(el=>!el.disabled&&el.getClientRects().length);
  function syncDrawer(){const open=mobile.matches&&side.classList.contains('open');scrim.hidden=!open;main.inert=open;side.inert=mobile.matches&&!open;document.body.classList.toggle('nav-open',open);menu.setAttribute('aria-expanded',String(open));if(open&&!wasOpen)focusables()[0]?.focus();if(!open&&wasOpen&&mobile.matches)menu.focus();wasOpen=open}
  new MutationObserver(syncDrawer).observe(side,{attributes:true,attributeFilter:['class']});mobile.addEventListener('change',syncDrawer);syncDrawer();
  scrim.addEventListener('click',()=>side.classList.remove('open'));
  document.addEventListener('keydown',event=>{if(!wasOpen)return;if(event.key==='Escape'){side.classList.remove('open');return}if(event.key==='Tab'){const elements=focusables(),first=elements[0],last=elements.at(-1);if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus()}}});
  window.addEventListener('wechat-admin:logout',()=>{side.classList.remove('open');syncDrawer()});
  // Preserve native forms while making every field understandable to keyboards and password managers.
  document.querySelectorAll('input,select,textarea').forEach(field=>{if(field.id&&!field.name)field.name=field.id;if(!field.hasAttribute('autocomplete')&&field.tagName==='INPUT')field.autocomplete='off';field.addEventListener('input',()=>{if(!field.hasAttribute('aria-invalid'))return;field.removeAttribute('aria-invalid');document.getElementById(field.id+'-error')?.remove();const descriptions=(field.getAttribute('aria-describedby')||'').split(' ').filter(id=>id!==field.id+'-error');if(descriptions.length)field.setAttribute('aria-describedby',descriptions.join(' '));else field.removeAttribute('aria-describedby')});if(field.type==='number')field.inputMode='numeric';if(field.id.includes('key')||field.id.includes('url')||field.id.includes('id'))field.spellcheck=false;const hint=field.parentElement?.querySelector('.hint');if(hint&&!field.hasAttribute('aria-describedby')){if(!hint.id)hint.id=field.id+'-hint';field.setAttribute('aria-describedby',hint.id)}});
  document.querySelector('.skip-link').addEventListener('click',event=>{event.preventDefault();main.focus()});
  document.querySelector('#login-password').setAttribute('aria-describedby','login-error');
})();
