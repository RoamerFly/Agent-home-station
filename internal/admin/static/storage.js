(() => {
  'use strict';
  const $=s=>document.querySelector(s), api=(...args)=>window.WechatAdmin.api(...args);
  const size=n=>{n=Number(n)||0;return n<1024?n+' B':n<1048576?(n/1024).toFixed(1)+' KiB':(n/1048576).toFixed(1)+' MiB'};
  const date=value=>{const d=new Date(value);return Number.isNaN(d.getTime())?'时间未知':d.getFullYear()+'-'+String(d.getMonth()+1).padStart(2,'0')+'-'+String(d.getDate()).padStart(2,'0')+'-'+d.toLocaleTimeString('zh-CN',{hour12:false})};
  const paragraph=(text,cls='hint')=>{const p=document.createElement('p');p.textContent=text;p.className=cls;return p};
  let generation=0,scanning=false,storageLoaded=false;
  async function storage(){
    if(scanning||!window.WechatAdmin.isAuthenticated())return;
    const stamp=generation;scanning=true;$('#storage-refresh').disabled=true;$('#storage-state').textContent='正在扫描…';$('#storage-error').textContent='';
    try{
      const data=await api('/api/storage');if(stamp!==generation)return;
      $('#storage-state').textContent='已统计 '+size(data.total_bytes);$('#storage-boundary').textContent=data.boundary;
      const areas=$('#storage-areas');areas.replaceChildren();for(const a of data.areas||[])areas.append(paragraph(a.name+'：'+size(a.bytes)+' · '+a.files+' 个文件'+(a.skipped?' · 跳过 '+a.skipped+' 个链接或特殊文件':'')));
      const limits=$('#storage-limits');limits.replaceChildren();for(const x of data.limits||[]){const row=document.createElement('div');row.className='storage-limit';row.append(paragraph(x.name+'：'+x.count+'/'+x.limit+(x.count>=x.limit*.8?' · 接近上限，请整理记录':'')));const meter=document.createElement('meter');meter.min=0;meter.max=x.limit;meter.value=x.count;meter.setAttribute('aria-label',x.name+'已用条数');row.append(meter);limits.append(row)}
      $('#storage-error').textContent=data.error||'';storageLoaded=true;
    }catch(e){if(stamp===generation){$('#storage-state').textContent='扫描未完成';$('#storage-error').textContent=e.message}}
    finally{scanning=false;$('#storage-refresh').disabled=false}
  }
  $('#storage-refresh').addEventListener('click',storage);
  $('#storage-panel').addEventListener('toggle',()=>{if($('#storage-panel').open&&!storageLoaded)storage()});


  function archivePanel(mount,kind){
    let scope='';const trigger=document.createElement('button');trigger.type='button';trigger.className='quiet';trigger.textContent=kind==='memory'?'历史归档':'历史日志归档';trigger.hidden=kind==='memory';trigger.setAttribute('aria-haspopup','dialog');mount.append(trigger);
    const base=()=>'/api/archives/'+kind;
    trigger.addEventListener('click',()=>{
      const selected=scope,stamp=generation;if(kind==='memory'&&!selected)return;
      const popup=window.WechatDialogs.open({title:kind==='memory'?'历史记忆归档':'历史日志归档',subtitle:'只读查看或下载，不重新注入模型、不重发消息。'});
      const status=paragraph('正在读取归档…'),list=document.createElement('div');popup.body.append(status,list);let cursor='',busy=false;
      const more=document.createElement('button');more.type='button';more.className='secondary';more.textContent='加载更多批次';popup.body.append(more);
      const query=()=>kind==='memory'?'scope='+encodeURIComponent(selected):'';
      async function batch(item){
        const detail=window.WechatDialogs.open({title:'归档 · '+date(item.created)}),state=paragraph('正在校验并读取…');detail.body.append(state);
        const link=document.createElement('a');link.className='secondary';link.textContent='下载 JSON 副本';link.href=base()+'/'+encodeURIComponent(item.id)+'?'+[query(),'download=1'].filter(Boolean).join('&');detail.body.append(link);
        try{const response=await api(base()+'/'+encodeURIComponent(item.id)+(query()?'?'+query():''));if(!detail.alive()||stamp!==generation)return;state.textContent='消息从新到旧排列。';const messages=kind==='memory'?response.data.messages:response.data;for(const message of [...(messages||[])].sort((a,b)=>Date.parse(b.time)-Date.parse(a.time))){if(kind==='memory')detail.body.append(window.WechatAdmin.renderMemoryMessage(message));else detail.body.append(paragraph(date(message.time)+' · '+(message.source||message.thread_id||'系统')+' · '+(message.content||message.event||'记录')+(message.reason?' · '+(window.WechatLogReason?.(message.reason)||message.reason):''),'archive-log-row'))}if(!messages?.length)state.textContent='此批次没有正文。'}catch(e){if(detail.alive()&&stamp===generation)state.textContent=e.message}
      }
      async function load(){
        if(busy)return;busy=true;more.disabled=true;
        try{const q=[query(),cursor?'after='+encodeURIComponent(cursor):''].filter(Boolean).join('&'),data=await api(base()+(q?'?'+q:''));if(!popup.alive()||stamp!==generation)return;for(const item of data.items||[]){const row=document.createElement('div');row.className='archive-batch';const title=document.createElement('span');title.textContent=date(item.created)+' · '+size(item.bytes);const read=document.createElement('button');read.type='button';read.className='secondary';read.textContent='查看';read.addEventListener('click',()=>batch(item));row.append(title,read);list.append(row)}cursor=data.next||'';more.hidden=!cursor;status.textContent=list.children.length?'已载入 '+list.children.length+' 个归档批次':'暂无归档。'}catch(e){if(popup.alive()&&stamp===generation)status.textContent=e.message}finally{busy=false;more.disabled=false}
      }
      more.addEventListener('click',load);load();
    });
    window.addEventListener('wechat-admin:logout',()=>{scope='';trigger.hidden=kind==='memory'});
    if(kind==='memory')window.addEventListener('wechat-admin:memory-selection',e=>{scope=e.detail.id;trigger.hidden=!scope});
  }
  archivePanel($('#memory-archive-mount'),'memory');archivePanel($('#logs'),'activity');
  window.addEventListener('wechat-admin:logout',()=>{generation++;storageLoaded=false;$('#storage-panel').open=false;$('#storage-areas').replaceChildren();$('#storage-limits').replaceChildren();$('#storage-state').textContent='展开后扫描管理端数据。';$('#storage-error').textContent='';$('#storage-boundary').textContent=''});
})();
