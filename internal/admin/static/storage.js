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
    let scope='',cursor='',busy=false,loaded=false,revision=0;
    const root=document.createElement('details');root.className='panel history-archive';root.id=kind+'-archives';root.hidden=kind==='memory';
    const summary=document.createElement('summary');summary.textContent=kind==='memory'?'历史记忆归档':'历史日志归档';
    const hint=paragraph('旧记录先压缩归档，可查看或下载副本；不注入模型、不重发消息。归档是当时快照，后续状态以新记录为准。');
    const controls=document.createElement('div');controls.className='inline-actions';const refresh=document.createElement('button');refresh.type='button';refresh.className='secondary';refresh.textContent='刷新归档';const status=paragraph('展开后读取归档批次。');status.setAttribute('role','status');controls.append(refresh,status);
    const list=document.createElement('div'),more=document.createElement('button');more.type='button';more.className='secondary';more.textContent='加载更多批次';more.hidden=true;root.append(summary,hint,controls,list,more);mount.append(root);
    const base=()=>'/api/archives/'+kind;
    const query=()=>kind==='memory'?'scope='+encodeURIComponent(scope):'';
    async function load(reset){
      if(busy||!window.WechatAdmin.isAuthenticated()||kind==='memory'&&!scope)return;
      busy=true;refresh.disabled=true;more.disabled=true;const stamp=revision,global=generation;status.textContent='正在读取…';
      try{
        const q=[query(),!reset&&cursor?'after='+encodeURIComponent(cursor):''].filter(Boolean).join('&');const data=await api(base()+(q?'?'+q:''));if(stamp!==revision||global!==generation)return;
        if(reset)list.replaceChildren();for(const item of data.items||[]){
          const row=document.createElement('details'),title=document.createElement('summary');title.textContent=date(item.created)+' · '+size(item.bytes);row.append(title);
          const body=document.createElement('div');body.className='archive-preview';const link=document.createElement('a');link.className='secondary';link.textContent='下载 JSON 副本';link.href=base()+'/'+encodeURIComponent(item.id)+'?'+[query(),'download=1'].filter(Boolean).join('&');row.append(link,body);let ready=false,reading=false;
          row.addEventListener('toggle',async()=>{if(!row.open||ready||reading)return;reading=true;body.replaceChildren(paragraph('正在校验并读取…'));try{const response=await api(base()+'/'+encodeURIComponent(item.id)+(query()?'?'+query():''));if(stamp!==revision||global!==generation)return;body.replaceChildren();const messages=kind==='memory'?response.data.messages:response.data;for(const message of messages||[]){if(kind==='memory')body.append(window.WechatAdmin.renderMemoryMessage(message));else body.append(paragraph(date(message.time)+' · '+(message.source||message.thread_id||'系统')+' · '+(message.content||message.event||'记录')+(message.reason?' · '+(window.WechatLogReason?.(message.reason)||message.reason):''),'archive-log-row'))}if(!messages?.length)body.append(paragraph('此批次没有正文。'));ready=true}catch(e){if(stamp===revision&&global===generation)body.replaceChildren(paragraph(e.message,'form-error'))}finally{reading=false}});
          list.append(row);
        }
        cursor=data.next||'';more.hidden=!cursor;status.textContent=list.children.length?'已加载 '+list.children.length+' 个归档批次':'暂无归档。升级前已被旧策略移出的记录无法补回。';loaded=true;
      }catch(e){if(stamp===revision&&global===generation)status.textContent=e.message}
      finally{busy=false;refresh.disabled=false;more.disabled=false;if(stamp!==revision&&root.open)load(true)}
    }
    refresh.addEventListener('click',()=>load(true));more.addEventListener('click',()=>load(false));root.addEventListener('toggle',()=>{if(root.open&&!loaded)load(true)});
    const reset=()=>{revision++;scope='';cursor='';loaded=false;list.replaceChildren();root.open=false;root.hidden=kind==='memory';more.hidden=true;status.textContent='展开后读取归档批次。'};
    window.addEventListener('wechat-admin:logout',reset);
    if(kind==='memory')window.addEventListener('wechat-admin:memory-selection',e=>{if(scope===e.detail.id)return;reset();scope=e.detail.id;root.hidden=false});
  }
  archivePanel($('#memory-panel'),'memory');archivePanel($('#logs'),'activity');
  window.addEventListener('wechat-admin:logout',()=>{generation++;storageLoaded=false;$('#storage-panel').open=false;$('#storage-areas').replaceChildren();$('#storage-limits').replaceChildren();$('#storage-state').textContent='展开后扫描管理端数据。';$('#storage-error').textContent='';$('#storage-boundary').textContent=''});
})();
