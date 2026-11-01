/* Account-scoped package administration. Does not invoke provider or WeChat tools. */
(() => {
 'use strict';
 const $=s=>document.querySelector(s),api=(...a)=>window.WechatAdmin.api(...a);
 const state={catalog:[],installed:[],runtimes:{},retained:new Set(),selected:'',detail:null,epoch:0,busy:false,loadPromise:null,loaded:false,dirty:false,editVersion:0,migrationVersion:0,hostRequest:null};
 const page=$('#plugins'),legacy=page.querySelector('.plugin-workspace'),guide=$('#plugin-guide');
 const el=(tag,text,klass)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(klass)n.className=klass;return n};
 const button=(text,action)=>{const b=el('button',text,'quiet');b.type='button';b.addEventListener('click',action);return b};
 const area=el('div',undefined,'package-workspace'),browser=el('aside',undefined,'panel package-browser'),editor=el('article',undefined,'panel package-editor');
 const list=el('div',undefined,'package-list'),status=el('p','选择服务查看配置、权限和任务。','hint');status.setAttribute('role','status');
 const refreshList=$('#plugins-refresh');
 const head=el('div',undefined,'section-heading');head.append(el('h3','能力目录'),button('迁移旧设置',openMigration),button('导入本机插件',importPackage));browser.append(head,list);editor.append(status);area.append(browser,editor);legacy.before(area);
 // The legacy declaration editor remains mounted for compatibility, but all
 // provider-facing configuration now has one visible home: the package detail
 // or the dedicated Connection & MCP page.
 function showPackages(){area.hidden=false;legacy.hidden=true;if(guide)guide.hidden=true;window.WechatSaveActions?.sync()}
 const account=()=>window.WechatAccountContext?.id||'';
 const context=()=>({id:state.selected,account:account(),epoch:state.epoch});
 const current=scope=>scope.epoch===state.epoch&&scope.id===state.selected&&scope.account===account()&&window.WechatAdmin?.isAuthenticated();
 const requestKey=()=>crypto.randomUUID?.()||Array.from(crypto.getRandomValues(new Uint8Array(16)),byte=>byte.toString(16).padStart(2,'0')).join('');
 const runtimeNames={running:'运行中',starting:'正在启动',stopping:'正在停止',failed:'异常',stopped:'已停止'};
 const names={'network':'访问网络','gpu':'使用 GPU','device':'控制设备','workspace.read':'读取授权工作区','workspace.write':'修改授权工作区','process.execute':'执行受控进程','artifact.read':'读取授权素材','artifact.write':'生成素材','model.invoke':'调用模型'};
 const capabilityGroups=[
  ['语音与音乐',['speech','music']],
  ['媒体与分享',['media-understanding','share-reader','web-search']],
  ['创作',['visual-generation']],
  ['开发与设备',['project-development','mobile-control']],
  ['连接',['external-tools']],
 ];
 const groupFor=id=>capabilityGroups.find(([,ids])=>ids.includes(id))?.[0]||'其他能力';
 function packageStatus(id,installed){
  if(!installed)return state.retained.has(id)?'已卸载 · 可查看保留任务':'未安装';
  if(installed.removed)return '已移除 · 可查看保留任务';
  const runtime=state.runtimes[id]?.state;
  if(runtime==='running'||runtime==='starting'||runtime==='stopping')return runtimeNames[runtime];
  if(runtime==='failed')return '异常 · 请打开检查';
  return installed.enabled?'已启用 · 未运行':'已安装 · 未启用';
 }
 function allPackages(){
  const all=new Map(state.catalog.map(p=>[p.id,p]));
  for(const item of state.installed)all.set(item.package.id,item.package);
  for(const id of state.retained)if(!all.has(id))all.set(id,{id,name:id==='mobile-control'?'设备控制':'已保留插件',status:'retained'});
  return all;
 }
 function capabilityCard(p,installed,klass='package-choice'){
  const card=button('',()=>choose(p.id));card.className=klass;card.setAttribute('aria-pressed',String(state.selected===p.id));
  card.append(el('strong',p.name),el('small',packageStatus(p.id,installed)));
  const next=installed&&!installed.enabled?'下一步：打开后检查配置并手动启动。':state.runtimes[p.id]?.state==='failed'?'下一步：打开查看运行原因。':!installed&&p.status==='ready'?'下一步：导入或迁移后再配置。':!installed?'开发模板，尚不能运行。':'打开配置、权限和任务';
  card.append(el('small',next,'hint'));
  return card;
 }
 function renderOverview(all){
  if(state.selected)return;
  const intro=el('div',undefined,'package-overview-intro'),copy=el('div');copy.append(el('h3','已安装能力'),el('p','选择一项能力即可查看真实安装、启用、运行、配置和任务状态。服务不会因浏览此页面而自动启动。','hint'));const actions=el('div',undefined,'package-overview-actions');actions.append(button('迁移旧设置',openMigration),button('导入本机插件',importPackage));intro.append(copy,actions);
  const grid=el('div',undefined,'capability-overview-grid'),used=new Set();
  for(const [title,ids] of capabilityGroups){
   const items=ids.map(id=>[id,all.get(id)]).filter(([,p])=>p);
   if(!items.length)continue;
   const group=el('section',undefined,'capability-overview-group');group.append(el('h4',title));
   for(const [,p] of items){used.add(p.id);group.append(capabilityCard(p,state.installed.find(item=>item.package.id===p.id),'capability-overview-card'))}
   if(title==='连接'){const connections=button('管理 HTTP 与 MCP',()=>window.WechatAdmin.nav('mcp'));connections.className='secondary capability-connections';group.append(connections)}
   grid.append(group);
  }
  const other=[...all.values()].filter(p=>!used.has(p.id));if(other.length){const group=el('section',undefined,'capability-overview-group');group.append(el('h4','其他能力'));for(const p of other)group.append(capabilityCard(p,state.installed.find(item=>item.package.id===p.id),'capability-overview-card'));grid.append(group)}
  if(!grid.children.length)grid.append(el('p','暂无可用插件。可导入本机插件目录，或先查看迁移旧设置。','empty'));
  editor.replaceChildren(intro,grid);window.WechatSaveActions?.sync();
 }
 function dirty(value){state.dirty=value;if(value){state.editVersion++;state.migrationVersion++;for(const check of document.querySelectorAll('[data-migration-confirm]')){check.checked=false;const action=check.closest('.package-migration-step')?.querySelector('button.primary');if(action)action.disabled=true}}window.WechatAdmin.pluginPackageDirty=value}
 function discard(){return !state.dirty||confirm('有未保存的插件配置，确定放弃？')}
 function renderList(){
  area.classList.toggle('package-overview',!state.selected);list.replaceChildren();const all=allPackages(),used=new Set();
  for(const [title,ids] of capabilityGroups){
   const items=ids.map(id=>all.get(id)).filter(Boolean);if(!items.length)continue;for(const p of items)used.add(p.id);
   const group=el('section',undefined,'package-group');group.append(el('h4',title));for(const p of items)group.append(capabilityCard(p,state.installed.find(item=>item.package.id===p.id)));list.append(group);
  }
  const other=[...all.values()].filter(p=>!used.has(p.id));if(other.length){const group=el('section',undefined,'package-group');group.append(el('h4','其他能力'));for(const p of other)group.append(capabilityCard(p,state.installed.find(item=>item.package.id===p.id)));list.append(group)}
  if(!all.size)list.append(el('p','暂无服务插件，可导入本机插件目录。','empty'));
  renderOverview(all);
 }
 async function load(){
  if(!window.WechatAdmin?.isAuthenticated())return;if(state.loadPromise)return state.loadPromise;state.busy=true;const epoch=state.epoch,owner=account();
  const pending=(async()=>{try{const data=await api('/api/plugin-packages');if(epoch!==state.epoch||owner!==account())return;state.catalog=data.catalog||[];state.installed=data.installed||[];state.runtimes=data.runtimes||{};state.retained=new Set(data.retained_ids||[]);state.loaded=true;renderList()}
  catch(e){if(epoch===state.epoch&&owner===account())status.textContent=e.message}})();state.loadPromise=pending;try{return await pending}finally{if(state.loadPromise===pending){state.busy=false;state.loadPromise=null}}
 }
 async function choose(id){
  if(id===state.selected&&state.detail)return;if(!discard())return;
  const epoch=++state.epoch,owner=account();state.selected=id;state.detail=null;dirty(false);renderList();editor.replaceChildren(status);status.textContent='正在读取插件…';
  const installed=state.installed.find(x=>x.package.id===id),p=installed?.package||state.catalog.find(x=>x.id===id);
  if(!installed&&!state.retained.has(id)){status.textContent=p?.status==='ready'?'此插件尚未安装。导入并确认权限后可配置。':'此开发模板尚不能运行。';editor.prepend(el('h3',p?.name||id));return}
  try{const d=await api('/api/plugin-packages/'+encodeURIComponent(id));if(epoch!==state.epoch||owner!==account())return;state.detail=d;renderDetail(d)}catch(e){if(epoch===state.epoch&&owner===account())status.textContent=e.message}
 }
 function renderDetail(d){
  if(state.selected==='mobile-control'&&d.host_data){renderHostDetail(d);return}
  const p=d.installed.package,heading=el('div',undefined,'section-heading'),runtime=d.runtime||{state:'stopped'};heading.append(el('h3',p.name));
  if(!d.installed.removed){const stop=d.installed.enabled||runtime.state==='running'||runtime.state==='stopping';const action=button(stop?'停止插件':'启动插件',()=>runtimeAction(stop?'stop':'start'));action.dataset.pluginRuntimeAction=stop?'stop':'start';action.disabled=!stop&&!d.runtime_connected;heading.append(action)}
  editor.replaceChildren(heading,status);status.textContent=d.runtime_connected?(runtimeNames[runtime.state]||'状态待核对')+(d.installed.enabled?'；停止后可修改配置。':'；保存配置后可启动。'):'执行环境未配置；可先保存配置，安装不会自动启动。';
  const permissions=el('p',(d.installed.permissions||[]).map(x=>names[x]||x).join('、')||'未授予额外权限','hint');editor.append(permissions);
  const form=el('form');form.id='package-config-form';const fields=el('div',undefined,'package-fields');
  const configured=new Set(d.configuration.configured_secrets||[]);
  for(const [name,spec] of Object.entries(d.schema.properties||{})){
   const row=el('div',undefined,'package-field'),label=el('label',spec.title||name);row.append(label);let input;
   if(spec.enum?.length){input=el('select');for(const v of spec.enum){const o=new Option(String(v),JSON.stringify(v));input.add(o)}}else{input=el('input');input.type=spec['x-secret']?'password':spec.type==='boolean'?'checkbox':['integer','number'].includes(spec.type)?'number':'text';if(spec.type==='integer')input.step='1';if(spec.minimum!=null)input.min=spec.minimum;if(spec.maximum!=null)input.max=spec.maximum;if(spec.maxLength!=null)input.maxLength=spec.maxLength}
   input.disabled=d.installed.enabled||d.installed.removed;input.id='package-field-'+name;label.htmlFor=input.id;input.dataset.configField=name;const value=d.configuration.values[name]??spec.default;
   if(spec['x-secret']){input.autocomplete='new-password';input.placeholder=configured.has(name)?'已配置，留空保留':'未配置'}else if(spec.enum?.length){input.value=value===undefined?'':JSON.stringify(value)}else if(spec.type==='boolean')input.checked=!!value;else input.value=value??'';
   if(d.schema.required?.includes(name)&&!spec['x-secret'])input.required=true;
   row.append(input);if(spec.description)row.append(el('small',spec.description,'hint'));fields.append(row);
   if(spec['x-secret']&&configured.has(name)){const clear=el('label',undefined,'package-secret-clear'),check=el('input');check.type='checkbox';check.disabled=d.installed.enabled||d.installed.removed;check.dataset.clearSecret=name;clear.append(check,el('span','清除此密钥'));row.append(clear)}
  }
  const save=button('保存插件配置',()=>{});save.id='package-config-save';save.type='submit';save.className='primary';save.disabled=d.installed.enabled||d.installed.removed;form.append(fields,save);editor.append(form);
  form.addEventListener('input',()=>dirty(true));form.addEventListener('submit',saveConfig);
  const taskHead=el('div',undefined,'section-heading');taskHead.append(el('h3','任务'),button('刷新任务',()=>loadJobs(true)));editor.append(taskHead);const tasks=el('div');tasks.id='package-jobs';editor.append(tasks);loadJobs(true);window.WechatSaveActions?.sync();
 }
 async function runtimeAction(action){
  if(!state.detail)return;if(state.dirty){status.textContent='请先保存当前配置，再启动插件。';return}
  const scope=context(),id=scope.id,editVersion=state.editVersion,button=editor.querySelector('[data-plugin-runtime-action]');if(button)button.disabled=true;
  let failure='';
  try{await api('/api/plugin-packages/'+encodeURIComponent(id)+'/runtime',{method:'POST',json:{action,revision:state.detail.installed.revision}})}catch(e){failure=e.message}
  if(!current(scope))return;
  try{const d=await api('/api/plugin-packages/'+encodeURIComponent(id));if(!current(scope))return;state.detail=d;const i=state.installed.findIndex(x=>x.package.id===id);if(i>=0)state.installed[i]=d.installed;state.runtimes[id]=d.runtime||{};renderList();if(editVersion!==state.editVersion||state.dirty){status.textContent=failure||'插件状态已更新；当前未保存的编辑已保留。';return}renderDetail(d);if(failure)status.textContent=failure}
  catch(e){if(current(scope)){status.textContent=failure||e.message;if(button?.isConnected)button.disabled=false}}
 }
 async function saveConfig(event){
  event.preventDefault();if(!state.detail)return;const scope=context(),id=scope.id,editVersion=state.editVersion,d=state.detail,form=$('#package-config-form');if(!form.reportValidity())return;
  const body={revision:d.configuration.revision,values:{},secrets:{},clear_secrets:[]};
  for(const input of form.querySelectorAll('[data-config-field]')){const name=input.dataset.configField,spec=d.schema.properties[name];if(spec['x-secret'])body.secrets[name]=input.value;else if(spec.enum?.length){if(input.value!=='')body.values[name]=JSON.parse(input.value)}else if(spec.type==='boolean')body.values[name]=input.checked;else if(['integer','number'].includes(spec.type)){if(input.value!=='')body.values[name]=Number(input.value)}else if(input.value!==''||!(d.installed.package.http_routes||[]).some(r=>r.optional&&r.endpoint_config===name))body.values[name]=input.value}
  for(const check of form.querySelectorAll('[data-clear-secret]:checked'))body.clear_secrets.push(check.dataset.clearSecret);
  const save=$('#package-config-save');save.disabled=true;
  try{const result=await api('/api/plugin-packages/'+encodeURIComponent(id)+'/configuration',{method:'POST',json:body});if(!current(scope))return;state.detail.configuration=result;if(editVersion!==state.editVersion){status.textContent='已保存提交的配置，后续编辑尚未保存。';return}dirty(false);renderDetail(state.detail);status.textContent='插件配置已保存，未启动服务。'}catch(e){if(current(scope))status.textContent=e.message+'；当前编辑已保留。'}finally{if(save.isConnected)save.disabled=false}
 }
 let next=0;
 function jobDiagnostic(job){
  const stage=job?.diagnostic?.stage;
  if(stage==='dispatch')return '调用插件未完成。';
  if(stage==='authority')return '核对权限时未能继续执行。';
  if(stage==='validation')return '检查结果时未能通过核对。';
  if(stage==='result_mode')return '结果格式不符合此操作。';
  return job?.state==='needs_review'?'结果需要核对；未自动重试。':'';
 }
 async function loadJobs(reset){
  const id=state.selected,epoch=state.epoch,box=$('#package-jobs');if(!box)return;if(reset){next=0;box.replaceChildren()}
  try{const page=await api('/api/plugin-packages/'+encodeURIComponent(id)+'/jobs?limit=30&after='+next);if(epoch!==state.epoch||!box.isConnected)return;box.querySelector('[data-more-jobs]')?.remove();next=page.next;
   const labels={pending:'待执行',submitting:'正在提交',running:'运行中',completed:'已完成',failed:'失败',cancelled:'已取消',needs_review:'需要核对'};
   for(const job of page.items||[]){const row=el('div',undefined,'package-job'),summary=el('div');summary.append(el('span',(labels[job.state]||job.state)+(job.cancel_requested&&job.state!=='cancelled'?' · 正在请求取消':'')));const diagnosis=jobDiagnostic(job);if(diagnosis)summary.append(el('small',diagnosis,'hint'));row.append(summary);if(!['completed','failed','cancelled'].includes(job.state)){const cancel=button('取消任务',async()=>{cancel.disabled=true;try{await api('/api/plugin-packages/'+encodeURIComponent(id)+'/jobs/'+encodeURIComponent(job.id)+'/cancel',{method:'POST',json:{revision:job.revision}});if(epoch===state.epoch)loadJobs(true)}catch(e){status.textContent=e.message;cancel.disabled=false}});cancel.disabled=job.cancel_requested;row.append(cancel)}box.append(row)}
   if(!box.children.length)box.append(el('p','暂无插件任务。','hint'));if(page.has_more){const more=button('继续读取任务',()=>loadJobs(false));more.dataset.moreJobs='';box.append(more)}
  }catch(e){if(epoch===state.epoch)status.textContent=e.message}
 }
 function renderHostDetail(d){
  const scope=context(),data=d.host_data||{},diagnostics=data.diagnostics||{},archive=data.state==='archive',runtime=d.runtime||{state:'stopped'};
  const heading=el('div',undefined,'section-heading');heading.append(el('h3',d.installed?.package?.name||'设备控制'));
  if(!archive&&d.installed&&!d.installed.removed){
   const stopping=d.installed.enabled||['running','stopping'].includes(runtime.state),control=button(stopping?'停止插件':'启动插件',()=>{if(current(scope))runtimeAction(stopping?'stop':'start')});
   control.dataset.pluginRuntimeAction=stopping?'stop':'start';control.disabled=!stopping&&!d.runtime_connected;heading.append(control);
  }
  editor.replaceChildren(heading,status);status.textContent=archive?'此插件已卸载。原有设备任务和受保护素材仍可查看或停止，不会创建新任务。':!d.installed?.enabled?'插件未启动；保存设备设置后，仍需手动启动插件。':runtimeNames[runtime.state]||'状态待核对';
  const message=(text,error=false)=>{if(current(scope)){status.textContent=text;status.classList.toggle('form-error',error)}};
  status.classList.remove('form-error');
  const action=async(name,body={})=>{
   if(!current(scope))throw new Error('账号或插件已变化，请重新打开插件。');
   return api('/api/plugin-packages/'+encodeURIComponent(scope.id)+'/actions/'+encodeURIComponent(name),{method:'POST',json:body});
  };
  const reload=async()=>{
   if(!current(scope))return;const next=await api('/api/plugin-packages/'+encodeURIComponent(scope.id));if(!current(scope))return;
   state.detail=next;
   if(state.dirty){message('状态已读取；当前未保存的设备设置已保留。');return}
   renderDetail(next);
  };
  const bind=(container,label,input,id)=>{input.id='package-mobile-'+id;const caption=el('label',label);caption.htmlFor=input.id;container.append(caption,input)};
  const allowed={configuration:['host_data.settings',['settings.save']],tasks:['host_data.tasks',['tasks.create','tasks.cancel','tasks.stop','']],resources:['host_data.resources',['']],diagnostics:['host_data.diagnostics',['devices.list','']]},seen=new Set();
  for(const declaration of Array.isArray(d.ui_schema?.sections)?d.ui_schema.sections:[]){
   const spec=allowed[declaration.component],section={...declaration,binding:declaration.binding||spec?.[0],action:declaration.action||({configuration:'settings.save',tasks:archive?'tasks.cancel':'tasks.create',diagnostics:archive?'':'devices.list'})[declaration.component]||''};
   if(!spec||section.binding!==spec[0]||!spec[1].includes(section.action||'')||seen.has(section.component)||archive&&section.component==='configuration')continue;
   seen.add(section.component);
   const panel=el('section',undefined,'panel package-host-'+section.component);panel.id='plugin-section-'+section.component;panel.dataset.hostComponent=section.component;panel.append(el('h3',section.title||section.component));editor.append(panel);
   if(section.component==='diagnostics'){
    panel.append(el('p',archive?diagnostics.planner_reason||'仅可查看历史或停止已有任务。':diagnostics.planner_available===true?'设备规划插件已就绪。':diagnostics.planner_reason||'设备规划插件未就绪，暂不能新建任务。','hint'));
    if(!archive&&section.action==='devices.list'){
     const result=el('p','','hint'),discover=button('检查已授权设备',async()=>{discover.disabled=true;try{const devices=await action('devices.list');if(current(scope)&&result.isConnected)result.textContent=JSON.stringify(devices)}catch(error){message(error.message,true)}finally{if(current(scope)&&discover.isConnected)discover.disabled=false}});panel.append(discover,result);
    }
   }
   if(section.component==='configuration'){
    const settings=data.settings||{},form=el('form'),grid=el('div',undefined,'field-grid'),enabled=el('input'),devices=el('textarea');form.className='package-host-form';enabled.type='checkbox';enabled.checked=!!settings.enabled;enabled.id='package-mobile-enabled';devices.rows=2;devices.value=(settings.devices||[]).join('\n');
    const switchRow=el('label',undefined,'switch-row');switchRow.htmlFor=enabled.id;switchRow.append(el('span','启用设备执行'),enabled);const deviceField=el('div');bind(deviceField,'允许设备 ID（每行一个）',devices,'devices');grid.append(switchRow,deviceField);
    const limits=el('div',undefined,'field-grid'),maxSteps=el('input'),maxSeconds=el('input');for(const input of [maxSteps,maxSeconds]){input.type='number';input.min='1';input.step='1';input.required=true}
    maxSteps.value=settings.max_steps??8;maxSeconds.value=settings.max_seconds??120;const stepField=el('div'),timeField=el('div');bind(stepField,'单次任务最多步骤',maxSteps,'max-steps');bind(timeField,'单次任务最长秒数',maxSeconds,'max-seconds');limits.append(stepField,timeField);
    const actions=el('fieldset',undefined,'mobile-action-options');actions.append(el('legend','允许动作（不选时只观察）'));
    for(const [key,label] of Object.entries({launch:'打开允许应用',tap:'点击',swipe:'滑动',back:'返回',home:'主屏幕',text:'输入英文/数字',capture:'短录屏观察（无音轨）',open_url:'允许浏览器打开公开链接'})){
     const row=el('label'),input=el('input');input.type='checkbox';input.checked=(settings.actions||[]).includes(key);input.dataset.mobileAction=key;row.append(input,document.createTextNode(label));actions.append(row);
    }
    const save=button('保存设备设置',()=>{});save.type='submit';save.className='primary';save.id='package-mobile-save';
    form.append(grid,limits,actions,el('p',settings.connection_profile_status==='已登记'?'设备连接已登记。设备范围和动作权限由当前账号保存。':'设备连接待登记；本页只保存执行范围和权限。','hint'),save);
    form.addEventListener('input',()=>{if(current(scope))dirty(true)});
    form.addEventListener('submit',async event=>{
     event.preventDefault();if(!current(scope)||save.disabled||!form.reportValidity())return;
     const editVersion=state.editVersion,body={revision:state.detail.host_data.settings.revision,enabled:enabled.checked,devices:devices.value.split(/\r?\n/).map(x=>x.trim()).filter(Boolean),actions:[...form.querySelectorAll('[data-mobile-action]:checked')].map(x=>x.dataset.mobileAction),max_steps:Number(maxSteps.value),max_seconds:Number(maxSeconds.value)};save.disabled=true;
     try{const result=await action('settings.save',body);if(!current(scope))return;state.detail.host_data.settings=result;
      if(editVersion!==state.editVersion){message('已保存提交的设备设置，后续编辑尚未保存。');return}
      dirty(false);await reload();if(!state.dirty)message('设备设置已保存；插件运行状态未改变。');
     }catch(error){message(error.message+'；当前编辑已保留。',true)}finally{if(current(scope)&&save.isConnected)save.disabled=false}
    });panel.append(form);
   }
   if(section.component==='tasks'){
    const controls=el('div',undefined,'inline-actions'),refresh=button('刷新任务',async()=>{try{await reload()}catch(error){message(error.message,true)}}),stop=button('停止全部任务',async()=>{stop.disabled=true;try{await action('tasks.stop');await reload()}catch(error){message(error.message,true)}finally{if(current(scope)&&stop.isConnected)stop.disabled=false}});
    stop.disabled=!(data.tasks||[]).some(task=>['queued','running'].includes(task.state));controls.append(refresh,stop);panel.append(controls);
    if(!archive&&section.action==='tasks.create'){
     const form=el('form',undefined,'package-host-task-form'),grid=el('div',undefined,'field-grid'),select=el('select'),goal=el('textarea');select.required=goal.required=true;goal.rows=2;goal.maxLength=1000;goal.placeholder='查看当前页面，描述可见内容，不点击或修改设置';for(const id of data.settings?.devices||[])select.append(new Option(id,id));
     const previous=state.hostRequest;if(previous?.account===scope.account&&previous.id===scope.id){select.value=previous.payload.device_id;goal.value=previous.payload.goal}
     const a=el('div'),b=el('div');bind(a,'设备',select,'device');bind(b,'任务目标',goal,'goal');grid.append(a,b);
     const start=button('开始任务',()=>{});start.type='submit';start.className='primary';start.disabled=!(d.installed?.enabled&&runtime.state==='running'&&data.settings?.enabled&&data.settings?.devices?.length&&diagnostics.planner_available===true);
     form.append(grid,start,el('p','任务只在已授权设备和动作范围内执行；登录、验证码及不确定结果会停止并记录。','hint'));
     const invalidate=()=>{if(current(scope)&&state.hostRequest?.signature!==JSON.stringify({device_id:select.value,goal:goal.value.trim()}))state.hostRequest=null};select.addEventListener('change',invalidate);goal.addEventListener('input',invalidate);
     form.addEventListener('submit',async event=>{
      event.preventDefault();if(!current(scope)||!form.reportValidity()||start.disabled)return;
      const payload={device_id:select.value,goal:goal.value.trim()},signature=JSON.stringify(payload),prior=state.hostRequest;
      if(!prior||prior.account!==scope.account||prior.id!==scope.id||prior.signature!==signature)state.hostRequest={account:scope.account,id:scope.id,signature,payload,key:requestKey()};
      const request=state.hostRequest;start.disabled=true;
      try{const result=await action('tasks.create',{...payload,request_key:request.key});if(!current(scope))return;if(!result?.id)throw new Error('任务提交结果待核对，可使用相同请求编号重试');if(state.hostRequest===request)state.hostRequest=null;await reload()}
      catch(error){message(error.message+'；未自动重试。再次提交相同目标会复用请求编号。',true)}finally{if(current(scope)&&start.isConnected)start.disabled=false}
     });panel.append(form);
    }
    const tasks=el('div',undefined,'mobile-tasks');for(const task of data.tasks||[]){
     const row=el('article',undefined,'mobile-task'),head=el('div',undefined,'section-heading');head.append(el('strong',task.goal||'未命名设备任务'),el('small',({queued:'等待执行',running:'执行中',completed:'已完成',cancelled:'已取消',needs_review:'需要检查',failed:'失败',stopped:'已停止'})[task.state]||task.state,'hint'));row.append(head,el('p',(task.result||task.reason||'正在检查设备状态')+' · '+(task.device_id||'设备待核对'),'hint'));
     if(task.observation_id){const image=el('img');image.alt='此任务最近一次实际设备截图';image.loading='lazy';image.src=window.WechatAccountContext?.url?.('/api/mobile/tasks/'+encodeURIComponent(task.id)+'/screenshot/'+encodeURIComponent(task.observation_id))||'';row.append(image)}
     for(const artifact of task.artifacts||[]){const video=el('video');video.controls=true;video.preload='metadata';video.src=window.WechatAccountContext?.url?.('/api/mobile/tasks/'+encodeURIComponent(task.id)+'/recording/'+encodeURIComponent(artifact.id))||'';row.append(el('p',artifact.summary||'实际短录屏，无音轨','hint'),video)}
     if(['queued','running'].includes(task.state)){const cancel=button('取消任务',async()=>{cancel.disabled=true;try{await action('tasks.cancel',{id:task.id});await reload()}catch(error){message(error.message,true)}finally{if(current(scope)&&cancel.isConnected)cancel.disabled=false}});row.append(cancel)}tasks.append(row);
    }
    if(!tasks.children.length)tasks.append(el('p',archive?'没有保留的设备任务。':'暂无设备任务。','hint'));panel.append(tasks);
   }
   if(section.component==='resources')panel.append(el('p','截图和录屏会随对应任务显示，仍受当前账号权限保护。','hint'));
  }
  editor.scrollTop=0;area.scrollTop=0;window.WechatSaveActions?.sync();
 }
 function migrationReason(reason){
  const labels={prepared_package_digest:'正在核对官方包摘要',plugin_package_not_ready:'插件包尚未完成迁移',tts_key:'缺少语音服务密钥',vision_key:'缺少视觉服务密钥',asr_key:'缺少语音识别服务密钥',yuanbao_cookie_or_assistant_cookie:'缺少分享读取登录信息'};
  return labels[reason]||String(reason||'尚未满足迁移条件');
 }
 function migrationCurrent(modal,account,version){return modal.alive()&&window.WechatAdmin?.isAuthenticated()&&account===(window.WechatAccountContext?.id||'')&&version===state.migrationVersion}
 function invalidateMigrationControls(box,message){
  for(const input of box.querySelectorAll('input,button'))input.disabled=true;
  box.append(el('p',message,'hint'));
 }
 function migrationPermissions(step){return Array.isArray(step.permissions)?step.permissions:[]}
 function migrationDigest(step){return String(step?.package?.expected_package_digest||'')}
 function renderMigrationStep(modal,box,account,version,step,afterApply){
  const display=state.catalog.find(item=>item.id===step.plugin)?.name||step.plugin||'未命名插件';
  const card=el('section',undefined,'package-migration-step'),title=el('div',undefined,'section-heading'),name=el('h3',display);card.style.cssText='display:grid;gap:12px;padding:14px;border:1px solid var(--line);border-radius:12px;background:var(--surface)';title.style.margin='0';name.style.margin='0';title.append(name);card.append(title);
  const blocked=[...(step.required_missing||[])],preserved=[...(step.unmapped_legacy_fields||[])];
  const sourceHash=String(step.apply_source_hash||''),digest=migrationDigest(step),permissions=migrationPermissions(step);
  if(step.status!=='ready_for_administrator_review'||blocked.length||!sourceHash||!digest){
   card.append(el('p','暂不能应用；旧功能继续保持原设置。','hint'));
   const reasons=[...blocked];if(!sourceHash)reasons.push('apply_source_hash_missing');if(!digest)reasons.push('prepared_package_digest');
   if(reasons.length){const list=el('ul');for(const reason of [...new Set(reasons)])list.append(el('li',migrationReason(reason)));card.append(list)}
   if(preserved.length)card.append(el('p','其他设置保留原处：'+preserved.join('、')+'。','hint'));
   box.append(card);return;
  }
  const summary=el('p','将迁移到当前账号，安装后保持未启用；不会启动服务或发送消息。','hint');summary.style.margin='0';card.append(summary);
  if(preserved.length)card.append(el('p','其他设置保留原处：'+preserved.join('、')+'。','hint'));
  const identity=el('dl',undefined,'package-migration-identity');identity.style.cssText='display:grid;grid-template-columns:max-content minmax(0,1fr);gap:6px 12px;margin:0;align-items:baseline';
  for(const [label,value] of [['账号',account],['来源摘要',sourceHash],['官方包摘要',digest]]){const term=el('dt',label),detail=el('dd',value);term.style.fontWeight='600';detail.style.cssText='margin:0;overflow-wrap:anywhere';identity.append(term,detail)}
  card.append(identity);
  const grants=el('div',undefined,'package-migration-grants');grants.style.cssText='display:grid;gap:6px';grants.append(el('strong','允许此插件使用'));
  const choices=[];for(const permission of permissions){const row=el('label',undefined,'switch-row'),check=el('input');check.type='checkbox';check.checked=true;check.dataset.migrationPermission=permission;row.append(el('span',names[permission]||permission),check);grants.append(row);choices.push(check)}
  if(!choices.length)grants.append(el('p','此步骤不请求额外权限。','hint'));card.append(grants);
  const acknowledgement=el('label',undefined,'switch-row'),confirm=el('input'),apply=button('确认并应用迁移',async()=>{});confirm.type='checkbox';confirm.dataset.migrationConfirm='';acknowledgement.append(el('span','我已核对账号、来源摘要、官方包摘要与所选权限'),confirm);card.append(acknowledgement);apply.className='primary';apply.style.justifySelf='start';apply.disabled=true;
  const refresh=()=>{confirm.checked=false;apply.disabled=true};for(const check of choices)check.addEventListener('change',refresh);confirm.addEventListener('change',()=>{apply.disabled=!confirm.checked});
  apply.addEventListener('click',async()=>{
   if(!migrationCurrent(modal,account,version)){invalidateMigrationControls(card,'账号、登录状态或未保存的插件配置已变化；请重新打开迁移预演。');return}
   const selected=choices.filter(check=>check.checked).map(check=>check.dataset.migrationPermission);apply.disabled=true;
   try{
    await api('/api/plugin-migrations/apply',{method:'POST',json:{account,plugin:step.plugin,apply_source_hash:sourceHash,prepared_package_digest:digest,permissions:selected,confirm:true}});
    if(!migrationCurrent(modal,account,version))return;card.replaceChildren(el('h3',step.plugin),el('p','迁移已保存为未启用插件；请在插件详情中检查配置后手动启动。','hint'));await afterApply(step.plugin);
   }catch(error){if(migrationCurrent(modal,account,version)){apply.disabled=false;card.append(el('p',error.message+'；未自动启用，刷新预演后可重试。','hint'))}}
  });
  card.append(apply);box.append(card);
 }
 async function openMigration(){
  if(!window.WechatAdmin?.isAuthenticated())return;showPackages();
  const account=window.WechatAccountContext?.id||'',version=state.migrationVersion,modal=window.WechatDialogs.open({title:'迁移旧设置',subtitle:'先只读核对；每个插件需单独确认，迁移后保持未启用。'}),box=el('div'),loading=el('p','正在读取当前账号的迁移预演…','hint');modal.dialog.style.width='min(720px, calc(100vw - 40px))';modal.dialog.style.height='min(720px, calc(100dvh - 48px))';modal.dialog.style.maxHeight='calc(100dvh - 48px)';const heading=modal.dialog.querySelector('.dialog-heading');heading.style.cssText='flex-wrap:nowrap;padding:14px 16px 12px';heading.querySelector('div').style.minWidth='0';box.append(loading);modal.body.append(box);
  try{
   const preview=await api('/api/plugin-migrations/preview');if(!migrationCurrent(modal,account,version))return;box.replaceChildren();
   const item=(preview.accounts||[]).find(row=>row.account===account);if(!item){box.append(el('p','当前账号不在迁移预演中；没有执行任何迁移。','hint'));return}
   box.append(el('p','仅展示迁移所需的身份与摘要，不显示旧配置值或密钥。','hint'));
   const afterApply=async id=>{await load();if(state.selected===id||!state.selected)await choose(id)};
   for(const step of item.plugins||[])renderMigrationStep(modal,box,account,version,step,afterApply);
   if(!(item.plugins||[]).length)box.append(el('p','没有可迁移的旧设置。','hint'));
  }catch(error){if(migrationCurrent(modal,account,version)){box.replaceChildren(el('p',error.message+'；未执行迁移。','hint'))}}
 }
 function importPackage(){
  const account=window.WechatAccountContext?.id||'',epoch=state.epoch;
  const modal=window.WechatDialogs.open({title:'导入插件',subtitle:'选择 ZIP 文件或填写本机目录，检查后确认安装。'}),label=el('label','本机插件目录'),input=el('input'),fileLabel=el('label','插件 ZIP 文件'),file=el('input'),result=el('div');
  file.type='file';file.accept='.zip,application/zip';fileLabel.append(file);input.placeholder='未选择 ZIP 时，使用此目录';label.append(input);
  const current=()=>modal.alive()&&account===(window.WechatAccountContext?.id||'')&&epoch===state.epoch;
  const invalidate=()=>result.replaceChildren();file.addEventListener('change',invalidate);input.addEventListener('input',invalidate);
  const prepare=button('检查插件',async()=>{
   prepare.disabled=true;file.disabled=true;input.disabled=true;result.replaceChildren();try{
    if(!current())return;const selected=file.files[0];
    if(selected&&selected.size>64*1024*1024)throw new Error('ZIP 超过 64 MiB，请将模型与运行环境作为独立资源安装。');
    const p=selected?await api('/api/plugin-packages/import',{method:'POST',headers:{'Content-Type':'application/zip'},body:selected}):await api('/api/plugin-packages/prepare',{method:'POST',json:{directory:input.value.trim()}});
    if(!current())return;result.append(el('h3',p.package.name),el('p','版本 '+p.package.version,'hint'));
    if(p.package.status!=='ready'){result.append(el('p','此包尚未完成迁移，不能安装。'));return}
    const destinations=(p.package.http_routes||[]).filter(r=>r.credential).map(r=>r.endpoint||('管理员配置的 HTTPS 接口：'+r.endpoint_config));
    if(destinations.length){result.append(el('p','凭据仅发送到以下声明接口：','hint'));const targets=el('ul');for(const endpoint of [...new Set(destinations)]){const item=el('li',endpoint);item.style.overflowWrap='anywhere';targets.append(item)}result.append(targets)}
    const permissions=[];for(const permission of p.package.permissions||[]){const row=el('label',undefined,'switch-row'),check=el('input');check.type='checkbox';check.checked=true;permissions.push([permission,check]);row.append(el('span',names[permission]||permission),check);result.append(row)}
    const confirm=button('确认安装',async()=>{confirm.disabled=true;try{if(!current())return;await api('/api/plugin-packages/install',{method:'POST',json:{token:p.token,digest:p.digest,permissions:permissions.filter(([,c])=>c.checked).map(([name])=>name),confirm:true}});if(!current())return;modal.close();await load();await choose(p.package.id)}catch(e){if(current()){result.append(el('p',e.message,'error'));confirm.disabled=false}}});confirm.className='primary';result.append(el('p','安装后保持未启用；升级将保留兼容的配置和密钥，配置不兼容时保留原安装并要求迁移。权限以本次确认为准。','hint'),confirm);
   }catch(e){if(current())result.append(el('p',e.message,'error'))}finally{prepare.disabled=false;file.disabled=false;input.disabled=false}
  });modal.body.append(fileLabel,label,prepare,result);
 }
 $('#plugins-refresh').addEventListener('click',load);
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='plugins')load()});
 window.WechatPluginPackages={
  async open(id,section){
   if(!window.WechatAdmin?.isAuthenticated())return;showPackages();const owner=account(),epoch=state.epoch;
   await load();if(owner!==account()||epoch!==state.epoch)return;
   if(id===state.selected&&state.detail){const scope=context(),next=await api('/api/plugin-packages/'+encodeURIComponent(id));if(current(scope)){state.detail=next;if(!state.dirty)renderDetail(next)}}else await choose(id);
   if(section&&owner===account()){const scope=context();requestAnimationFrame(()=>{if(current(scope))document.getElementById('plugin-section-'+section)?.scrollIntoView({block:'start',behavior:'auto'})})}
  },
  // Reuse the existing read-only migration preview from capability entry points.
  // It never installs, grants permissions, starts a runtime, or applies a step.
  openMigration
 };
 window.addEventListener('wechat-admin:logout',()=>{state.epoch++;state.migrationVersion++;state.loaded=false;state.loadPromise=null;state.busy=false;state.selected='';state.detail=null;state.catalog=[];state.installed=[];state.runtimes={};state.retained.clear();state.hostRequest=null;dirty(false);list.replaceChildren();editor.replaceChildren(status);status.textContent='选择服务查看配置、权限和任务。'});
 showPackages();
})();
