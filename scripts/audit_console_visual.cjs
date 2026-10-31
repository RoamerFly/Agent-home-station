/* Synthetic visual audit only. Does not click save/send/start/test actions. */
'use strict';
const fs=require('node:fs/promises'),path=require('node:path');
module.exports=async function audit(page,pages,output){
 await fs.mkdir(output,{recursive:true});const report=[];
 for(const viewport of [{width:1440,height:900},{width:375,height:812}])for(const theme of ['light','dark']){
  await page.setViewportSize(viewport);await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
  for(const id of pages){
   await page.evaluate(s=>window.WechatAdmin.nav(s),id);await page.waitForTimeout(80);
   if(id==='workbench'){await page.locator('#flow-fit').click();await page.waitForTimeout(50)}
   for(const expanded of [false,true]){
    const layout=await page.evaluate(expanded=>{
     const root=document.querySelector('.content>.active-page');
     for(const detail of root.querySelectorAll('details'))detail.open=expanded;
     const visible=e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0&&getComputedStyle(e).visibility!=='hidden'};
     const scrolls=[root,...root.querySelectorAll('*')].filter(e=>visible(e)&&e.clientHeight>70&&e.scrollHeight>e.clientHeight+4&&/auto|scroll/.test(getComputedStyle(e).overflowY));
     scrolls.forEach((e,i)=>{e.dataset.auditScroll=String(i);e.scrollTop=0});
     const unowned=[...root.querySelectorAll('h2,h3,p,summary')].filter(e=>visible(e)&&e.textContent.trim()&&!e.classList.contains('sr-only')&&!e.closest('.panel,.card,.metric,.voice-card,.account-row,.catalog-card,.rule-row,.dashboard-row,.round-card,.round-workspace,.flow-workspace,.flow-toolbar,.document-editor,.workspace-pane,.workspace-header,.page-toolbar-card,.page-surface,.round-metrics,.round-filters,#provider')).map(e=>({tag:e.tagName,text:e.textContent.trim().slice(0,90)}));
     const steps=Math.max(1,...scrolls.map(e=>Math.ceil((e.scrollHeight-e.clientHeight)/Math.max(1,e.clientHeight*.8))+1));
     return {unowned,steps,horizontal:document.documentElement.scrollWidth>innerWidth+2||root.scrollWidth>root.clientWidth+2};
    },expanded);
    const steps=layout.steps,images=[];
    for(let step=0;step<steps;step++){
     await page.evaluate(ratio=>{for(const e of document.querySelectorAll('[data-audit-scroll]'))e.scrollTop=ratio*(e.scrollHeight-e.clientHeight)},steps===1?0:step/(steps-1));await page.waitForTimeout(35);
     const name=`${id}-${theme}-${viewport.width}-${expanded?'open':'closed'}-${step}.png`;await page.screenshot({path:path.join(output,name)});images.push(name);
    }
    await page.evaluate(()=>{for(const e of document.querySelectorAll('[data-audit-scroll]')){e.scrollTop=0;delete e.dataset.auditScroll}});
    report.push({page:id,theme,width:viewport.width,expanded,...layout,complete:images.length===layout.steps,images});
   }
  }
 }
 await fs.writeFile(path.join(output,'audit.json'),JSON.stringify(report,null,2));
 console.log(JSON.stringify({visualAudit:true,pages:pages.length,states:report.length,screenshots:report.reduce((n,r)=>n+r.images.length,0),horizontalFailures:report.filter(r=>r.horizontal).length,incomplete:report.filter(r=>!r.complete).length,unowned:report.filter(r=>r.unowned.length).map(r=>({page:r.page,width:r.width,expanded:r.expanded,items:r.unowned}))}));
};
