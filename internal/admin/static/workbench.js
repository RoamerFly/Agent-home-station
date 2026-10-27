/* A navigation/status canvas over the existing Agent, not a second executor. */
(() => {
 'use strict';
 const $=s=>document.querySelector(s),key='wechat_agent_canvas_v1',W=198,H=116;
 const nodes=[
  ['status','状态与费用','运行与消耗',40,50,['overview']],
  ['receive','微信接入','收到消息',40,240,['wechat']],
  ['rules','会话规则','群聊与私信',300,240,['rules','catalog']],
  ['observe','图片与视频','媒体观察',300,50,['permissions']],
  ['model','语言与语音模型','理解与表达',560,50,['provider']],
  ['agent','Agent','自主判断与行动',560,240,['agent','companion']],
  ['memory','会话记忆','上下文与摘要',560,450,['memory']],
  ['persona','角色','人设与相处方式',40,450,['companion']],
  ['tools','工具与插件','行动与反馈',820,50,['plugins','mcp','permissions','mobile']],
  ['send','发送与回执','动态延迟与交付',1080,240,['tools','logs']],
  ['retry','核对与补发','等待、核对、恢复',820,450,['logs']],
  ['silent','选择安静','本轮无需回复',1080,450,['companion']]
 ].map(([id,title,subtitle,x,y,pages])=>({id,title,subtitle,x,y,pages}));
 const links=[['receive','rules','接收'],['rules','observe','有媒体'],['rules','agent','对话'],['observe','agent','实际观察'],['model','agent','模型'],['persona','memory','角色与关系'],['memory','agent','上下文'],['agent','tools','调用'],['tools','agent','真实结果'],['agent','send','回复'],['agent','silent','保持安静'],['send','retry','未确认'],['retry','send','核对后续发']];
 const titles={overview:'状态与费用',wechat:'微信连接',rules:'回复规则',catalog:'成员目录',provider:'模型与语音',agent:'任务与文档',companion:'角色',memory:'会话记忆',plugins:'插件',mcp:'联网与 MCP',permissions:'权限',mobile:'手机探索',tools:'手动发送',logs:'执行回合'};
 const state={pan:{x:20,y:20},scale:1,positions:{},selected:'',trace:'',parked:null,active:false,skipClick:false,pluginCount:null};
 let gesture=null,timer=null,restored=false;
 const compact=()=>matchMedia('(max-width:520px)').matches;
 try{const saved=JSON.parse(localStorage.getItem(key)||'null');if(saved&&saved.version===1){restored=true;for(const n of nodes){const p=saved.positions?.[n.id];if(p&&[p.x,p.y].every(Number.isFinite)&&Math.abs(p.x)<5000&&Math.abs(p.y)<5000)state.positions[n.id]=p}if([saved.scale,saved.pan?.x,saved.pan?.y].every(Number.isFinite)){state.scale=Math.max(.15,Math.min(1.7,saved.scale));state.pan=saved.pan}}}catch{}
 for(const n of nodes)state.positions[n.id]||={x:n.x,y:n.y};
 const el=(tag,cls,text)=>{const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e};
 function save(){try{localStorage.setItem(key,JSON.stringify({version:1,positions:state.positions,pan:state.pan,scale:state.scale}))}catch{}}
 function size(){if(state.active){const top=$('#flow-workspace').getBoundingClientRect().top;$('#flow-workspace').style.height=Math.max(360,innerHeight-top-48)+'px'}}
 function transform(){ $('#flow-world').style.transform=`translate(${state.pan.x}px,${state.pan.y}px) scale(${state.scale})`;$('#flow-zoom').textContent=Math.round(state.scale*100)+'%'; }
 function edges(){const svg=$('#flow-edges');svg.replaceChildren();const ns='http://www.w3.org/2000/svg',defs=document.createElementNS(ns,'defs'),marker=document.createElementNS(ns,'marker'),tip=document.createElementNS(ns,'path');marker.id='flow-edge-arrow';for(const [k,v]of Object.entries({viewBox:'0 0 10 10',refX:'9',refY:'5',markerWidth:'6',markerHeight:'6',orient:'auto'}))marker.setAttribute(k,v);tip.setAttribute('d','M0,0 L10,5 L0,10 Z');tip.style.fill='var(--field-line)';tip.style.stroke='none';marker.append(tip);defs.append(marker);svg.append(defs);for(const [from,to,label]of links){const a=state.positions[from],b=state.positions[to],forward=b.x>a.x;
   const x1=a.x+(forward?W:W/2),y1=a.y+(forward?H/2:H),x2=b.x+(forward?0:W/2),y2=b.y+(forward?H/2:0),bend=Math.max(55,Math.abs(x2-x1)*.46);
   const path=document.createElementNS(ns,'path');path.setAttribute('d',`M${x1},${y1} C${x1+(forward?bend:40)},${y1} ${x2-(forward?bend:40)},${y2} ${x2},${y2}`);path.setAttribute('marker-end','url(#flow-edge-arrow)');path.dataset.from=from;path.dataset.to=to;const title=document.createElementNS(ns,'title');title.textContent=label;path.append(title);svg.append(path);
  }highlightEdges()}
 function paintPositions(){for(const n of nodes){const p=state.positions[n.id],e=document.getElementById('flow-node-'+n.id);e.style.left=p.x+'px';e.style.top=p.y+'px'}edges();transform()}
 function fit(){const v=$('#flow-viewport');if(compact()){v.scrollTop=0;return}const xs=Object.values(state.positions).map(p=>p.x),ys=Object.values(state.positions).map(p=>p.y),left=Math.min(...xs),top=Math.min(...ys),width=Math.max(...xs)+W-left,height=Math.max(...ys)+H-top;
  state.scale=Math.max(.15,Math.min(1.1,(v.clientWidth-32)/width,(v.clientHeight-60)/height));state.pan={x:(v.clientWidth-width*state.scale)/2-left*state.scale,y:30-top*state.scale};transform();save();
 }
 function zoom(factor,x,y){const v=$('#flow-viewport');x??=v.clientWidth/2;y??=v.clientHeight/2;const old=state.scale,next=Math.max(.15,Math.min(1.7,old*factor));state.pan={x:x-(x-state.pan.x)*next/old,y:y-(y-state.pan.y)*next/old};state.scale=next;transform();save()}
 function restore(){const p=state.parked;if(!p)return;p.page.classList.remove('active-page');p.page.classList.remove('flow-embedded');p.marker.replaceWith(p.page);state.parked=null;$('#flow-inspector-body').replaceChildren()}
 function highlightEdges(){for(const path of $('#flow-edges').querySelectorAll('path[data-from]'))path.dataset.emphasis=!state.selected?'normal':path.dataset.from===state.selected||path.dataset.to===state.selected?'active':'muted'}
 function closeInspector(){restore();$('#flow-inspector').hidden=true;state.selected='';for(const n of nodes)document.getElementById('flow-node-'+n.id).setAttribute('aria-pressed','false');highlightEdges()}
 function showPage(id){
  for(const button of $('#flow-inspector-tabs').children)button.setAttribute('aria-pressed',String(button.dataset.page===id));if(state.parked?.page.id===id)return;restore();const page=document.getElementById(id);if(!page||id==='workbench')return;
  const marker=document.createComment('canvas inspector: '+id);page.before(marker);state.parked={page,marker};page.classList.add('active-page','flow-embedded');$('#flow-inspector-body').append(page);
  window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section:id,embedded:true}}));
  for(const button of $('#flow-inspector-tabs').children)button.setAttribute('aria-pressed',String(button.dataset.page===id));
 }
 function inspect(id,pageID){
  const n=nodes.find(n=>n.id===id)||nodes.find(n=>n.pages.includes(id));if(!n)return;
  state.selected=n.id;$('#flow-inspector').hidden=false;$('#flow-inspector-title').textContent=n.title;$('#flow-inspector-subtitle').textContent=n.subtitle;
  for(const item of nodes)document.getElementById('flow-node-'+item.id).setAttribute('aria-pressed',String(item.id===n.id));
  highlightEdges();
  const tabs=$('#flow-inspector-tabs');tabs.replaceChildren();for(const id of n.pages){const b=el('button','quiet',titles[id]);b.type='button';b.dataset.page=id;b.addEventListener('click',()=>showPage(id));tabs.append(b)}
  showPage(pageID||n.pages[0]);
 }
 function selectedRound(){return window.WechatActivity?.rounds().find(r=>r.id===state.trace)}
 function status(){
  const cfg=window.WechatAdmin.getConfig()||{},runtime=window.WechatAdmin.getStatus()||{},scopes=window.WechatAdmin.getScopes(),round=selectedRound();
  const hints={status:runtime.bot?.state==='running'?'Agent 运行中':'查看状态与用量',receive:runtime.native?.authenticated?'微信已登录':'微信未登录',rules:(cfg.group_rules?.length||0)+' 个群聊规则',observe:(cfg.behavior?.read_images?'图片已启用':'图片已关闭')+' / '+(cfg.behavior?.read_videos?'视频已启用':'视频已关闭'),model:cfg.model||'模型尚未配置',agent:'自主选择回复、工具或安静',memory:scopes.length+' 个会话',persona:cfg.companion?.name||'角色尚未配置',tools:state.pluginCount==null?'按当前权限使用工具':state.pluginCount+' 个已安装插件',send:cfg.behavior?.random_delay?'动态延迟已启用':'查看发送与回执',retry:cfg.behavior?.retry_uncertain?'延迟核对已启用':'查看核对与补发',silent:'由模型判断是否参与'};
  const evidence={};
  if(round){
   evidence.receive=round.received.length?round.received.length+' 条接收记录':'';
   const observed=round.entries.filter(x=>x.event==='media_understood').length;if(observed)evidence.observe='已取得 '+observed+' 项观察';
   if(round.tools)evidence.tools=round.tools+' 次工具操作';
   if(round.entries.some(x=>x.event==='character_decision'||x.event==='model_failed'))evidence.agent=window.WechatActivity.status(round);
   if(round.sent.length)evidence.send=round.sent.filter(x=>['accepted','observed'].includes(x.status)).length+'/'+round.sent.length+' 条已接受';
   if(round.entries.some(x=>x.retry_attempts||/^delivery_|media_retry/.test(x.event)))evidence.retry='存在核对 / 补发步骤';
   if(round.status==='silent')evidence.silent='本轮选择安静';
  }
  for(const n of nodes){const e=document.getElementById('flow-node-'+n.id),value=evidence[n.id]||hints[n.id];e.querySelector('.flow-node-state').textContent=value;e.title=n.title+'：'+value;e.dataset.evidence=evidence[n.id]?'observed':'';e.dataset.failed=String(!!round&&n.id==='agent'&&round.status==='failed')}
  $('#flow-trace-open').hidden=!round;
 }
 function traces(){const select=$('#flow-trace'),previous=state.trace;select.replaceChildren();const first=el('option','','流程概览');first.value='';select.append(first);
  for(const r of (window.WechatActivity?.rounds()||[]).filter(r=>!r.system).slice(0,40)){const option=el('option','',r.source+' · '+window.WechatActivity.time(r.time)+' · '+window.WechatActivity.status(r));option.value=r.id;select.append(option)}select.value=[...select.options].some(x=>x.value===previous)?previous:'';state.trace=select.value;status();
 }
 function init(){
  const container=$('#flow-nodes');for(const n of nodes){const button=el('button','flow-node');button.type='button';button.id='flow-node-'+n.id;button.dataset.node=n.id;button.setAttribute('aria-label',n.title+'，点击配置；方向键移动节点');button.setAttribute('aria-pressed','false');button.append(el('small','flow-node-category',n.subtitle),el('strong','flow-node-title',n.title),el('span','flow-node-state','正在读取…'));container.append(button);
   button.addEventListener('click',()=>{if(state.skipClick){state.skipClick=false;return}inspect(n.id)});
   button.addEventListener('keydown',e=>{if(compact())return;const offsets={ArrowLeft:[-20,0],ArrowRight:[20,0],ArrowUp:[0,-20],ArrowDown:[0,20]};if(offsets[e.key]){e.preventDefault();const [x,y]=offsets[e.key];state.positions[n.id].x+=x;state.positions[n.id].y+=y;paintPositions();save()}});
  }
  const jump=el('select','flow-jump');jump.id='flow-jump';jump.setAttribute('aria-label','选择节点');const hint=el('option','','选择节点…');hint.value='';jump.append(hint);for(const n of nodes){const option=el('option','',n.title);option.value=n.id;jump.append(option)}jump.addEventListener('change',()=>{if(jump.value)inspect(jump.value)});$('#flow-trace').before(jump);
  const move=el('select','flow-move');move.id='flow-move';move.setAttribute('aria-label','不用拖动移动所选节点');for(const [value,text]of [['','移动节点…'],['left','向左移动'],['right','向右移动'],['up','向上移动'],['down','向下移动']]){const option=el('option','',text);option.value=value;move.append(option)}move.addEventListener('change',()=>{const offset={left:[-30,0],right:[30,0],up:[0,-30],down:[0,30]}[move.value];if(offset&&state.selected){state.positions[state.selected].x+=offset[0];state.positions[state.selected].y+=offset[1];paintPositions();save()}move.value=''});$('#flow-inspector-close').before(move);
  const v=$('#flow-viewport');v.addEventListener('pointerdown',e=>{if(compact()||e.button!==0)return;const n=e.target.closest('.flow-node'),p=n?state.positions[n.dataset.node]:state.pan;gesture={pointer:e.pointerId,id:n?.dataset.node,x:e.clientX,y:e.clientY,start:{...p},moved:false,capture:n||v};(n||v).setPointerCapture(e.pointerId)});
  v.addEventListener('pointermove',e=>{if(!gesture||gesture.pointer!==e.pointerId)return;const dx=e.clientX-gesture.x,dy=e.clientY-gesture.y;if(Math.hypot(dx,dy)>5)gesture.moved=true;if(!gesture.moved)return;const p={x:gesture.start.x+dx/(gesture.id?state.scale:1),y:gesture.start.y+dy/(gesture.id?state.scale:1)};if(gesture.id)state.positions[gesture.id]=p;else state.pan=p;paintPositions()});
  function release(e){if(!gesture||gesture.pointer!==e.pointerId)return;state.skipClick=gesture.moved;if(gesture.capture.hasPointerCapture(e.pointerId))gesture.capture.releasePointerCapture(e.pointerId);gesture=null;save();setTimeout(()=>{state.skipClick=false},0)}v.addEventListener('pointerup',release);v.addEventListener('pointercancel',release);
  v.addEventListener('wheel',e=>{if(compact()||!e.ctrlKey&&!e.metaKey)return;e.preventDefault();const r=v.getBoundingClientRect();zoom(Math.exp(-e.deltaY*.002),e.clientX-r.left,e.clientY-r.top)},{passive:false});
  v.addEventListener('keydown',e=>{if(e.target!==v)return;if(e.key==='f'||e.key==='F'){e.preventDefault();fit()}if(e.key==='Escape')closeInspector()});
  $('#flow-fit').addEventListener('click',fit);$('#flow-zoom-in').addEventListener('click',()=>zoom(1.2));$('#flow-zoom-out').addEventListener('click',()=>zoom(1/1.2));$('#flow-reset').addEventListener('click',()=>{for(const n of nodes)state.positions[n.id]={x:n.x,y:n.y};paintPositions();fit()});$('#flow-inspector-close').addEventListener('click',closeInspector);
  $('#flow-trace-open').addEventListener('click',()=>{const r=selectedRound();if(r)window.WechatActivity.openRound(r)});
  $('#flow-trace').addEventListener('change',e=>{state.trace=e.target.value;status();const r=selectedRound();$('#flow-notice').textContent=r?'已叠加回合状态：'+r.source+' · '+window.WechatActivity.status(r)+'。点击“回合详情”查看完整过程。':'拖动只调整本浏览器的布局；连线表示现有依赖，Agent 根据实际对话自主行动。'});
  $('#flow-inspector').addEventListener('click',e=>{const link=e.target.closest('[data-section-link]');if(!link)return;e.preventDefault();e.stopImmediatePropagation();inspect(link.dataset.sectionLink,link.dataset.sectionLink)},true);
  paintPositions();status();
 }
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail.embedded)return;state.active=e.detail.section==='workbench';if(timer)clearInterval(timer);timer=null;if(!state.active){closeInspector();return}requestAnimationFrame(()=>{size();if(!restored){fit();restored=true}else transform();traces()});timer=setInterval(status,2500);window.WechatAdmin.api('/api/plugins').then(data=>{state.pluginCount=(data.installed||data.items||[]).length;status()}).catch(()=>{});window.WechatActivity?.refresh()});
 window.addEventListener('wechat-admin:activity-updated',()=>{if(state.active)traces()});window.addEventListener('wechat-admin:activity-pending',()=>{if(state.active)window.WechatActivity.loadPending()});window.addEventListener('wechat-admin:scopes',()=>{if(state.active)status()});
 window.addEventListener('wechat-admin:logout',()=>{state.active=false;clearInterval(timer);closeInspector();state.pluginCount=null});
 window.WechatWorkbench={inspect,isActive:()=>state.active,closeInspector};init();window.addEventListener('resize',size);const observer=new ResizeObserver(size);observer.observe($('.flow-toolbar'));observer.observe($('#notice'));
})();
