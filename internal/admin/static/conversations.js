(() => {
 'use strict';
 const $=s=>document.querySelector(s),api=(...args)=>window.WechatAdmin.api(...args),jobs=new Map();
 let scopes=window.WechatAdmin.getScopes(),view=null,epoch=0,indexSignature='';
 const {element: node} = window.WechatControls;
 const button = (parent, text, onClick, style = 'quiet') => window.WechatControls.button(parent, text, onClick, style);
 const time=value=>window.WechatActivity.time(value);
 function conversations(){return scopes.map(scope=>({...scope,label:scope.label||scope.thread,updated:scope.last_interaction})).sort((a,b)=>(Date.parse(b.updated)||0)-(Date.parse(a.updated)||0))}
 function renderIndex(){
  const query=$('#conversation-search').value.trim().toLocaleLowerCase(),items=conversations().filter(x=>[x.label,x.thread].join(' ').toLocaleLowerCase().includes(query));
  const current=items.find(x=>x.thread===view?.item.thread);if(current)view.item={...view.item,...current};
  const signature=JSON.stringify([items.map(x=>[x.id,x.thread,x.label,x.updated,x.has_summary,x.message_count]),[...jobs]]);if(signature===indexSignature)return;indexSignature=signature;
  const y=window.scrollY,box=$('#companion-scopes'),position=$('#memory-panel').scrollTop;box.replaceChildren();$('#conversation-state').textContent=items.length+' 个群聊 / 私信，按最近活动排列';
  if(!items.length)node(box,'p','暂时没有匹配的会话。','empty');
  for(const item of items){
   const row=node(box,'article',undefined,'conversation-row');row.dataset.thread=item.thread;row.dataset.selected=String(item.thread===view?.item.thread);if(item.id)row.dataset.scopeId=item.id;
   const open=button(row,'',()=>openConversation(item),'conversation-open');open.setAttribute('aria-controls','conversation-inspector');
   const name=node(open,'span',undefined,'conversation-name');node(name,'strong',item.label);node(name,'small',item.thread.endsWith('@chatroom')?'群聊':'私信');
   node(open,'span',time(item.updated),'conversation-date');
   node(open,'span',(item.has_summary?'已压缩 · ':'')+(item.message_count||0)+' 条记忆','conversation-count');
   const actions=node(row,'div',undefined,'conversation-row-actions'),job=jobs.get(item.id);
   const compressButton=button(actions,job?.pending?'压缩中…':'压缩',()=>compress(item),'secondary');compressButton.disabled=!item.id||!!job?.pending;compressButton.setAttribute('aria-label','压缩'+item.label);
   if(job?.text&&(job.pending||job.error))node(actions,'small',job.text,job.error?'form-error':'hint');
  }
  $('#memory-panel').scrollTop=position;window.scrollTo({top:y,behavior:'instant'});
 }
 function syncCompression(){
  renderIndex();if(!view)return;$('#context-summary-view').disabled=!view.data;const job=jobs.get(view.item.id);$('#context-compress-now').disabled=!view.item.id||!!job?.pending;$('#context-compress-state').textContent=job?.text||'';
 }
 async function refresh(){const b=$('#conversation-refresh');b.disabled=true;try{await Promise.all([window.WechatAdmin.refreshScopes()]);renderIndex()}catch(e){$('#conversation-state').textContent=e.message}finally{b.disabled=false}}
 async function compress(item){
  if(!item.id||jobs.get(item.id)?.pending)return;const stamp=epoch;
  jobs.set(item.id,{pending:true,text:'正在压缩…'});syncCompression();
  try{
   const result=await api('/api/companion/memory/'+encodeURIComponent(item.id)+'/compress',{method:'POST',json:{}});if(stamp!==epoch)return;
   jobs.set(item.id,{pending:false,text:result.compressed?'已压缩':'近期消息不足，无需压缩'});
   await window.WechatAdmin.refreshScopes();if(stamp!==epoch)return;
   // Completing A must never reload B's transcript or disable B's controls.
   if(view?.item.id===item.id)await loadView(view);
  }catch(e){if(stamp===epoch)jobs.set(item.id,{pending:false,text:e.message,error:true})}
  finally{if(stamp===epoch)syncCompression()}
 }
 function renderSummary(parent,data){
  if(!data.summary)return;
  const card=node(parent,'article',undefined,'conversation-summary');node(card,'h3','上下文摘要');
  node(card,'p','已压缩 '+data.compressed_count+' 条；压缩前 '+data.history_count+' 条消息已收起。'+(data.retained_context_count?'其中 '+data.retained_context_count+' 条仍作为模型的近期上下文保留。':''),'hint');
  let parsed;try{parsed=JSON.parse(data.summary)}catch{}
  if(parsed&&typeof parsed==='object'&&!Array.isArray(parsed)){
   if(parsed.current_focus)node(card,'p',String(parsed.current_focus),'summary-focus');
   for(const [key,title,describe]of [
    ['pending_tasks','待办',x=>[x.request,x.state].filter(Boolean).join(' · ')],
    ['topics','话题',x=>x.text],['people','关系',x=>[x.name,x.notes].filter(Boolean).join('：')],
    ['preferences','偏好',x=>x],['corrections','更正',x=>x],['media','媒体',x=>[x.title,x.state].filter(Boolean).join(' · ')]
   ]){const values=Array.isArray(parsed[key])?parsed[key].map(describe).filter(x=>typeof x==='string'&&x):[];if(!values.length)continue;node(card,'h4',title);const list=node(card,'ul');for(const value of values)node(list,'li',value)}
  }else node(card,'p',data.summary,'round-body');
  button(card,'完整摘要',()=>{const popup=window.WechatDialogs.open({title:'完整上下文摘要'});node(popup.body,'pre',parsed?JSON.stringify(parsed,null,2):data.summary,'technical-json')});
 }
 function renderFeed(v){
  if(view!==v||!v.popup.alive())return;
  const root=$('#memory-content'),scroll=v.popup.body.scrollTop;root.replaceChildren();
  const data=v.data||{messages:[]},units=[];
  window.WechatMemory?.mount(root,v.item.id,()=>{if(view===v)loadView(v)});renderSummary(root,data);
  for(const message of data.messages||[])units.push({time:message.time,message});
  units.sort((a,b)=>(Date.parse(b.time)||0)-(Date.parse(a.time)||0));
  for(const unit of units){
   const row=node(root,'article',undefined,'conversation-message');
   if(unit.message)row.append(window.WechatAdmin.renderMemoryMessage(unit.message));


  }
  if(!units.length)node(root,'p',data.summary?'压缩后的新消息会显示在这里。旧消息可通过上方按钮查看。':'暂时没有保存的聊天内容。','empty');
  $('#memory-history').hidden=!data.history_count;$('#memory-history').textContent='压缩前的消息（'+(data.history_count||0)+'）';
  v.popup.body.scrollTop=scroll;
 }
 async function loadView(v){
  if(!v.item.id){v.data={messages:[]};renderFeed(v);$('#memory-load-state').textContent='此会话目前仅有执行记录，尚未保存聊天记忆。';return}
  const request=++v.request;$('#memory-load-state').textContent='正在读取会话…';
  try{
   const data=await api('/api/companion/memory/'+encodeURIComponent(v.item.id));if(view!==v||request!==v.request||!v.popup.alive())return;
   v.data=data;renderFeed(v);$('#memory-load-state').textContent='消息从新到旧排列。';syncCompression();
  }catch(e){if(view===v&&request===v.request)$('#memory-load-state').textContent=e.message}
 }
 function openConversation(item){
  view?.popup.close();const root=$('#memory-detail');root.hidden=false;
  const v={item,request:0,data:null,popup:null};
  $('#scope-palace-open').disabled=!item.id;
  $('#conversation-title').textContent=item.label;$('#conversation-idle').hidden=true;
  $('#conversation-inspector').append(root);document.querySelector('.conversation-workspace').classList.add('has-selection');
  for(const row of document.querySelectorAll('.conversation-row'))row.dataset.selected=String(row.dataset.thread===item.thread);
  v.popup={body:$('#memory-content'),alive:()=>view===v&&root.isConnected,close:()=>{
   if(view!==v)return;view=null;root.hidden=true;$('#conversation-detail-parking').append(root);$('#conversation-idle').hidden=false;
   document.querySelector('.conversation-workspace').classList.remove('has-selection');for(const row of document.querySelectorAll('.conversation-row'))row.dataset.selected='false';window.dispatchEvent(new CustomEvent('wechat-admin:memory-selection',{detail:{id:''}}));
  }};
  view=v;$('#memory-content').replaceChildren();$('#memory-note').value='';window.dispatchEvent(new CustomEvent('wechat-admin:memory-selection',{detail:{id:item.id||''}}));syncCompression();loadView(v);
 }
 function openHistory(){
  const v=view;if(!v?.data?.history_count)return;const id=v.item.id,popup=window.WechatDialogs.open({title:v.item.label+' · 压缩前的消息',subtitle:'从新到旧排列；只读查看，不重新注入模型。'});
  const status=node(popup.body,'p','正在读取…','hint'),list=node(popup.body,'div',undefined,'conversation-feed');let offset=0,revision='',busy=false;
  const more=button(popup.body,'加载更多',load,'secondary');
  async function load(){if(busy)return;busy=true;more.disabled=true;try{const q=new URLSearchParams({offset:String(offset)});if(revision)q.set('revision',revision);const data=await api('/api/companion/memory/'+encodeURIComponent(id)+'/history?'+q);if(!popup.alive())return;for(const message of data.messages||[])list.append(window.WechatAdmin.renderMemoryMessage(message));revision=data.revision;offset=data.next_offset;more.hidden=offset==null;status.textContent='共 '+data.total+' 条；'+data.compressed_count+' 条已压缩，'+data.retained_context_count+' 条仍保留为近期上下文。'}catch(e){if(popup.alive())status.textContent=e.message}finally{busy=false;more.disabled=false}}
  load();
 }
 function manage(){
  const v=view;if(!v?.data)return;const content=$('#memory-management');const popup=window.WechatDialogs.open({title:v.item.label+' · 备注与管理',content,onClose:()=>$('#memory-management-parking').append(content)});
  const notes=$('#memory-notes');notes.replaceChildren();for(const note of v.data.notes||[])node(notes,'p',note,'memory-note');
  $('#conversation-preferences').value=v.data.preferences||'';$('#conversation-preferences-state').textContent='';
  popup.body.dataset.scopeId=v.item.id;
 }
 async function savePreferences(){
  const v=view;if(!v?.data)return;const button=$('#save-conversation-preferences'),status=$('#conversation-preferences-state');button.disabled=true;
  try{const result=await api('/api/companion/memory/'+encodeURIComponent(v.item.id)+'/preferences',{method:'POST',json:{text:$('#conversation-preferences').value,revision:v.data.preferences_revision||0}});if(view!==v)return;v.data.preferences=result.text;v.data.preferences_revision=result.revision;status.textContent='已保存，仅对本会话生效'}catch(e){if(view===v)status.textContent=e.message}finally{button.disabled=false}
 }
 $('#save-conversation-preferences').addEventListener('click',savePreferences);
 async function saveNote(){const v=view,note=$('#memory-note').value.trim();if(!v?.data||!note)return;const b=$('#save-memory-note');b.disabled=true;try{await api('/api/companion/memory/'+encodeURIComponent(v.item.id)+'/notes',{method:'POST',json:{text:note}});if(view!==v)return;$('#memory-note').value='';v.data.notes=[...(v.data.notes||[]),note];node($('#memory-notes'),'p',note,'memory-note');await window.WechatAdmin.refreshScopes()}catch(e){node($('#memory-notes'),'p',e.message,'form-error')}finally{b.disabled=false}}
 async function clear(){const v=view;if(!v?.data||!confirm('清除这个会话的活跃记忆和摘要？只读归档和微信聊天记录仍保留。'))return;const b=$('#clear-memory');b.disabled=true;try{await api('/api/companion/memory/'+encodeURIComponent(v.item.id)+'/clear',{method:'POST',json:{}});if(view===v)await loadView(v);await window.WechatAdmin.refreshScopes()}catch(e){node($('#memory-notes'),'p',e.message,'form-error')}finally{b.disabled=false}}
 $('#conversation-search').addEventListener('input',renderIndex);window.WechatRefresh.bind($('#conversation-refresh'),refresh);
 $('#scope-palace-open').addEventListener('click',()=>{
  if(view?.item.id)window.WechatPalace.openConversation(view.item.id,view.item.label);
 });
 $('#context-summary-view').addEventListener('click',()=>{
  const data=view?.data;if(!data)return;
  const updated=data.summary_updated&&Date.parse(data.summary_updated)>0?new Date(data.summary_updated).toLocaleString():'';
  const popup=window.WechatDialogs.open({title:view.item.label+' · 压缩摘要',subtitle:data.summary&&updated?'最近成功压缩于 '+updated:''});
  if(data.summary)renderSummary(popup.body,data);else node(popup.body,'p','这个会话还没有成功保存的压缩摘要。','hint');
 });
 $('#context-compress-now').addEventListener('click',()=>view&&compress(view.item));$('#memory-history').addEventListener('click',openHistory);window.WechatRefresh.bind($('#memory-refresh'),()=>view&&loadView(view));$('#memory-settings').addEventListener('click',manage);$('#save-memory-note').addEventListener('click',saveNote);$('#clear-memory').addEventListener('click',clear);
 window.addEventListener('wechat-admin:scopes',e=>{
  scopes=e.detail.scopes;
  if(view&&!scopes.some(item=>item.thread===view.item.thread&&item.id===view.item.id))view.popup.close();
  renderIndex();
 });


 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail.section==='memory'){renderIndex();if(!scopes.length)window.WechatAdmin.refreshScopes()}});
 window.addEventListener('wechat-admin:logout',()=>{epoch++;jobs.clear();scopes=[];view=null;indexSignature='';$('#companion-scopes').replaceChildren()});
 $('#conversation-close').addEventListener('click',()=>view?.popup.close());
 $('#conversation-send').addEventListener('click',()=>{if(!view)return;window.WechatAdmin.nav('tools');window.dispatchEvent(new CustomEvent('wechat-admin:recipient',{detail:{thread:view.item.thread,label:view.item.label}}))});
 $('#conversation-members').addEventListener('click',()=>{if(!view)return;window.WechatAdmin.nav('catalog');window.dispatchEvent(new CustomEvent('wechat-admin:catalog-target',{detail:{thread:view.item.thread}}))});
 $('#conversation-rule').addEventListener('click',()=>{if(!view)return;window.WechatAdmin.nav('rules');window.dispatchEvent(new CustomEvent('wechat-admin:rule-target',{detail:{thread:view.item.thread}}))});
 window.WechatConversations={getScope:()=>view?.data,reload:()=>view&&loadView(view),openConversation,compress};renderIndex();
})();
