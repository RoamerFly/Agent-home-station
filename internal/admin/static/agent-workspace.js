(() => {
 'use strict';
 const $=s=>document.querySelector(s),api=(...a)=>window.WechatAdmin.api(...a);
 const state={revision:'',loadedName:'',scope:'shared',busy:false,dirty:false,epoch:0,processSignature:'',loaded:false};
 const labels={running:'运行中',completed:'已完成',failed:'失败',cancelled:'已取消',timed_out:'执行超时',interrupted:'重启后待检查',needs_review:'需要核对'};
 function status(text){$('#agent-doc-state').textContent=text}
 function clear(){state.revision='';state.loadedName='';state.dirty=false;$('#agent-doc-name').value='';$('#agent-doc-content').value='';status('')}
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
 if(withScopes){const data=await api('/api/companion/state');if(epoch!==state.epoch)return;const select=$('#agent-doc-scope');select.replaceChildren(new Option('共享项目指南','shared'));for(const item of data.scopes||[])select.add(new Option(item.thread_name||item.thread||item.id,item.id));select.value=scope}
 const select=$('#agent-doc-select'),selected=select.value;select.replaceChildren(new Option('选择文档',''));for(const d of result.documents)select.add(new Option(d.name,d.name));select.value=selected;processes(result.processes||[]);state.loaded=true;
 }catch(e){status(e.message)}}
 async function read(name){const epoch=++state.epoch,scope=state.scope;try{const d=await api('/api/agent/workspace?scope='+encodeURIComponent(scope)+'&name='+encodeURIComponent(name));if(epoch!==state.epoch)return;$('#agent-doc-name').value=d.name;$('#agent-doc-content').value=d.content;state.loadedName=d.name;state.revision=d.revision;state.dirty=false;status(d.revision?'已读取最新版本':'新文档')}catch(e){status(e.message)}}
 $('#agent-doc-select').addEventListener('change',()=>{const name=$('#agent-doc-select').value;if(name)read(name)});
 $('#agent-doc-scope').addEventListener('change',()=>{state.epoch++;state.scope=$('#agent-doc-scope').value;clear();refresh(false)});
 $('#agent-doc-refresh').addEventListener('click',()=>refresh(true));
 $('#agent-doc-new').addEventListener('click',()=>{state.epoch++;clear();$('#agent-doc-select').value='';$('#agent-doc-name').focus()});
 for(const id of ['agent-doc-name','agent-doc-content'])$('#'+id).addEventListener('input',()=>{state.dirty=true;status('文档有未保存更改')});
 $('#agent-doc-save').addEventListener('click',async()=>{if(state.busy)return;const name=$('#agent-doc-name').value.trim();if(!name)return status('请填写 Markdown 文档名');const epoch=state.epoch,scope=state.scope,content=$('#agent-doc-content').value;state.busy=true;$('#agent-doc-save').disabled=true;try{const d=await api('/api/agent/workspace',{method:'POST',json:{scope,name,content,revision:name===state.loadedName?state.revision:''}});if(epoch!==state.epoch)return;state.loadedName=d.name;state.revision=d.revision;state.dirty=$('#agent-doc-content').value!==content||$('#agent-doc-name').value.trim()!==name;status(state.dirty?'保存期间还有新编辑，尚未保存':'文档已保存');await refresh(false)}catch(e){status(e.message+'；当前编辑内容已保留')}finally{state.busy=false;$('#agent-doc-save').disabled=false}});
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='agent')refresh(true)});
 window.addEventListener('wechat-admin:logout',()=>{state.epoch++;clear();state.scope='shared';state.loaded=false;$('#agent-process-list').replaceChildren()});
 setInterval(()=>{if(location.hash==='#agent'&&window.WechatAdmin.isAuthenticated())refresh(false)},5000);
})();
