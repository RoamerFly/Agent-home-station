(() => {
 'use strict';
 const api=(...a)=>window.WechatAdmin.api(...a);
 const {element: el, button} = window.WechatControls;
 function field(parent,label,value='',type='text'){const l=el(parent,'label',label),n=el(l,type==='textarea'?'textarea':'input');if(type!=='textarea')n.type=type;n.value=value;return n}
 const names={fact:'事实',preference:'偏好',episode:'事件',lesson:'经验'},states={active:'有效',conflicted:'待核实',superseded:'已更正',deleted:'已遗忘'};
 function memoryState(item){
  if(item.status==='active' && item.commitment_state){
   const label={open:'约定待办',ended:'约定已结束',cancelled:'约定已取消'}[item.commitment_state];
   if(label)return label;
  }
  return states[item.status]||item.status||'已保存';
 }
 function showMemoryDetail(title,data){
  const p=window.WechatDialogs.open({title});
  if(data.detail){
   for(const item of [data,...(data.revisions||[])]){const card=el(p.body,'article',undefined,'conversation-summary');el(card,'h3',(item===data?'当前内容':'此前内容')+' · 第 '+item.version+' 版');el(card,'p',item.detail,'round-body');el(card,'p',memoryState(item),'hint');for(const ref of item.sources||[])if(ref.quote)el(card,'blockquote',ref.quote)}
  }else{
   el(p.body,'p',(data.events||0)+' 条来源 · '+(data.entries||0)+' 条长期记忆');
   const jobs=data.jobs||[];if(!jobs.length)el(p.body,'p','暂无需要整理的会话。','hint');
   for(const job of jobs){const card=el(p.body,'article',undefined,'conversation-summary');el(card,'h3',({idle:'整理完成',running:'正在整理',pending:'等待整理',failed:'整理未完成',budget_wait:'今日整理额度已用完'})[job.state]||'等待更新');el(card,'p','待整理 '+(job.pending_sources||0)+' 条消息');if(job.updated)el(card,'p','更新于 '+window.WechatActivity.time(job.updated),'hint');if(job.state==='failed')el(card,'p','请检查记忆服务配置后重试。','hint')}
  }
 }
 const person=id=>window.WechatNames?.display(id,'群成员')||'群成员';
 function relationParticipant(item,assertion,side){const id=side==='object'?assertion.object:assertion.subject,known=(item.sources||[]).find(ref=>ref.sender===id);if(known)return known.sender_name||person(id);if(side==='object'&&assertion.object_basis==='literal')return assertion.object_text||id;if(side==='object'&&assertion.object_basis==='relative')return relationParticipant(item,assertion,'subject')+'的'+(assertion.object_text||assertion.predicate);if(side==='subject'&&assertion.subject_text&&!['我','你','您'].includes(assertion.subject_text))return assertion.subject_text;return person(id)}
 function relations(parent,item){for(const a of item.assertions||[]){const row=el(parent,'div',undefined,'conversation-summary');const predicates={father:'父亲',mother:'母亲',spouse:'配偶',child:'子女',attribute:'相关信息',reports:'转述'};el(row,'strong',relationParticipant(item,a,'subject')+' · '+(a.negated?'不是':'')+(predicates[a.predicate]||'关系信息')+' · '+relationParticipant(item,a,'object'));el(row,'p',({explicit:'明确陈述',reported:'他人转述',corrected:'已更正',uncertain:'尚未确定'})[a.state]||'待核对','hint');const reporter=(item.sources||[]).find(ref=>ref.sender===a.asserted_by);el(row,'p','陈述者：'+(reporter?.sender_name||person(a.asserted_by)),'hint')}}
 async function source(scope,id){const p=window.WechatDialogs.open({title:'原始来源'}),status=el(p.body,'p','正在读取…','hint');try{const e=await api('/api/memory/'+scope+'/sources/'+encodeURIComponent(id));if(!p.alive())return;status.textContent=[e.sender_name||person(e.sender),window.WechatActivity?.time(e.at)||e.at,e.complete?'完整原文':'旧记录，完整性未知','版本 '+e.revision].join(' · ');if(e.addressees?.length)el(p.body,'p','对话对象：'+e.addressees.map(person).join('、'),'hint');for(const m of e.incoming_media||[])el(p.body,'p','媒体上传者：'+person(m.uploader_id)+' · '+(({verified:'已读取',observed:'已观察',failed:'未读成功',pending:'等待读取'})[m.observation_state]||'待核对'),'hint');el(p.body,'pre',e.text,'readable-full-text')}catch(e){if(p.alive())status.textContent=e.message}}
 function chooseCorrectionSource(scope,subject,onPick){
  const panel=window.WechatDialogs.open({title:'选择更正依据',subtitle:'从当前会话选择一条用来更正的原话。'});
  const form=el(panel.body,'form'),query=field(form,'搜索原话');
  el(form,'button','搜索','primary').type='submit';
  const status=el(panel.body,'p','','hint'),list=el(panel.body,'div');
  status.setAttribute('role','status');
  const more=button(panel.body,'加载更多',()=>load(false),'secondary');
  let cursor='',controller;
  async function load(reset){
   if(reset){controller?.abort();cursor='';list.replaceChildren()}
   const current=new AbortController();
   controller=current;more.disabled=true;status.textContent='正在读取…';
   const params=new URLSearchParams({query:query.value,originals:'true'});
   if(subject)params.set('subject',subject);
   if(cursor)params.set('cursor',cursor);
   try{
    const {page}=await api('/api/memory/'+encodeURIComponent(scope)+'?'+params,{signal:current.signal});
    if(!panel.alive()||controller!==current)return;
    for(const {event} of page.items){
     if(event?.role!=='user'||!event.text||(subject&&event.sender!==subject))continue;
     const row=el(list,'article',undefined,'conversation-summary');
     window.WechatDialogs.text(row,event.text,{title:'完整原话'});
     el(row,'p',[event.sender_name||person(event.sender),event.at].filter(Boolean).join(' · '),'hint');
     button(row,'选用这条',()=>{onPick({event_id:event.id,quote:event.text});panel.close()});
    }
    cursor=page.next_cursor||'';more.hidden=!page.has_more;
    status.textContent=list.childElementCount?'选择原话后可缩短引句。':cursor?'本页没有合适的原话，可继续查看。':'没有匹配的原话。';
   }catch(error){if(!current.signal.aborted&&panel.alive())status.textContent=error.message}
   finally{if(controller===current)more.disabled=false}
  }
  form.addEventListener('submit',event=>{event.preventDefault();load(true)});
  panel.dialog.addEventListener('close',()=>controller?.abort(),{once:true});load(true);
 }
 function correct(scope,m,reload,selectSource=false){const p=window.WechatDialogs.open({title:'更正长期记忆'}),form=el(p.body,'form'),content=field(form,'更正内容',m.detail,'textarea'),sourceFields=selectSource?el(form,'div',undefined,'memory-correction-source'):form,id=field(sourceFields,selectSource?'来源事件编号':'新来源事件编号'),quote=field(sourceFields,'该来源中的精确原句','','textarea'),status=el(form,'p','','hint');status.setAttribute('role','status');content.required=true;if(!m.manual){id.required=true;quote.required=true}
  if(selectSource){const picker=button(form,'从当前会话选择原话',()=>chooseCorrectionSource(scope,m.subject,source=>{id.value=source.event_id;quote.value=source.quote;status.textContent='已选取来源。请核对并按需要缩短精确引句。'}),'secondary');picker.type='button';const manual=el(form,'details',undefined,'disclosure');el(manual,'summary','手工填写来源编号（兼容）');manual.append(sourceFields)}
  const save=el(form,'button','保存更正','primary');save.type='submit';form.addEventListener('submit',async e=>{e.preventDefault();if(!form.reportValidity())return;save.disabled=true;try{await api('/api/memory/'+scope+'/entries/'+m.id,{method:'POST',json:{action:'correct',expected_revision:m.version,detail:content.value,sources:id.value?[{event_id:id.value.trim(),quote:quote.value}]:[]}});p.close();reload()}catch(e){status.textContent=e.message}finally{save.disabled=false}})
 }
 function experienceLabel(item){
  const state={open:'共同约定',ended:'已结束的约定',cancelled:'已取消约定'}[item.commitment_state];
  return state||(item.perspective==='source_statement'||item.perspective==='reported_statement'?'成员陈述':'聊过的事');
 }
 function experiencePanel(scope,onChanged,body){
  const alive=()=>body.isConnected,controls=el(body,'div',undefined,'memory-experience-controls'),viewLabel=el(controls,'label','查看'),viewPicker=el(viewLabel,'select'),personFilter=el(controls,'div','','hint'),status=el(body,'p','正在读取…','hint'),list=el(body,'div',undefined,'memory-experience-list');status.setAttribute('role','status');
  const views=[['','共同经历'],['relations','人物关系'],['open_commitments','未完约定']];for(const [value,label]of views){const option=el(viewPicker,'option',label);option.value=value}
  let cursor='',busy=false,controller,view='',personID='';
  const more=button(body,'加载更多经历',()=>load(false),'secondary');
  function changed(){onChanged?.();load(true)}
  function viewName(){return ({relations:'人物关系',open_commitments:'未完约定'})[view]||'共同经历'}
  function forget(item){const relationship=Array.isArray(item.assertions)&&item.assertions.length>0,confirm=window.WechatDialogs.open({title:relationship?'遗忘这条关系记录':'遗忘这段经历',subtitle:relationship?'会遗忘整条来源记忆及其中的全部关系，不能只移除其中一项。只读历史归档仍保留。':'这段经历不再用于自动召回；只读历史归档仍保留。'});button(confirm.body,'确认遗忘',async()=>{try{await api('/api/memory/'+encodeURIComponent(scope)+'/entries/'+encodeURIComponent(item.id),{method:'POST',json:{action:'forget',expected_revision:item.version}});confirm.close();changed()}catch(e){status.textContent=e.message}},'danger')}
  function personButtons(actions,item){
   const labelFor=id=>{const source=(item.sources||[]).find(ref=>ref.sender===id);if(source)return source.sender_name||person(id);for(const assertion of item.assertions||[]){if(assertion.subject===id)return relationParticipant(item,assertion,'subject');if(assertion.object===id)return relationParticipant(item,assertion,'object')}return person(id)};
   const people=new Map();if(item.subject)people.set(item.subject,labelFor(item.subject));for(const assertion of item.assertions||[]){if(assertion.subject)people.set(assertion.subject,labelFor(assertion.subject));if(assertion.object&&assertion.object_basis!=='literal')people.set(assertion.object,labelFor(assertion.object));}
   for(const [id,label]of people){const inspect=button(actions,'查看 '+label,()=>{personID=id;personFilter.textContent='正在查看 '+label+'；';const clear=button(personFilter,'查看全部',()=>{personID='';personFilter.replaceChildren();load(true)},'quiet');clear.type='button';load(true)},'quiet');inspect.dataset.personId=id}
  }
  function relationRows(parent,item){
   const assertions=item.assertions||[];if(!assertions.length){el(parent,'p','这条记录没有可展示的结构化关系。','hint');return}
   relations(parent,item);
  }
  function render(item){
   const relationView=view==='relations',structured=Array.isArray(item.assertions)&&item.assertions.length>0,row=el(list,'article',undefined,'conversation-summary memory-experience-item');el(row,'h3',relationView?'人物关系':experienceLabel(item));
   if(relationView)relationRows(row,item);else{window.WechatDialogs.text(row,item.detail||item.summary||'未命名经历',{title:'完整共同经历'});if(item.summary&&item.summary!==item.detail)el(row,'p',item.summary,'hint');if(structured)relations(row,item)}
   const refs=item.sources||[],speakers=new Map();for(const ref of refs)if(ref.sender)speakers.set(ref.sender,ref.sender_name||person(ref.sender));
   const meta=['发言者：'+([...speakers.values()].join('、')||'未知')];if(item.subject&&!speakers.has(item.subject))meta.push('相关人物：'+person(item.subject));if(item.updated)meta.push('更新于 '+window.WechatActivity.time(item.updated));meta.push('第 '+item.version+' 版');el(row,'p',meta.join(' · '),'hint');
   if(!relationView){const detail=String(item.detail||item.summary||'').replace(/^\s*(?:成员|发言者)陈述：\s*/,'').trim();for(const ref of refs)if(ref.quote&&detail!==String(ref.quote).trim())el(row,'blockquote',ref.quote)}
   const actions=el(row,'div',undefined,'dialog-actions');for(const [index,ref] of refs.entries()){const label=refs.length>1?'查看来源 '+(index+1)+(ref.sender_name||ref.sender?'（'+(ref.sender_name||person(ref.sender))+'）':''):'查看来源';button(actions,label,()=>source(scope,ref.event_id));}
   if(view==='relations'||view==='open_commitments')personButtons(actions,item);
   button(actions,'完整记忆与修订',async()=>{try{showMemoryDetail('记忆来源与版本',await api('/api/memory/'+encodeURIComponent(scope)+'/entries/'+encodeURIComponent(item.id)))}catch(e){status.textContent=e.message}});
   if(!structured){const entry={...item,sources:refs};button(actions,'更正',()=>correct(scope,entry,changed,true));}
   button(actions,'遗忘',()=>forget(item));
  }
  async function load(reset){
   if(busy&&!reset)return;if(reset){controller?.abort();cursor='';list.replaceChildren()}busy=true;controller=new AbortController();const current=controller;more.disabled=true;body.setAttribute('aria-busy','true');
   try{const params=new URLSearchParams({limit:'20'});if(view)params.set('view',view);if(personID)params.set('person',personID);if(cursor)params.set('cursor',cursor);const data=await api('/api/companion/memory/'+encodeURIComponent(scope)+'/experiences?'+params,{signal:current.signal});if(!alive()||controller!==current)return;
    const items=data.items||[];for(const item of items)render(item);cursor=data.next_cursor||'';more.hidden=!data.has_more;
    status.textContent=items.length||list.children.length?'只显示有来源的'+viewName()+'。':'这个会话还没有可展示的'+viewName()+'。';
    if(!list.children.length)el(list,'p',view==='relations'?'暂时没有可展示的人物关系。':view==='open_commitments'?'暂时没有未完约定。':'暂时没有共同经历。之后形成的约定和可核对经历会显示在这里。','empty');
   }catch(e){if(!current.signal.aborted&&alive())status.textContent=e.message}finally{if(controller===current){busy=false;more.disabled=false;body.removeAttribute('aria-busy')}}
  }
  viewPicker.addEventListener('change',()=>{view=viewPicker.value;personID='';personFilter.replaceChildren();load(true)});
  return {load,dispose:()=>controller?.abort()};
 }
 function reflectionPanel(scope,onChanged,body){
  const alive=()=>body.isConnected,status=el(body,'p','将在打开时读取。','hint'),list=el(body,'div',undefined,'memory-reflection-list');status.setAttribute('role','status');
  let cursor='',busy=false,controller;
  const more=button(body,'加载更多记录',()=>load(false),'secondary');more.hidden=true;
  const sourceID=item=>item.source?.event_id||item.source_event_id;
  const errorMessage=error=>error?.status===409?'这条想法刚刚发生变化，请刷新后再试。':String(error?.message||error||'操作未完成，请重试。');
  function changed(){onChanged?.();load(true)}
  function correct(item){
   const panel=window.WechatDialogs.open({title:'更正兴趣与心情',subtitle:'兴趣和当时的心情可分别保留；来源原文不会改变。'}),form=el(panel.body,'form'),note=field(form,'兴趣（可留空）',item.note||'','textarea'),feeling=field(form,'当时的心情（可留空）',item.feeling||'','textarea'),status=el(form,'p','','hint'),save=el(form,'button','保存','primary');
   status.setAttribute('role','status');save.type='submit';
   form.addEventListener('submit',async event=>{event.preventDefault();const nextNote=note.value.trim(),nextFeeling=feeling.value.trim();if(!nextNote&&!nextFeeling){status.textContent='请保留兴趣或心情中的至少一项；若要整体移除，请使用“遗忘”。';note.focus();return}save.disabled=true;status.textContent='正在保存…';try{await api('/api/companion/memory/'+encodeURIComponent(scope)+'/reflections/'+encodeURIComponent(item.id),{method:'POST',json:{action:'correct',expected_revision:item.revision,note:nextNote,feeling:nextFeeling}});panel.close();changed()}catch(error){status.textContent=errorMessage(error)}finally{save.disabled=false}});
  }
  function forget(item){
   const panel=window.WechatDialogs.open({title:'忘记这条想法',subtitle:'她将不再用它决定要不要继续了解相关内容；来源原文仍按会话权限保存。'}),status=el(panel.body,'p','','hint'),confirm=button(panel.body,'确认忘记',async()=>{confirm.disabled=true;status.textContent='正在更新…';try{await api('/api/companion/memory/'+encodeURIComponent(scope)+'/reflections/'+encodeURIComponent(item.id),{method:'POST',json:{action:'forget',expected_revision:item.revision}});panel.close();changed()}catch(error){status.textContent=errorMessage(error);confirm.disabled=false}},'danger');
  }
  function render(item){
   const row=el(list,'article',undefined,'conversation-summary memory-reflection-item'),hasInterest=Boolean(item.note),hasFeeling=Boolean(item.feeling),title=hasInterest&&hasFeeling?'兴趣与心情':hasInterest?'她记下的兴趣':'她当时的心情';el(row,'h3',title);
   if(hasInterest)window.WechatDialogs.text(el(row,'div',undefined,'memory-reflection-content'),'兴趣：'+item.note,{title:'完整兴趣'});
   if(hasFeeling)window.WechatDialogs.text(el(row,'div',undefined,'memory-reflection-content'),'当时的心情：'+item.feeling,{title:'当时的心情'});
   const origin=item.source||{},meta=[];if(origin.sender_name||origin.sender)meta.push('来自 '+(origin.sender_name||person(origin.sender)));if(origin.at)meta.push(window.WechatActivity.time(origin.at));if(item.updated)meta.push('更新于 '+window.WechatActivity.time(item.updated));meta.push('第 '+item.revision+' 版');el(row,'p',meta.join(' · '),'hint');
   if(origin.observation_excerpt)window.WechatDialogs.text(row,'观察依据：'+origin.observation_excerpt,{title:'完整观察依据'});
   if(item.parent_experience?.detail)el(row,'p','关联经历：'+item.parent_experience.detail,'hint');
   const actions=el(row,'div',undefined,'dialog-actions'),eventID=sourceID(item);if(eventID)button(actions,'查看来源',()=>source(scope,eventID));
   button(actions,'更正',()=>correct(item));button(actions,'遗忘',()=>forget(item));
  }
  async function load(reset){
   if(busy&&!reset)return;if(reset){controller?.abort();cursor='';list.replaceChildren()}busy=true;controller=new AbortController();const current=controller;more.disabled=true;body.setAttribute('aria-busy','true');status.textContent='正在读取…';
   try{const params=new URLSearchParams({limit:'20'});if(cursor)params.set('cursor',cursor);const data=await api('/api/companion/memory/'+encodeURIComponent(scope)+'/reflections?'+params,{signal:current.signal});if(!alive()||controller!==current)return;for(const item of data.items||[])render(item);cursor=data.next_cursor||'';more.hidden=!cursor;status.textContent=list.children.length?'每条记录都可以查看来源、更正或遗忘。':'暂时没有可展示的兴趣或心情。';
   }catch(error){if(!current.signal.aborted&&alive())status.textContent=errorMessage(error)}finally{if(controller===current){busy=false;more.disabled=false;body.removeAttribute('aria-busy')}}
  }
  return {load,dispose:()=>controller?.abort()};
 }
 function open(scope,onChanged){const p=window.WechatDialogs.open({title:'会话记忆',subtitle:'共同经历、兴趣与心情及全部记忆都保留来源；更正或遗忘会同步更新。'}),tabs=el(p.body,'div',undefined,'memory-tabs'),experiencePane=el(p.body,'section',undefined,'memory-tab-panel'),reflectionPane=el(p.body,'section',undefined,'memory-tab-panel'),allPane=el(p.body,'section',undefined,'memory-tab-panel'),form=el(allPane,'form'),q=field(form,'搜索内容'),original=field(form,'搜索原始消息','','checkbox'),status=el(allPane,'p','正在读取…','hint'),list=el(allPane,'div'),search=el(form,'button','搜索','primary');search.type='submit';status.setAttribute('role','status');let cursor='',busy=false,controller;
  const experienceView=experiencePanel(scope,onChanged,experiencePane),reflectionView=reflectionPanel(scope,onChanged,reflectionPane),experienceTab=button(tabs,'共同经历',()=>select('experience'),'quiet'),reflectionTab=button(tabs,'兴趣与心情',()=>select('reflections'),'quiet'),allTab=button(tabs,'全部记忆',()=>select('all'),'quiet');function select(name){const experiences=name==='experience',reflections=name==='reflections',all=name==='all';experiencePane.hidden=!experiences;reflectionPane.hidden=!reflections;allPane.hidden=!all;experienceTab.dataset.active=String(experiences);reflectionTab.dataset.active=String(reflections);allTab.dataset.active=String(all);experienceTab.setAttribute('aria-pressed',String(experiences));reflectionTab.setAttribute('aria-pressed',String(reflections));allTab.setAttribute('aria-pressed',String(all));if(experiences)experienceView.load(true);else if(reflections)reflectionView.load(true);else load(true)}select('experience');
  button(allPane,'重新整理与补建索引',async()=>{try{await api('/api/memory/'+scope+'/rebuild',{method:'POST',json:{}});status.textContent='已安排后台重建。可刷新查看进度；显式重建不受自动维护日预算限制。'}catch(e){status.textContent=e.message}});const more=button(allPane,'加载更多',()=>load(false));
  async function load(reset){if(busy){controller?.abort()}busy=true;controller=new AbortController();const current=controller;if(reset){cursor='';list.replaceChildren()}more.disabled=true;p.body.setAttribute('aria-busy','true');try{const params=new URLSearchParams({query:q.value,originals:String(original.checked)});if(cursor)params.set('cursor',cursor);const data=await api('/api/memory/'+scope+'?'+params,{signal:current.signal});if(!p.alive()||controller!==current)return;
   status.textContent=data.status.events+' 条来源，'+data.status.entries+' 条派生记忆，'+data.status.vectors+' 条向量。'+(data.page.degraded?'当前使用词法检索。':'')+' '+(data.status.jobs||[]).map(x=>({idle:'已整理',running:'整理中',pending:'待整理',failed:'整理失败，稍后重试',budget_wait:'已达到自动维护预算'}[x.state]||x.state)).join('；');
   for(const hit of data.page.items||[]){const row=el(list,'article',undefined,'conversation-summary');if(hit.event){el(row,'h3','原始消息');window.WechatDialogs.text(row,hit.event.text,{title:'原始消息预览'});el(row,'p',hit.event.at+' · '+hit.event.sender,'hint');button(row,'查看完整来源',()=>source(scope,hit.event.id));continue}
    const m=hit.entry;el(row,'h3',(names[m.kind]||m.kind)+' · '+memoryState(m)+(m.pinned?' · 已固定':''));window.WechatDialogs.text(row,m.detail,{title:'完整长期记忆'});el(row,'p',person(m.subject)+' · 版本 '+m.version+(m.inference?' · 尚未确定':'')+(m.manual?' · 主动笔记':''),'hint');relations(row,m);
    const refreshAll=()=>{onChanged?.();load(true)},actions=el(row,'div',undefined,'dialog-actions');for(const ref of m.sources||[])button(actions,'查看来源',()=>source(scope,ref.event_id));button(actions,'完整记忆与修订',async()=>{try{showMemoryDetail('记忆来源与版本',await api('/api/memory/'+scope+'/entries/'+m.id))}catch(e){status.textContent=e.message}});if(!(m.assertions||[]).length)button(actions,'更正',()=>correct(scope,m,refreshAll));
    for(const [action,label]of [[m.pinned?'unpin':'pin',m.pinned?'取消固定':'固定'],['forget','遗忘']])button(actions,label,async()=>{if(action==='forget'){const confirm=window.WechatDialogs.open({title:'遗忘这条记忆',subtitle:'该记忆及其来源不再用于自动召回；只读历史归档仍保留。'});button(confirm.body,'确认遗忘',async()=>{await change();confirm.close()})}else await change();async function change(){try{await api('/api/memory/'+scope+'/entries/'+m.id,{method:'POST',json:{action,expected_revision:m.version}});refreshAll()}catch(e){status.textContent=e.message}}});
   }cursor=data.page.next_cursor||'';more.hidden=!data.page.has_more;if(!list.children.length)el(list,'p','没有匹配记忆。自动整理完成后可在这里检查来源。','empty');
  }catch(e){if(!current.signal.aborted&&p.alive())status.textContent=e.message}finally{if(controller===current){busy=false;more.disabled=false;p.body.removeAttribute('aria-busy')}}}
  form.addEventListener('submit',e=>{e.preventDefault();load(true)});p.dialog.addEventListener('close',()=>{controller?.abort();experienceView.dispose();reflectionView.dispose()},{once:true})
 }
 async function settings(){const p=window.WechatDialogs.open({title:'记忆设置'}),status=el(p.body,'p','正在读取…','hint');status.setAttribute('role','status');try{const data=await api('/api/memory/services');if(!p.alive())return;status.textContent='自动整理使用配置的维护模型，留空时沿用聊天模型。向量服务配置后才启用语义召回。';const form=el(p.body,'form',undefined,'memory-settings-form'),s=data.settings,e=data.embedding,m=data.maintenance,inputs={};
  const enabled=field(form,'启用自动记忆','','checkbox');enabled.checked=s.enabled;
  for(const [key,label]of [['batch_messages','批量整理消息数'],['idle_seconds','空闲整理秒数'],['daily_tokens','自动维护每日 Token 预算（0 为不限额）'],['recent_tokens','近期对话自动注入 Token'],['recall_tokens','长期记忆自动注入 Token'],['continuity_tokens','固定与连续性额度'],['context_window_tokens','模型上下文窗口（0 为未设置）'],['output_reserve_tokens','输出预留 Token']]){inputs[key]=field(form,label,s[key],'number');inputs[key].min='0';inputs[key].required=true}
  el(form,'h3','回复依据');const audit=s.reply_audit||{enabled:true,snapshots:true,retention_days:7},auditEnabled=field(form,'保留回复依据','','checkbox'),auditSnapshots=field(form,'故障或需要复核时保留脱敏快照','','checkbox'),auditDays=field(form,'保留天数',audit.retention_days,'number');auditEnabled.checked=audit.enabled;auditSnapshots.checked=audit.snapshots;auditDays.min='1';auditDays.required=true;
  el(form,'h3','语义检索');const endpoint=field(form,'嵌入服务地址',e.base_url),model=field(form,'嵌入模型',e.model),revision=field(form,'嵌入模型版本',e.revision||''),dimensions=field(form,'指定维度（0 使用服务默认）',e.dimensions||0,'number'),key=field(form,data.embedding_key_configured?'嵌入密钥（已保存，留空保留）':'嵌入密钥','','password'),clearKey=field(form,'清除已保存嵌入密钥','','checkbox');
  el(form,'h3','独立维护模型');const me=field(form,'维护服务地址',m.base_url),mm=field(form,'维护模型',m.model),mk=field(form,data.maintenance_key_configured?'维护密钥（已保存，留空保留）':'维护密钥','','password'),mc=field(form,'清除已保存维护密钥','','checkbox'),provider=el(el(form,'label','维护服务类型'),'select');for(const [value,label]of [['openai-compatible','OpenAI 兼容'],['deepseek','DeepSeek']]){const o=el(provider,'option',label);o.value=value}provider.value=m.provider||'openai-compatible';
  const save=el(form,'button','保存记忆设置','primary');save.type='submit';form.addEventListener('submit',async event=>{event.preventDefault();if(!form.reportValidity())return;save.disabled=true;try{const options={...s,enabled:enabled.checked,reply_audit:{enabled:auditEnabled.checked,snapshots:auditSnapshots.checked,retention_days:Number(auditDays.value)}};for(const [k,input]of Object.entries(inputs))options[k]=Number(input.value);await api('/api/memory/services',{method:'POST',json:{settings:options,embedding:{base_url:endpoint.value.trim(),model:model.value.trim(),revision:revision.value.trim(),dimensions:Number(dimensions.value),api_key:key.value},maintenance:{base_url:me.value.trim(),model:mm.value.trim(),api_key:mk.value,provider:provider.value},clear_embedding_key:clearKey.checked,clear_maintenance_key:mc.checked}});key.value='';mk.value='';status.textContent='已保存，后续维护与召回使用新设置。'}catch(e){status.textContent=e.message}finally{save.disabled=false}});
  button(p.body,'测试已保存的嵌入服务',async()=>{try{const result=await api('/api/memory/embedding/test',{method:'POST',json:{}});status.textContent='合成测试通过，维度 '+result.dimensions+'，'+(result.reported?'提供商报告':'未报告')+'用量。'}catch(e){status.textContent=e.message}})
 }catch(e){if(p.alive())status.textContent=e.message}}
 function mount(parent,scope,onChanged){if(scope)button(parent,'会话记忆',()=>open(scope,onChanged))}
 const host=document.querySelector('#wechat');if(host){const card=el(host,'section',undefined,'card');el(card,'h2','记忆与上下文');el(card,'p','设置自动记忆，查看整理进度。','hint');button(card,'记忆设置',settings);button(card,'整理进度',async()=>{try{showMemoryDetail('记忆整理进度',await api('/api/memory/status'))}catch(e){window.WechatAdmin.notice(e.message,true)}})}
 window.WechatMemory={mount,open,settings,source};
})();
