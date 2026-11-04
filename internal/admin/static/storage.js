(() => {
  'use strict';
  const $=s=>document.querySelector(s), api=(...args)=>window.WechatAdmin.api(...args);
  const size=n=>{n=Number(n)||0;return n<1024?n+' B':n<1048576?(n/1024).toFixed(1)+' KiB':(n/1048576).toFixed(1)+' MiB'};
  const date=value=>{const d=new Date(value);return Number.isNaN(d.getTime())?'时间未知':d.getFullYear()+'-'+String(d.getMonth()+1).padStart(2,'0')+'-'+String(d.getDate()).padStart(2,'0')+'-'+d.toLocaleTimeString('zh-CN',{hour12:false})};
  const paragraph=(text,cls='hint')=>{const p=document.createElement('p');p.textContent=text;p.className=cls;return p};
  let generation=0,pending=null,storageLoaded=false;
  function action(label,run){
    const b=document.createElement('button');b.type='button';b.className='quiet';b.textContent=label;
    b.addEventListener('click',async()=>{b.disabled=true;try{await run()}catch(e){$('#storage-error').textContent=e.message}finally{b.disabled=false}});return b;
  }
  function renderArea(area,data){
    const row=document.createElement('article');row.className='storage-area';row.dataset.area=area.id;
    const name=document.createElement('strong');name.textContent=area.name;row.append(name,paragraph(size(area.bytes)+' · '+area.files+' 个文件'));
    if(area.incomplete)row.append(paragraph('未扫描完整，可展开存储位置查看。'));
    if(area.skipped)row.append(paragraph('已跳过 '+area.skipped+' 个链接或特殊文件'));
    const locations=area.locations||[];
    if(locations.length){
      const details=document.createElement('details'),label=document.createElement('summary');label.textContent='存储位置';details.append(label);
      locations.forEach((path,index)=>{
        const line=document.createElement('div'),code=document.createElement('code');line.className='storage-paths';code.textContent=path;line.append(code);
        if(data.can_open_folder)line.append(action('打开文件夹',()=>api('/api/storage/open',{method:'POST',json:{area:area.id,index}})));
        line.append(action('复制路径',async()=>{await navigator.clipboard.writeText(path);$('#storage-state').textContent='路径已复制'}));details.append(line);
      });
      row.append(details);
    }
    if(area.id==='logs'){
      const used=(data.limits||[]).find(x=>x.name==='活跃日志')?.count||0;
      if(used>200)row.append(action('归档较早的 '+(used-200)+' 条日志',async()=>{
        const result=await api('/api/storage/archive-logs',{method:'POST',json:{}});
        await storage();$('#storage-state').textContent='已归档 '+result.archived+' 条，最近 200 条保留在活跃日志中。';
      }));
    }
    return row;
  }
  function storage(){
    if(!pending)pending=scan().finally(()=>{pending=null});
    return pending;
  }
  async function scan(){
    if(!window.WechatAdmin.isAuthenticated())return;
    const stamp=generation;$('#storage-refresh').disabled=true;$('#storage-state').textContent='正在扫描…';$('#storage-error').textContent='';
    try{
      const data=await api('/api/storage');if(stamp!==generation)return;
      $('#storage-state').textContent=(data.partial?'部分统计 ':'已统计 ')+size(data.total_bytes);$('#storage-boundary').textContent=data.boundary;
      const areas=$('#storage-areas');
      const expanded=new Set(Array.from(areas.querySelectorAll('details[open]'),item=>item.parentElement.dataset.area));
      areas.replaceChildren(...(data.areas||[]).map(area=>{
        const row=renderArea(area,data),details=row.querySelector('details');
        if(details&&expanded.has(area.id))details.open=true;
        return row;
      }));
      const limits=$('#storage-limits');limits.replaceChildren();for(const x of data.limits||[]){const row=document.createElement('div');row.className='storage-limit';row.append(paragraph(x.name+'：'+x.count+'/'+x.limit+(x.count>=x.limit*.8?' · 接近记录上限':'')));const meter=document.createElement('meter');meter.min=0;meter.max=x.limit;meter.value=x.count;meter.setAttribute('aria-label',x.name+'已用条数');row.append(meter);limits.append(row)}
      $('#storage-error').textContent=data.error||'';storageLoaded=true;
    }catch(e){if(stamp===generation){$('#storage-state').textContent='扫描未完成';$('#storage-error').textContent=e.message}}
    finally{$('#storage-refresh').disabled=false}
  }
  window.WechatRefresh.bind($('#storage-refresh'),storage);
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
        const link=document.createElement('a');link.className='secondary';link.textContent='下载 JSON 副本';link.href=window.WechatAccountContext.url(base()+'/'+encodeURIComponent(item.id)+'?'+[query(),'download=1'].filter(Boolean).join('&'));detail.body.append(link);
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
