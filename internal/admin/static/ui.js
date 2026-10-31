(() => {
  'use strict';
  const side=document.querySelector('.sidebar'),menu=document.querySelector('#menu-toggle'),scrim=document.querySelector('#drawer-scrim'),main=document.querySelector('.content');
  const mobile=matchMedia('(max-width:760px)');let wasOpen=false;
  const focusables=()=>[...side.querySelectorAll('button,a,input,select')].filter(el=>!el.disabled&&el.getClientRects().length);
  function syncDrawer(){const open=mobile.matches&&side.classList.contains('open');scrim.hidden=!open;main.inert=open;side.inert=mobile.matches&&!open;document.body.classList.toggle('nav-open',open);menu.setAttribute('aria-expanded',String(open));if(open&&!wasOpen)focusables()[0]?.focus();if(!open&&wasOpen&&mobile.matches)menu.focus();wasOpen=open}
  new MutationObserver(syncDrawer).observe(side,{attributes:true,attributeFilter:['class']});mobile.addEventListener('change',syncDrawer);syncDrawer();
  scrim.addEventListener('click',()=>side.classList.remove('open'));
  document.addEventListener('keydown',event=>{if(!wasOpen)return;if(event.key==='Escape'){side.classList.remove('open');return}if(event.key==='Tab'){const elements=focusables(),first=elements[0],last=elements.at(-1);if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus()}}});
  window.addEventListener('wechat-admin:logout',()=>{side.classList.remove('open');syncDrawer()});
  // One delegated reset; use native form validation rather than inserting
  // extra error rows and focus/scroll handlers for every individual input.
  document.addEventListener('input',event=>event.target.removeAttribute?.('aria-invalid'));
  document.querySelector('#login-password').setAttribute('aria-describedby','login-error');
  // A single moving selection. CSS transitions can reverse in flight; measure
  // only on navigation/resize, never on pointer movement or every animation frame.
  const nav=side.querySelector('nav'),position=document.createElement('span');
  position.className='nav-position';position.setAttribute('aria-hidden','true');nav.prepend(position);
  let frame=0;
  function placeSelection(){
    frame=0;const active=nav.querySelector('.main-nav.active');
    if(!active||!active.getClientRects().length){position.style.opacity='0';return}
    const y=active.offsetTop,height=active.offsetHeight;
    position.style.height=height+'px';position.style.transform=`translateY(${y}px)`;position.style.opacity='1';nav.classList.add('has-position');
  }
  function scheduleSelection(){if(!frame)frame=requestAnimationFrame(placeSelection)}
  window.addEventListener('wechat-admin:navigate',scheduleSelection);
  new ResizeObserver(scheduleSelection).observe(nav);
  window.addEventListener('wechat-admin:logout',()=>{if(frame)cancelAnimationFrame(frame);frame=0;position.style.opacity='0'});
  scheduleSelection();
})();
