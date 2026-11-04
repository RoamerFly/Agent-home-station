(() => {
  'use strict';
  const $ = (s) => document.querySelector(s);
  const state = { loaded:false, busy:false, kind:'text', config:null, file:null, previewUrl:null, upload:null, key:'', send:null, uncertain:false, libraryLoaded:false };

  const set = (node, value) => { if (node) node.textContent = value; };
  const prettyKind = (kind) => ({text:'文字',image:'图片',file:'文件',video:'视频',voice:'语音'}[kind] || kind);
  const formatBytes = (n) => { const value=Number(n||0); return value<1024?value+' B':value<1048576?(value/1024).toFixed(1)+' KB':(value/1048576).toFixed(1)+' MB'; };
  const bridge = () => window.WechatAdmin;
  const api = (...args) => bridge().api(...args);
  const notify = (message,error) => bridge().notice(message,error);
  function lock(on){ state.busy=on; ['#tools-upload','#tools-send','#tools-lookup','#tools-file','#tools-recipient'].forEach(id=>{const el=$(id);if(el)el.disabled=on}); document.querySelectorAll('#tools-kind button').forEach(b=>b.disabled=on); }
  function key(){ return crypto.randomUUID ? crypto.randomUUID() : 'send-'+Date.now()+'-'+Math.random().toString(36).slice(2); }
  function limit(){ return Number(state.config?.limits?.[state.kind]||0); }
  function isMedia(){ return state.kind!=='text'; }
  function revoke(){ if(state.previewUrl){URL.revokeObjectURL(state.previewUrl);state.previewUrl=null;} }
  function resetUpload(){ revoke(); state.file=null; state.upload=null; $('#tools-file').value=''; $('#tools-preview').replaceChildren(); $('#tools-preview').hidden=true; set($('#tools-upload-status'),''); render(); }
  function terminal(){ return ['accepted','succeeded','failed','cancelled'].includes(state.send?.state); }
  function newSend(){ if(state.busy||!terminal())return; state.quote=null;state.key='';state.send=null;state.sendStarted=false;state.uncertain=false;$('#tools-text').value='';resetUpload();$('#tools-result').replaceChildren();set($('#tools-progress'),'已新建发送草稿，请重新核对内容。');render(); }
  function setKind(kind){ if(!['text','image','file','video','voice'].includes(kind)||state.busy)return; state.kind=kind; if(kind!=='text')state.quote=null;resetUpload(); document.querySelectorAll('#tools-kind button').forEach(b=>{const selected=b.dataset.kind===kind;b.setAttribute('aria-checked',String(selected));}); const file=$('#tools-file'); file.accept=kind==='image'?'image/*':kind==='video'?'video/*':kind==='voice'?'audio/*':''; render(); }
  function renderRecipients(){
    const select=$('#tools-recipient');const previous=state.send?.thread_id||state.quote?.thread_id||select.value; select.replaceChildren();
    const recipients=state.config?.recipients||[];
    if(!recipients.length){const o=new Option('没有可用的已授权会话','');select.add(o);select.disabled=true;return;}
    recipients.forEach(r=>select.add(new Option(r.name||r.id,r.id)));
    if(recipients.some(r=>r.id===previous))select.value=previous;select.disabled=state.busy;
  }
  function renderFile(){
    const area=$('#tools-upload-area'),text=$('#tools-text'),note=$('#tools-file-note'),upload=$('#tools-upload');
    area.hidden=!isMedia(); text.disabled=isMedia(); const textLabel=document.querySelector('label[for="tools-text"]'); if(textLabel)textLabel.hidden=isMedia();
    if(!isMedia()) return;
    const lim=limit(); set(note,lim?'当前类型上限：'+formatBytes(lim)+'。':'服务器未声明当前类型大小限制。');
    upload.disabled=state.busy||!state.file||Boolean(state.upload);
  }
  function renderReview(){
    const list=$('#tools-review-list'); list.replaceChildren();
    const recipient=state.config?.recipients?.find(r=>r.id===$('#tools-recipient').value);
    const pairs=[['类型',prettyKind(state.kind)],['接收方',recipient?.name||'请选择已授权会话']];
    if(state.quote)pairs.push(['引用消息',state.quote.body||state.quote.message_id]);if(state.kind==='text')pairs.push(['内容',$('#tools-text').value.trim()||'尚未填写']);
    else pairs.push(['附件',state.upload?state.upload.filename+' · '+formatBytes(state.upload.byte_length):'尚未上传']);
    pairs.forEach(([term,value])=>{const dt=document.createElement('dt');dt.textContent=term;const dd=document.createElement('dd');dd.textContent=value;list.append(dt,dd);});
    const valid=Boolean(recipient?.id)&&(state.kind==='text'?Boolean($('#tools-text').value.trim()):Boolean(state.upload))&&!state.busy&&!state.sendStarted;
    $('#tools-send').disabled=!valid;
    $('#tools-lookup').disabled=state.busy||!state.key||terminal();
    $('#tools-new').disabled=state.busy||!terminal();
  }
  function render(){ renderFile();renderReview(); const frozen=state.busy||Boolean(state.sendStarted);$('#tools-file').disabled=frozen;$('#tools-recipient').disabled=frozen||!state.config?.recipients?.length;$('#tools-text').disabled=frozen||isMedia();document.querySelectorAll('#tools-kind button').forEach(b=>b.disabled=frozen); }
  function showFile(){
    const box=$('#tools-preview');box.replaceChildren();
    if(!state.file){box.hidden=true;return;}
    const title=document.createElement('p');title.textContent=state.file.name+' · '+formatBytes(state.file.size);box.append(title);
    if(state.kind==='image'){
      revoke();state.previewUrl=URL.createObjectURL(state.file);
      const image=document.createElement('img');image.src=state.previewUrl;image.alt='待上传图片预览';box.append(image);
    }
    box.hidden=false; render();
  }
  function result(value){
    state.send=value||state.send;
    const box=$('#tools-result'); box.replaceChildren();
    if(!value){return;}
    const stateLabel={accepted:'微信已接受',succeeded:'已完成',queued:'排队中',running:'执行中',failed:'失败',cancelled:'已取消',needs_review:'送达情况待核实',unknown:'送达情况待核实'}[value.state]||value.state||value.phase||'已提交';
    const strong=document.createElement('strong');strong.textContent='状态：'+stateLabel;box.append(strong);
    const phase={pacing:'等待录音时长，后台仍在接收消息',preparing:'准备附件',submitting:'提交微信'}[value.phase]||value.phase;
    [['阶段',phase],['发送编号',value.id],['服务端编号',value.server_id],['说明',value.reason]].forEach(([name,val])=>{if(val){const p=document.createElement('p');p.textContent=name+'：'+val;box.append(p);}});
    const stages={voice_handshake_before_submission:'建立连接失败，当前上传块尚未提交',voice_upload_exchange_outcome_unknown:'上传连接中断，当前段送达情况待核实',voice_upload_receipt_unverified:'未能验证当前段的微信回执'};
    if(stages[value.voice_failure_stage]){const p=document.createElement('p');p.textContent=stages[value.voice_failure_stage];box.append(p);}
    const network={voice_transport_timeout:'等待连接或响应超时',voice_transport_connect:'无法建立上传连接',voice_transport_body:'读取上传响应失败',voice_transport_closed:'上传连接被关闭',voice_transport_io:'网络读写失败',voice_transport_unclassified:'上传网络异常'}[value.voice_transport_kind];
    if(network){const p=document.createElement('p');p.textContent=network+(Number.isInteger(value.voice_failed_offset)?' · 上传位置 '+value.voice_failed_offset+' 字节':'');box.append(p);}
    (value.voice_chunks||[]).forEach(chunk=>{const p=document.createElement('p');const label={accepted:chunk.has_receipt?'微信已接受':'回执待核实',prepared:'等待发送',submitting:'正在提交',rejected:'微信拒绝',needs_review:'送达情况待核实'}[chunk.state]||chunk.state;p.textContent='第 '+(chunk.index+1)+' 段 · '+(chunk.duration_ms/1000).toFixed(1)+' 秒 · '+label+(chunk.pacing_ms?' · 发送间隔 '+(chunk.pacing_ms/1000).toFixed(1)+' 秒':'')+(chunk.prefix_retries?' · 未完成数据块重试 '+chunk.prefix_retries+' 次':'');box.append(p);});
    set($('#tools-progress'),['needs_review','unknown'].includes(value.state)?'本次发送已停止，请按段核对实际收件情况；不会自动重发。':terminal()?'本次操作已得到最终状态。':'正在等待服务器确认，可稍后查询状态。');
  }
  function renderRecent(items){
    const box=$('#tools-recent-list');box.replaceChildren();
    if(!items?.length){const p=document.createElement('p');p.className='empty';p.textContent='暂时没有可恢复查询的发送记录。';box.append(p);return;}
    items.slice(0,20).forEach(item=>{const status=item.status||{};const row=document.createElement('div');row.className='library-row';const detail=document.createElement('div');const title=document.createElement('strong');title.textContent=prettyKind(status.kind||'text')+' · '+(status.state||'等待查询');const meta=document.createElement('p');meta.textContent=(item.created||'')+(status.thread_id?' · '+window.WechatNames.display(status.thread_id):'');detail.append(title,meta);const recover=document.createElement('button');recover.type='button';recover.className='secondary';recover.textContent='恢复查询';recover.disabled=!item.key;recover.addEventListener('click',()=>{state.key=item.key;state.send=status;state.sendStarted=true;state.uncertain=false;result(status);render();lookup();});row.append(detail,recover);box.append(row);});
  }
  function renderLibrary(items){
    const box=$('#library-list');box.replaceChildren();set($('#library-state'),'已上传图片可由角色自主选用；可在此维护名称和语义描述。');
    if(!items?.length){const p=document.createElement('p');p.className='empty';p.textContent='暂时没有已上传的图片素材。上传图片后可在这里编辑说明。';box.append(p);return;}
    items.forEach(item=>{const row=document.createElement('div');row.className='library-row';const form=document.createElement('div');const titleLabel=document.createElement('label');titleLabel.textContent='名称';const title=document.createElement('input');title.maxLength=120;title.value=item.title||'';title.setAttribute('aria-label','图片名称');titleLabel.append(title);const descriptionLabel=document.createElement('label');descriptionLabel.textContent='语义描述';const description=document.createElement('textarea');description.rows=2;description.maxLength=1000;description.value=item.description||'';description.placeholder='例如：蓝色鲸鱼娘在海边挥手，适合轻松问候';descriptionLabel.append(description);form.append(titleLabel,descriptionLabel);const save=document.createElement('button');save.type='button';save.className='secondary';save.textContent='保存素材说明';save.addEventListener('click',async()=>{save.disabled=true;try{await api('/api/library/'+encodeURIComponent(item.id),{method:'POST',json:{title:title.value.trim(),description:description.value.trim(),enabled:true}});set($('#library-state'),'素材说明已保存，AI 图片目录已更新。');}catch(err){notify(err.message||'保存素材说明失败。',true);}finally{save.disabled=false;}});row.append(form,save);box.append(row);});
  }
  async function loadLibrary(refresh=false){
    if((state.libraryLoaded&&!refresh)||state.busy||!bridge()?.isAuthenticated())return;
    const button=$('#library-refresh');button.disabled=true;set($('#library-state'),'正在读取已上传图片素材…');
    try{const library=await api('/api/library');state.libraryLoaded=true;renderLibrary(library.items||[]);}
    catch(err){set($('#library-state'),err.message||'无法读取图片素材库。');}
    finally{button.disabled=false;}
  }
  async function load(refresh=false){
    if((state.loaded&&!refresh)||state.busy||!bridge()?.isAuthenticated())return;
    lock(true);set($('#tools-progress'),'正在读取已授权会话与服务能力…');
    try{
      state.config=await api('/api/tools/state');state.loaded=true;
      const kinds=state.config?.capabilities?.mediaKinds||[];
      document.querySelectorAll('#tools-kind [data-kind]').forEach(button=>{const kind=button.dataset.kind;button.hidden=kind!=='text'&&!kinds.includes(kind);});
      renderRecipients();renderRecent(state.config.recent_sends||[]);$('#tools-workspace').hidden=false;$('#tools-unavailable').hidden=true;render();set($('#tools-progress'),'选择内容后，上传与发送都需要你手动确认。');
    }catch(err){
      const box=$('#tools-unavailable');box.hidden=false;box.textContent=err.message||'无法读取手动发送功能。';set($('#tools-progress'),'功能尚不可用。');
    }finally{lock(false);render();if(state.loaded)loadLibrary(true);}
  }
  async function upload(){
    if(!state.file||state.busy||state.upload)return;
    const cap=limit();if(cap&&state.file.size>cap){notify('附件超过当前类型大小限制。',true);return;}
    const form=new FormData();form.append('file',state.file);form.append('kind',state.kind);
    lock(true);set($('#tools-upload-status'),'正在上传附件；上传完成后仍不会发送。');
    try{state.upload=await api('/api/tools/upload',{method:'POST',formData:form});set($('#tools-upload-status'),'已上传：'+state.upload.filename+'。请在右侧核对后手动发送。');set($('#tools-progress'),'附件已上传，尚未发送。');}
    catch(err){set($('#tools-upload-status'),'');notify(err.message||'附件上传失败。',true);}
    finally{lock(false);render();if(state.kind==='image')loadLibrary(true);}
  }
  async function send(){
    if(state.busy||state.sendStarted)return;
    const recipient=$('#tools-recipient').value;if(!recipient)return notify('请选择已授权接收方。',true);
    const text=$('#tools-text').value.trim();if(new TextEncoder().encode(text).length>4096)return notify('文字内容不能超过 4096 字节。',true);if(state.kind==='text'&&!text)return notify('请输入文字内容。',true);
    if(isMedia()&&!state.upload)return notify('请先上传附件。',true);
    state.key=state.key||key();state.sendStarted=true;lock(true);$('#tools-send').disabled=true;set($('#tools-progress'),'正在提交一次微信发送请求，请勿重复点击。');
    try{
      const value=await api('/api/tools/send',{method:'POST',json:{thread_id:recipient,kind:state.kind,text:state.kind==='text'?text:'',source_id:state.upload?.id||'',idempotency_key:state.key,...(state.quote?{quote:{message_id:state.quote.message_id,server_id:state.quote.server_id}}:{})}});
      result(value);notify('发送请求已提交；请查看进度。');
    }catch(err){
      if([400,403,413,415,422].includes(err.status)){result({state:'failed',reason:err.message});notify(err.message,true);return;}state.uncertain=true;set($('#tools-progress'),'网络结果不明确。本次编号已保留，请查询发送状态，不会自动重发。');$('#tools-lookup').disabled=false;notify(err.message||'发送结果不明确，请查询状态。',true);
    }finally{lock(false);render();if(state.uncertain)$('#tools-lookup').disabled=false;}
  }
  async function lookup(){
    if(!state.key||state.busy)return;
    lock(true);set($('#tools-progress'),'正在查询本次操作状态…');
    try{const value=state.send?.id?await api('/api/tools/sends/'+encodeURIComponent(state.send.id)):await api('/api/tools/lookup?key='+encodeURIComponent(state.key));result(value);}
    catch(err){notify(err.message||'状态查询失败。',true);}
    finally{lock(false);render();}
  }
  function bind(){
    document.querySelectorAll('#tools-kind button').forEach(b=>b.addEventListener('click',()=>setKind(b.dataset.kind)));
    $('#tools-file').addEventListener('change',e=>{const selected=e.target.files?.[0]||null;resetUpload();state.file=selected;showFile();});
    $('#tools-text').addEventListener('input',renderReview);$('#tools-recipient').addEventListener('change',()=>{state.quote=null;renderReview()});
    $('#tools-upload').addEventListener('click',upload);$('#tools-send').addEventListener('click',send);$('#tools-lookup').addEventListener('click',lookup);$('#tools-new').addEventListener('click',newSend);window.WechatRefresh.bind($('#library-refresh'),()=>loadLibrary(true));
    window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='tools')load(true);});
    window.addEventListener('wechat-admin:config-changed',()=>{if($('#tools').classList.contains('active-page'))load(true);});
    window.addEventListener('wechat-admin:quote',async e=>{if(state.busy||state.sendStarted){notify('请先完成当前发送，再引用其他消息。',true);return;}await load(true);const q=e.detail||{};if(!state.config?.recipients?.some(r=>r.id===q.thread_id)){notify('引用目标不在已授权会话中。',true);return;}setKind('text');state.quote=q;$('#tools-recipient').value=q.thread_id;render();notify('已选择引用消息；填写回复后核对并手动发送。');});window.addEventListener('wechat-admin:logout',()=>{revoke();state.loaded=false;state.libraryLoaded=false;state.file=null;state.upload=null;state.quote=null;state.send=null;state.sendStarted=false;state.key='';$('#tools-result').replaceChildren();$('#library-list').replaceChildren();$('#tools-recent-list').replaceChildren();$('#tools-text').value='';});window.addEventListener('beforeunload',revoke);
  }
  bind();
  window.addEventListener('wechat-admin:recipient',async event=>{
    if(state.busy||state.sendStarted){notify('当前有发送任务，请完成后再切换接收方。',true);return;}
    await load(true);const target=event.detail;
    if(!state.config?.recipients?.some(r=>r.id===target.thread)){notify('此会话尚未授权手动发送。',true);return;}
    $('#tools-recipient').value=target.thread;state.quote=null;renderReview();
    const label=document.getElementById('send-context-label');if(label)label.textContent='发送至 '+target.label;
  });
})();
