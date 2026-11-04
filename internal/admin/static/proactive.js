(() => {
 'use strict';
 const $=s=>document.querySelector(s), state={selected:new Set(),rules:new Map(),directory:new Map(),busy:false};
 const reasons={ready:'可安排判断',not_selected:'未选择',character_disabled:'角色未开启',proactive_permission_denied:'主动问候权限未开启',text_reply_permission_denied:'文本回复未开启',quiet_hours:'安静时段',proactive_daily_budget:'今日总机会已用尽',target_daily_budget:'此目标今日机会已用尽',no_reliable_context:'等待积累可用记忆',context_expired:'上下文已过期',idle_wait:'等待旧版调度时间',scheduled_wait:'等待下次自主评估'};
 const api=(...a)=>window.WechatAdmin.api(...a);
 function el(parent,tag,text,cls){const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;parent.append(n);return n}
 function allowed(){const c=window.WechatAdmin?.getConfig()||{},list=new Map();for(const r of c.group_rules||[])list.set(r.id,{id:r.id,name:r.name||r.id,kind:'group'});if($('#private-enabled').checked)for(const id of $('#direct-ids').value.split(/\r?\n/).map(s=>s.trim()).filter(Boolean))list.set(id,{id,name:id,kind:'friend'});if($('#filehelper-enabled').checked)list.set('filehelper',{id:'filehelper',name:'文件传输助手',kind:'filehelper'});return list}
 function settings(){return {proactive_threads:[...state.selected],proactive_rules:[...state.selected].map(thread=>state.rules.get(thread)).filter(Boolean)}}
 function load(c){state.selected=new Set(c.proactive_threads||[]);state.rules=new Map((c.proactive_rules||[]).map(r=>[r.thread,{...r}]));render()}
 function ruleFor(id){return state.rules.get(id)||{thread:id,idle_minutes:window.WechatAdmin?.getConfig()?.companion?.proactive_idle_minutes||60,max_per_day:1}}
 function render(){const box=$('#proactive-targets');if(!box)return;box.replaceChildren();const choices=allowed(),query=$('#proactive-search').value.trim().toLowerCase();for(const id of state.selected)if(!choices.has(id))choices.set(id,{id,name:state.directory.get(id)?.name||id,kind:'unavailable'});
  let matches=0;
  for(const [kind,title] of [['group','群聊'],['friend','好友私信'],['filehelper','文件传输助手'],['unavailable','已失去回复授权的目标']]){
   const rows=[...choices.values()].filter(x=>x.kind===kind).map(x=>({...x,name:state.directory.get(x.id)?.name||x.name})).filter(x=>!query||[x.name,x.id].join(' ').toLowerCase().includes(query));if(!rows.length)continue;
   const field=el(box,'fieldset',undefined,'proactive-group');el(field,'legend',title);
   for(const item of rows){matches++;const selected=state.selected.has(item.id),row=el(field,'div',undefined,'proactive-target');row.dataset.thread=item.id;const label=el(row,'label',undefined,'proactive-choice'),check=el(label,'input');check.type='checkbox';check.checked=selected;check.setAttribute('aria-label',item.name+' 自主续聊');const text=el(label,'span');el(text,'strong',item.name);el(text,'small',item.id);
    check.addEventListener('change',()=>{if(check.checked)state.selected.add(item.id);else{state.selected.delete(item.id);state.rules.delete(item.id)}window.WechatAdmin.markDirty();render();const replacement=[...document.querySelectorAll('.proactive-target')].find(row=>row.dataset.thread===item.id)?.querySelector('input[type=checkbox]');(replacement||$('#proactive-search')).focus({preventScroll:true})});
    const status=state.directory.get(item.id)?.state;el(row,'p',kind==='unavailable'?'请取消选择或恢复回复授权后保存':selected?(status?.reason==='not_selected'?'保存后更新调度状态':reasons[status?.reason]||'保存后更新调度状态')+(status?' · 评估 '+status.today+' 次 · 安静 '+(status.outcomes?.silent||0)+' 次 · 已接受分享 '+(status.outcomes?.accepted||0)+' 轮'+(status.outcomes?.failed?' · 异常 '+status.outcomes.failed+' 次':'')+(status.outcomes?.skipped?' · 跳过 '+status.outcomes.skipped+' 次':''):''):'未启用','hint proactive-target-state');
   }
  }
  if(!matches)el(box,'p',query?'没有匹配的已授权目标。':'暂无已授权目标，请先添加群聊或私信回复规则。','empty');
 }
 async function refresh(){if(state.busy||!window.WechatAdmin?.isAuthenticated())return;state.busy=true;$('#proactive-refresh').disabled=true;try{const data=await api('/api/companion/targets');state.directory=new Map((data.items||[]).map(x=>[x.id,x]));$('#proactive-state').textContent='今日已评估 '+data.today+' 次；不设每日硬额度，选择安静不会耗尽后续机会。';if(!$('#proactive-targets [aria-invalid="true"]')&&!$('#proactive-targets').contains(document.activeElement))render()}catch(e){$('#proactive-state').textContent='读取目标失败：'+e.message}finally{state.busy=false;$('#proactive-refresh').disabled=false}}

 refresh=window.WechatRefresh.join(refresh);
 $('#proactive-search').addEventListener('input',render);window.WechatRefresh.bind($('#proactive-refresh'),refresh);
 for(const id of ['private-enabled','filehelper-enabled','direct-ids'])$('#'+id).addEventListener('input',render);
 window.addEventListener('wechat-admin:config-changed',()=>{render();if($('#permissions').classList.contains('active-page'))refresh()});
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='permissions')refresh()});
 window.addEventListener('wechat-admin:logout',()=>{state.selected.clear();state.rules.clear();state.directory.clear();$('#proactive-targets').replaceChildren();$('#proactive-state').textContent='';$('#proactive-search').value=''});
 window.WechatProactive={settings,load,render};
})();
