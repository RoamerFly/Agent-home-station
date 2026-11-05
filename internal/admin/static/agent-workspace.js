(() => {
 'use strict';
 const $=s=>document.querySelector(s),api=(...a)=>window.WechatAdmin.api(...a);
 const state={revision:'',loadedName:'',scope:'shared',busy:false,dirty:false,epoch:0,processSignature:'',loaded:false};
 const labels={running:'运行中',completed:'已完成',failed:'失败',cancelled:'已取消',timed_out:'执行超时',interrupted:'重启后待检查',needs_review:'需要核对'};
 function status(text){$('#agent-doc-state').textContent=text}
 function discardDraft(){return !state.dirty||confirm('文档有未保存的修改，确定放弃？')}
 window.addEventListener('beforeunload',event=>{if(state.dirty){event.preventDefault();event.returnValue=''}});
 function clear(){state.revision='';state.loadedName='';state.dirty=false;$('#agent-doc-name').value='';$('#agent-doc-content').value='';status('')}
 function clearAccountWorkspace(){clear();state.layout=null;$('#managed-workspace-status')?.remove()}
 function processes(items){
  const signature=JSON.stringify(items);if(signature===state.processSignature)return;state.processSignature=signature;
  const box=$('#agent-process-list');if(!items.length){box.textContent='暂无后台进程';return}
  if(!box.querySelector('details'))box.replaceChildren();
  const rows=new Map([...box.querySelectorAll(':scope > details')].map(el=>[el.dataset.processId,el]));
  for(const item of items.slice().sort((a,b)=>b.started.localeCompare(a.started))){
   let row=rows.get(item.id);if(!row){row=document.createElement('details');row.dataset.processId=item.id;row.append(document.createElement('summary'),document.createElement('pre'));box.append(row)}
   row.querySelector('summary').textContent=(labels[item.state]||item.state)+' · '+item.id;
   const pre=row.querySelector('pre'),top=pre.scrollTop,bottom=pre.scrollHeight-pre.clientHeight-top<5,text=item.output||'尚无输出';
   if(pre.textContent!==text){pre.textContent=text;pre.scrollTop=bottom?pre.scrollHeight:top}
   let stop=row.querySelector('button');if(item.state==='running'&&!stop){stop=document.createElement('button');stop.type='button';stop.className='secondary';stop.textContent='停止进程';stop.addEventListener('click',async()=>{stop.disabled=true;try{await api('/api/agent/processes/'+encodeURIComponent(item.id)+'/cancel',{method:'POST',json:{}});await refresh(false)}catch(e){status(e.message);stop.disabled=false}});row.append(stop)}else if(item.state!=='running'&&stop)stop.remove();rows.delete(item.id)
  }
  for(const row of rows.values())row.remove()
 }

 async function refresh(withScopes=true){if(!window.WechatAdmin.isAuthenticated())return;const epoch=state.epoch,scope=state.scope;try{
 const result=await api('/api/agent/workspace?scope='+encodeURIComponent(scope));if(epoch!==state.epoch)return;
 if(withScopes){const data=await api('/api/companion/state');if(epoch!==state.epoch)return;const select=$('#agent-doc-scope');select.replaceChildren(new Option('本账号项目指南','shared'),new Option('管理员全局笔记','global'));for(const item of data.scopes||[])select.add(new Option(item.thread_name||item.label||item.thread||item.id,item.id));select.value=scope}
 const select=$('#agent-doc-select'),selected=select.value;select.replaceChildren(new Option('选择文档',''));for(const d of result.documents)select.add(new Option(d.name,d.name));select.value=selected;processes(result.processes||[]);state.loaded=true;
 const layout=result.layout;if(layout){state.layout=layout;let box=$('#managed-workspace-status');if(!box){box=document.createElement('section');box.id='managed-workspace-status';box.className='workspace-runtime';const title=document.createElement('strong'),facts=document.createElement('dl'),publicRoot=document.createElement('dd'),privateRoot=document.createElement('dd');title.textContent='可用工作区';facts.className='workspace-facts';for(const [label,value] of [['公共工作区',publicRoot],['当前账号私有工作区',privateRoot]]){const dt=document.createElement('dt');dt.textContent=label;facts.append(dt,value)}box.append(title,facts);document.querySelector('#agent > .workspace-header').insertAdjacentElement('afterend',box)}const fields=box.querySelectorAll('dd');fields[0].textContent=layout.public_workspace||'未配置';fields[1].textContent=layout.private_workspace||'未配置';box.title=layout.backend?'运行环境：'+layout.backend:''}
 }catch(e){status(e.message)}}
 async function read(name){const epoch=++state.epoch,scope=state.scope;try{const d=await api('/api/agent/workspace?scope='+encodeURIComponent(scope)+'&name='+encodeURIComponent(name));if(epoch!==state.epoch)return;$('#agent-doc-name').value=d.name;$('#agent-doc-content').value=d.content;state.loadedName=d.name;state.revision=d.revision;state.dirty=false;status(d.revision?'已读取最新版本':'新文档')}catch(e){status(e.message)}}

 const toolbar=document.querySelector('#agent-workspace-panel .workspace-toolbar');
 const projectsButton=document.createElement('button');projectsButton.type='button';projectsButton.className='secondary';projectsButton.textContent='受管项目';toolbar.append(projectsButton);
 projectsButton.addEventListener('click',async()=>{
  const modal=window.WechatDialogs.open({title:'受管项目',subtitle:'按需使用音乐等扩展能力。项目与模型复用，输入和结果按账号保存。'});modal.body.textContent='正在读取项目清单…';
  try{const data=await api('/api/agent/projects');if(!modal.alive())return;modal.body.replaceChildren();
   for(const project of data.projects||[]){const section=document.createElement('section'),heading=document.createElement('div'),title=document.createElement('h3'),button=document.createElement('button'),usage=document.createElement('p');section.className='managed-project';heading.className='managed-project-heading';title.textContent=project.name||project.id;button.type='button';button.className='secondary';button.textContent='调用详情';button.setAttribute('aria-label','查看'+title.textContent+'的调用详情');button.addEventListener('click',()=>{const detail=window.WechatDialogs.open({title:title.textContent,subtitle:'实际登记的路径与参数，供排查环境和调用项目时使用。'}),pre=document.createElement('pre');pre.className='workspace-code';pre.textContent=JSON.stringify(project,null,2);detail.body.append(pre)});heading.append(title,button);usage.textContent=project.usage||'尚未填写使用说明，请查看调用详情。';section.append(heading,usage);modal.body.append(section)}
   const issues=data.unavailable_manifests||[];
   if(issues.length){const note=document.createElement('p');note.className='hint';note.setAttribute('role','status');const reasons={project_manifest_invalid:'JSON 对象格式无效',project_manifest_metadata_too_large:'登记信息超过 64 KiB',project_manifest_not_regular:'不是普通文件',project_not_registered:'文件已移除',project_manifest_unavailable:'文件暂不可读'};note.textContent='部分登记文件需检查：'+issues.map(item=>item.name+'（'+(reasons[item.reason]||'读取失败')+'）').join('、')+((data.projects||[]).length?'。其他项目仍可使用。':'。请修复登记信息后重新打开。');modal.body.append(note)}
   if(!(data.projects||[]).length&&!issues.length)modal.body.textContent='尚未登记项目。请先安装所需工具，再登记其入口和使用说明。';
  }catch(e){if(modal.alive())modal.body.textContent=e.message}
 });
 const mediaButton=document.createElement('button');mediaButton.type='button';mediaButton.className='secondary';mediaButton.textContent='附件投递';toolbar.append(mediaButton);
 mediaButton.addEventListener('click',()=>{
  const modal=window.WechatDialogs.open({title:'跨会话附件投递',subtitle:'查看当前账号的来源、目标与已保存回执，不会重发或重新生成。'}),results=document.createElement('div'),message=document.createElement('p'),more=document.createElement('button');
  message.className='hint';message.setAttribute('role','status');more.type='button';more.className='secondary';more.textContent='继续查看';more.hidden=true;modal.body.append(message,results,more);let offset=0,busy=false;
  const names={superseded:'已合并，停止重复投递',accepted:'微信已接受',queued:'排队或等待核对',paused:'已暂停，请检查授权或原任务',needs_review:'记录需检查',pending:'等待确认'};
  const kinds={voice:'语音',file:'文件',image:'图片',video:'视频'};
  async function load(){if(busy)return;busy=true;more.disabled=true;message.textContent='正在读取投递记录…';try{
   const data=await api('/api/agent/media-batches?offset='+offset+'&limit=30');if(!modal.alive())return;
   for(const item of data.items||[]){
    const row=document.createElement('section'),heading=document.createElement('div'),title=document.createElement('h3'),detail=document.createElement('button'),summary=document.createElement('p');row.className='managed-project';heading.className='managed-project-heading';
    title.textContent=item.source_name?(item.source_name+' → '+item.target_name):'投递记录需检查';detail.type='button';detail.className='secondary';detail.textContent='投递详情';detail.setAttribute('aria-label','查看'+title.textContent+'的投递详情');
    detail.addEventListener('click',()=>{
     const view=window.WechatDialogs.open({title:title.textContent,subtitle:names[item.state]||item.state}),facts=document.createElement('dl');facts.className='workspace-facts';
     for(const [label,value] of [['发起会话',item.origin_name],['来源会话',item.source_name],['目标会话',item.target_name],['操作编号',item.operation_id],['创建时间',item.created?new Date(item.created).toLocaleString('zh-CN',{hour12:false}):'未记录']]){if(!value)continue;const dt=document.createElement('dt'),dd=document.createElement('dd');dt.textContent=label;dd.textContent=value;facts.append(dt,dd)}
     view.body.append(facts);
     for(const part of item.parts||[]){const p=document.createElement('p');p.textContent='第 '+part.index+' 段 · '+(kinds[part.kind]||part.kind)+' · '+part.title+' · '+(names[part.state]||part.state)+(part.server_id?' · 回执 '+part.server_id:'');view.body.append(p)}
     const note=document.createElement('p');note.className='hint';note.textContent=item.reason||'已接受表示取得微信服务端回执，手机收件和播放需另外确认。';view.body.append(note);
    });
    summary.className='hint';summary.textContent=(names[item.state]||item.state)+(item.total_parts?' · 已确认 '+item.accepted_parts+'/'+item.total_parts+' 段':'');heading.append(title,detail);row.append(heading,summary);results.append(row);
   }
   offset=data.next_offset;more.hidden=!data.has_more;message.textContent=data.total?data.note:'暂无跨会话附件投递记录。';
  }catch(e){if(modal.alive())message.textContent=e.message}finally{busy=false;more.disabled=false}}
  more.addEventListener('click',load);load();
 });
 const globalButton=document.createElement('button');globalButton.type='button';globalButton.className='secondary';globalButton.textContent='账号全局检索';toolbar.append(globalButton);
 globalButton.addEventListener('click',()=>{
  let searchController,epoch=0,busy=false,offset=0,query='',thread='';
  const directoryController=new AbortController();
  const modal=window.WechatDialogs.open({title:'管理员全局会话',subtitle:'检索当前账号的全部已保存会话，无需开启群聊自动回复。不会自动发送或转发。',onClose:()=>{epoch++;searchController?.abort();directoryController.abort()}});
  const form=document.createElement('form'),scopeLabel=document.createElement('label'),scope=document.createElement('select'),label=document.createElement('label'),input=document.createElement('input'),button=document.createElement('button'),more=document.createElement('button'),results=document.createElement('div'),resultStatus=document.createElement('p'),directoryStatus=document.createElement('p');
  form.className='global-search-controls';scopeLabel.className=label.className='global-search-field';
  scopeLabel.textContent='会话范围';scope.setAttribute('aria-label','全局检索会话');const all=document.createElement('option');all.value='';all.textContent='全部会话';scope.append(all);scopeLabel.append(scope);
  label.textContent='关键词';input.type='search';input.placeholder='群名、文件名或聊天内容';input.setAttribute('aria-label','全局检索关键词');label.append(input);
  button.type='submit';button.className='primary';button.textContent='检索';more.type='button';more.className='secondary';more.textContent='继续查看';more.hidden=true;
  resultStatus.className=directoryStatus.className='hint';resultStatus.setAttribute('role','status');directoryStatus.setAttribute('role','status');
  form.append(scopeLabel,label,button);modal.body.append(form,directoryStatus,resultStatus,results,more);input.focus({preventScroll:true});
  async function search(reset){
   if(!modal.alive()||busy&&!reset)return;
   if(reset){epoch++;searchController?.abort();offset=0;query=input.value;thread=scope.value;results.replaceChildren();more.hidden=true}
   const current=epoch;searchController=new AbortController();busy=true;more.disabled=true;results.setAttribute('aria-busy','true');resultStatus.setAttribute('role','status');resultStatus.textContent='正在检索…';
   try{
    const params=new URLSearchParams({query,thread_id:thread,offset:String(offset),limit:'30'});
    const data=await api('/api/agent/global-search?'+params,{signal:searchController.signal});
    if(!modal.alive()||current!==epoch)return;
    for(const item of data.items||[]){const row=document.createElement('details'),summary=document.createElement('summary'),text=document.createElement('p');summary.textContent=(item.thread_name||item.thread_id)+' · '+new Date(item.at).toLocaleString('zh-CN',{hour12:false});text.textContent=item.kind==='asset'?('[素材] '+item.filename+' · '+item.bytes+' 字节 · '+item.asset_id):item.text;row.append(summary,text);results.append(row)}
    offset=data.next_offset;more.hidden=!data.has_more;resultStatus.textContent=data.total?'已显示 '+offset+' / '+data.total+' 条记录':'没有匹配的记录。';
   }catch(e){if(modal.alive()&&current===epoch&&e.name!=='AbortError'){resultStatus.setAttribute('role','alert');resultStatus.textContent=e.message}}
   finally{if(modal.alive()&&current===epoch){busy=false;more.disabled=false;results.setAttribute('aria-busy','false')}}
  }
  async function conversations(){
   directoryStatus.textContent='正在读取会话范围…';let next=0;const seen=new Set(),issues=new Set(),names=new Map();
   try{
    do{
     const data=await api('/api/agent/conversations?offset='+next+'&limit=100&refresh='+(next===0),{signal:directoryController.signal});if(!modal.alive())return;
     for(const item of data.items||[]){if(seen.has(item.id))continue;seen.add(item.id);const option=document.createElement('option'),name=item.name||item.id;option.value=item.id;option.textContent=name;const same=names.get(name)||[];same.push(option);names.set(name,same);if(same.length>1)for(const choice of same)choice.textContent=name+' · '+choice.value;scope.append(option)}
     for(const issue of data.unavailable_sources||[])issues.add(issue);
     if(!data.has_more)break;
     if(!Number.isInteger(data.next_offset)||data.next_offset<=next)throw new Error('会话范围读取未完成，仍可检索已保存记录。');next=data.next_offset;
    }while(modal.alive());
    if(modal.alive()){directoryStatus.textContent=issues.size?'部分微信目录暂不可读，已保存的会话仍可检索。':'';directoryStatus.hidden=!issues.size}
   }catch(e){if(modal.alive()&&e.name!=='AbortError')directoryStatus.textContent='会话范围暂未加载完整，仍可检索已保存记录。'}
  }
  form.addEventListener('submit',e=>{e.preventDefault();search(true)});form.addEventListener('keydown',e=>{if(e.key==='Escape'){e.preventDefault();e.stopPropagation();modal.close()}});scope.addEventListener('change',()=>search(true));more.addEventListener('click',()=>search(false));search(true);conversations();
 });
 $('#agent-doc-select').addEventListener('change',()=>{const name=$('#agent-doc-select').value;if(name&&discardDraft())read(name);else $('#agent-doc-select').value=state.loadedName});
 $('#agent-doc-scope').addEventListener('change',()=>{if(!discardDraft()){$('#agent-doc-scope').value=state.scope;return}state.epoch++;state.scope=$('#agent-doc-scope').value;clear();refresh(false)});
 window.WechatRefresh.bind($('#agent-doc-refresh'),()=>refresh(true));
 $('#agent-doc-new').addEventListener('click',()=>{if(!discardDraft())return;state.epoch++;clear();$('#agent-doc-select').value=''});
 for(const id of ['agent-doc-name','agent-doc-content'])$('#'+id).addEventListener('input',()=>{state.dirty=true;status('文档有未保存更改')});
 $('#agent-doc-save').addEventListener('click',async()=>{if(state.busy)return;const name=$('#agent-doc-name').value.trim();if(!name)return status('请填写 Markdown 文档名');const epoch=state.epoch,scope=state.scope,content=$('#agent-doc-content').value;state.busy=true;$('#agent-doc-save').disabled=true;try{const d=await api('/api/agent/workspace',{method:'POST',json:{scope,name,content,revision:name===state.loadedName?state.revision:''}});if(epoch!==state.epoch)return;state.loadedName=d.name;state.revision=d.revision;state.dirty=$('#agent-doc-content').value!==content||$('#agent-doc-name').value.trim()!==name;status(state.dirty?'保存期间还有新编辑，尚未保存':'文档已保存');await refresh(false)}catch(e){status(e.message+'；当前编辑内容已保留')}finally{state.busy=false;$('#agent-doc-save').disabled=false}});
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='agent')refresh(true)});
 window.addEventListener('wechat-admin:account-changed',()=>{state.epoch++;clearAccountWorkspace();state.scope='shared';state.loaded=false;$('#agent-process-list').replaceChildren()});
 window.addEventListener('wechat-admin:logout',()=>{state.epoch++;clearAccountWorkspace();state.scope='shared';state.loaded=false;$('#agent-process-list').replaceChildren()});
 setInterval(()=>{if(location.hash==='#agent'&&window.WechatAdmin.isAuthenticated())refresh(false)},5000);
})();
