(() => {
 'use strict';
 const $=s=>document.querySelector(s),query=new URL(location.href).searchParams;
 let selected=query.get('account')||'',rows=[],busy=false,primary='',removed=[];
 const names=new Map([['filehelper','文件传输助手']]);
 function remember(id,...candidates){if(!id)return;const name=candidates.find(x=>typeof x==='string'&&x.trim()&&x!==id&&!/^wxid_|@chatroom$/.test(x));if(name)names.set(id,name.trim());}
 function display(id,fallback){return names.get(id)||fallback||id||'名称未同步';}
 function url(path){const target=new URL(path,location.origin);if(target.origin===location.origin&&target.pathname.startsWith('/api/')&&selected)target.searchParams.set('account',selected);return target.pathname+target.search+target.hash;}
 function choose(id,section){if(!id||id===selected&&!section)return;const target=new URL(location.href);target.searchParams.set('account',id);if(section)target.hash=section;location.assign(target.href);}
 const connections={online:'微信在线',reconnecting:'正在重连',disconnected:'未登录',unavailable:'服务不可用'},bots={running:'Agent 运行中',waiting_login:'Agent 等待登录',disabled:'Agent 已暂停',error:'Agent 异常',draining:'更新收尾中',drained:'等待更新'};
 function render(){
  const select=$('#account-select'),list=$('#account-list');if(!select||!list)return;
  const active=rows.find(x=>x.id===selected);select.replaceChildren();list.replaceChildren();
  rows.forEach((item,index)=>{
   const suffix=rows.filter(x=>x.name===item.name).length>1?' · '+(index+1):'';
   const option=document.createElement('option');option.value=item.id;option.textContent=item.name+suffix;option.selected=item.id===selected;select.append(option);
   const row=document.createElement('article');row.className='account-row'+(item.id===selected?' selected':'');
   const info=document.createElement('div');info.className='account-info';const title=document.createElement('strong');title.textContent=item.name+suffix;
   const status=document.createElement('span');status.className='account-status';status.textContent=(connections[item.native?.connection]||'状态未知')+' · '+(bots[item.bot?.state]||item.bot?.state||'Agent 已暂停');
   const detail=document.createElement('div');detail.className='account-note';if(item.label&&item.label!==item.name){const note=document.createElement('small');note.textContent='备注：'+item.label;detail.append(note)}
   if(item.load_error){const error=document.createElement('span');error.className='error';error.textContent=item.load_error;info.append(error)}info.append(title,status,detail);const button=document.createElement('button');button.type='button';button.className='quiet';button.textContent=item.id===selected?'当前管理':'管理此账号';button.disabled=item.id===selected||!!item.load_error;button.addEventListener('click',()=>choose(item.id,'wechat'));const actions=document.createElement('div');actions.className='button-row';actions.append(button);if(!item.primary){actions.append(actionButton('修改备注',()=>rename(item)),actionButton('移除账号',()=>remove(item)))}row.append(info,actions);list.append(row);
  });
  const archive=$('#removed-accounts');archive.replaceChildren();archive.hidden=!removed.length;for(const item of removed){const row=document.createElement('div');row.className='button-row';const name=document.createElement('span');name.textContent=(item.label||'账号 '+item.id.slice(-6))+(item.deleting?' · 删除未完成':' · 已移除');row.append(name);if(!item.deleting)row.append(actionButton('恢复账号',()=>manage(item,'restore')));row.append(actionButton(item.deleting?'继续删除':'删除数据',()=>remove(item)));archive.append(row)}
  $('#account-current-name').textContent=active?.name||'微信账号';
  const metric=$('#account-state');if(metric&&active){metric.textContent=active.name;metric.title=active.id}
 }
 async function load(){
  const data=await window.WechatAdmin.api('/api/accounts');primary=data.primary;removed=(data.items||[]).filter(x=>x.removed);rows=(data.items||[]).filter(x=>!x.removed);
  if(!selected)selected=data.primary;else if(!rows.some(x=>x.id===selected)){selected=data.primary;const target=new URL(location.href);target.searchParams.set('account',selected);history.replaceState(null,'',target.href);window.WechatAdmin.notice('原先选择的账号不存在，已切换到当前账号。')}
  $('#add-account').disabled=!data.supported;
  $('#accounts-state').textContent=data.directory_error||`${rows.length} 个账号 · 各自独立处理会话`;
  render();return data;
 }
 async function refresh(){if(busy||!window.WechatAdmin?.isAuthenticated())return;busy=true;try{await load()}catch(error){window.WechatAdmin.notice(error.message,true)}finally{busy=false}}
 function actionButton(label,action){const b=document.createElement('button');b.type='button';b.className='quiet';b.textContent=label;b.addEventListener('click',async()=>{b.disabled=true;try{await action()}catch(e){window.WechatAdmin.notice(e.message,true)}finally{b.disabled=false}});return b}
 async function manage(item,action,label){await window.WechatAdmin.api('/api/accounts/'+encodeURIComponent(item.id)+'/manage',{method:'POST',json:{action,label}});if((action==='remove'||action==='purge')&&selected===item.id){choose(primary,'wechat');return}await load()}
 async function rename(item){const label=prompt('账号备注（留空清除）',item.label||'');if(label!==null)await manage(item,'rename',label)}
 function remove(item){
  const popup=window.WechatDialogs.open({title:'是否删除该账号的所有数据？',subtitle:item.label||item.name||('账号 '+item.id.slice(-6))});
  popup.dialog.classList.add('account-removal');
  const info=document.createElement('p');info.textContent='移除时会停止该账号的 Agent 并退出微信。仅移除可恢复账号资料，同时删除会永久清除本应用管理的配置、记忆、任务和私有工作区。桌面服务停止时清理本机微信缓存，外部挂载的资料保留。';
  const status=document.createElement('p');status.setAttribute('role','status');status.hidden=true;
  const actions=document.createElement('div');actions.className='button-row';
  let pending=false;
  const submit=async(action)=>{if(pending)return;pending=true;for(const b of actions.children)b.disabled=true;
   status.hidden=false;status.textContent='正在处理…';
   try{if(action==='purge'||!item.removed)await manage(item,action);popup.close()}
   catch(e){status.textContent=e.message;for(const b of actions.children)b.disabled=false}
   finally{pending=false}
  };
  const keep=actionButton('仅移除账号',()=>submit('remove')),purge=actionButton('同时删除数据',()=>submit('purge'));purge.className='danger-text';
  if(!item.deleting)actions.append(keep);actions.append(purge);popup.body.append(info,status,actions);
 }
 async function create(){const button=$('#add-account');button.disabled=true;try{const item=await window.WechatAdmin.api('/api/accounts',{method:'POST',json:{label:''}});choose(item.id,'wechat')}catch(error){window.WechatAdmin.notice(error.message,true);button.disabled=false}}
 window.WechatAccountContext={get id(){return selected},url};
 window.WechatNames={display,remember};
 function update(status){const item=rows.find(x=>x.id===selected);if(!item)return;item.native=status.native;item.bot=status.bot;if(status.account_name)item.name=item.label||status.account_name;render()}
 window.WechatAccounts={load,refresh,name:()=>rows.find(x=>x.id===selected)?.name||'',render,update};
 document.addEventListener('DOMContentLoaded',()=>{
  $('#account-select').addEventListener('change',event=>choose(event.target.value));$('#add-account').addEventListener('click',create);$('#refresh-accounts').addEventListener('click',refresh);
 });
 window.addEventListener('wechat-admin:navigate',event=>{if(event.detail.section==='wechat')refresh()});
 window.addEventListener('wechat-admin:scopes',event=>{for(const item of event.detail.scopes||[])remember(item.thread,item.label)});
 window.addEventListener('wechat-admin:config-changed',()=>{for(const item of window.WechatAdmin?.getConfig()?.group_rules||[])remember(item.id,item.name)});
})();
