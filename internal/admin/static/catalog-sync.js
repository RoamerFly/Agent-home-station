(()=>{'use strict';
 const api=(...args)=>window.WechatAdmin.api(...args);let timer=null,busy=false,generation=0;
 const buttons=()=>document.querySelectorAll('[data-catalog-sync]');
 function render(job){busy=job.state==='running';buttons().forEach(b=>b.disabled=busy);document.querySelectorAll('[data-catalog-sync-status]').forEach(node=>{node.textContent=job.message+(job.records?' · 更新记录 '+job.records+' 条':'')+(job.hydrated_groups?' · 补充新群 '+job.hydrated_groups+' 个':'');node.dataset.state=job.state})}
 function stop(){if(timer)clearTimeout(timer);timer=null}
 function schedule(){stop();timer=setTimeout(poll,1200)}
 async function poll(){const token=generation;try{const job=await api('/api/catalog/sync');if(token!==generation)return;render(job);if(busy)schedule();else if(job.state==='succeeded')window.dispatchEvent(new CustomEvent('wechat-admin:catalog-synced',{detail:job}))}catch(e){if(token!==generation)return;busy=false;buttons().forEach(b=>b.disabled=false);document.querySelectorAll('[data-catalog-sync-status]').forEach(n=>n.textContent=e.message+'；可先查询目录，不会自动重新发起同步。')}}
 async function start(kind){if(busy)return;generation++;busy=true;buttons().forEach(b=>b.disabled=true);const token=generation;try{const body={kind};if(kind==='members'){body.thread=document.querySelector('#catalog-thread').value;if(!body.thread)throw new Error('请先选择群聊')}const job=await api('/api/catalog/sync',{method:'POST',json:body});if(token!==generation)return;render(job);if(busy)schedule()}catch(e){if(token!==generation)return;busy=false;buttons().forEach(b=>b.disabled=false);document.querySelectorAll('[data-catalog-sync-status]').forEach(n=>n.textContent=e.message)}}
 window.WechatCatalogSync={isBusy:()=>busy};
 buttons().forEach(b=>b.addEventListener('click',()=>start(b.dataset.catalogSync)));
 window.addEventListener('wechat-admin:navigate',e=>{if(['rules','catalog'].includes(e.detail?.section))poll()});
 window.addEventListener('wechat-admin:logout',()=>{generation++;stop();busy=false;buttons().forEach(b=>b.disabled=false);document.querySelectorAll('[data-catalog-sync-status]').forEach(n=>n.textContent='')});
})();
