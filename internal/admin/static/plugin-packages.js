/* Account-scoped package administration. Does not invoke provider or WeChat tools. */
(() => {
 'use strict';
 const $=s=>document.querySelector(s),api=(...a)=>window.WechatAdmin.api(...a);
 const state={catalog:[],installed:[],runtimes:{},selected:'',detail:null,epoch:0,busy:false,loaded:false,dirty:false,editVersion:0,migrationVersion:0};
 const page=$('#plugins'),legacy=page.querySelector('.plugin-workspace');
 const el=(tag,text,klass)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(klass)n.className=klass;return n};
 const button=(text,action)=>{const b=el('button',text,'quiet');b.type='button';b.addEventListener('click',action);return b};
 const tabs=el('nav',undefined,'package-tabs');tabs.setAttribute('aria-label','插件类型');
 const area=el('div',undefined,'package-workspace'),browser=el('aside',undefined,'panel package-browser'),editor=el('article',undefined,'panel package-editor');
 const list=el('div',undefined,'package-list'),status=el('p','选择服务查看配置、权限和任务。','hint');status.setAttribute('role','status');
 const head=el('div',undefined,'section-heading');head.append(el('h3','服务插件'),button('迁移旧设置',openMigration),button('导入本机插件',importPackage));browser.append(head,list);editor.append(status);area.append(browser,editor);
 const services=button('服务插件',()=>mode(true)),external=button('外部接口',()=>mode(false));tabs.append(services,external);legacy.before(area);
 // Share the existing toolbar instead of consuming another full row.
 const toolbar=page.querySelector('.page-toolbar-card');if(toolbar)toolbar.append(tabs);else area.before(tabs);
 function mode(packages){area.hidden=!packages;legacy.hidden=packages;services.setAttribute('aria-pressed',String(packages));external.setAttribute('aria-pressed',String(!packages));window.WechatSaveActions?.sync();if(packages)load()}
 const runtimeNames={running:'运行中',starting:'正在启动',stopping:'正在停止',failed:'异常',stopped:'已停止'};
 const names={'network':'访问网络','gpu':'使用 GPU','device':'控制设备','workspace.read':'读取授权工作区','workspace.write':'修改授权工作区','process.execute':'执行受控进程','artifact.read':'读取授权素材','artifact.write':'生成素材','model.invoke':'调用模型'};
 function dirty(value){state.dirty=value;if(value){state.editVersion++;state.migrationVersion++;for(const check of document.querySelectorAll('[data-migration-confirm]')){check.checked=false;const action=check.closest('.package-migration-step')?.querySelector('button.primary');if(action)action.disabled=true}}window.WechatAdmin.pluginPackageDirty=value}
 function discard(){return !state.dirty||confirm('有未保存的插件配置，确定放弃？')}
 function renderList(){
  list.replaceChildren();const all=new Map(state.catalog.map(p=>[p.id,p]));for(const item of state.installed)all.set(item.package.id,item.package);
  for(const p of all.values()){
   const installed=state.installed.find(x=>x.package.id===p.id),b=button('',()=>choose(p.id));b.className='package-choice';b.setAttribute('aria-pressed',String(state.selected===p.id));
   b.append(el('strong',p.name),el('small',installed?(installed.removed?'已移除':state.runtimes[p.id]?.state==='running'?'运行中':state.runtimes[p.id]?.state==='failed'?'异常':installed.enabled?'已启用 · 未运行':'已安装 · 未启用'):p.status==='ready'?'未安装':'迁移中'));
   list.append(b);
  }
  if(!all.size)list.append(el('p','暂无服务插件，可导入本机插件目录。','empty'));
 }
 async function load(){
  if(!window.WechatAdmin?.isAuthenticated()||state.busy)return;state.busy=true;const epoch=state.epoch;
  try{const data=await api('/api/plugin-packages');if(epoch!==state.epoch)return;state.catalog=data.catalog||[];state.installed=data.installed||[];state.runtimes=data.runtimes||{};state.loaded=true;renderList()}
  catch(e){if(epoch===state.epoch)status.textContent=e.message}finally{state.busy=false}
 }
 async function choose(id){
  if(id===state.selected&&state.detail)return;if(!discard())return;
  const epoch=++state.epoch;state.selected=id;state.detail=null;dirty(false);renderList();editor.replaceChildren(status);status.textContent='正在读取插件…';
  const installed=state.installed.find(x=>x.package.id===id),p=installed?.package||state.catalog.find(x=>x.id===id);
  if(!installed){status.textContent=p?.status==='ready'?'此插件尚未安装。导入并确认权限后可配置。':'此服务正在迁移为独立插件，当前已有功能继续沿用原设置。';editor.prepend(el('h3',p?.name||id));return}
  try{const d=await api('/api/plugin-packages/'+encodeURIComponent(id));if(epoch!==state.epoch)return;state.detail=d;renderDetail(d)}catch(e){if(epoch===state.epoch)status.textContent=e.message}
 }
 function renderDetail(d){
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
  const id=state.selected,epoch=state.epoch,button=editor.querySelector('[data-plugin-runtime-action]');if(button)button.disabled=true;
  let failure='';
  try{await api('/api/plugin-packages/'+encodeURIComponent(id)+'/runtime',{method:'POST',json:{action,revision:state.detail.installed.revision}})}catch(e){failure=e.message}
  if(epoch!==state.epoch)return;
  try{const d=await api('/api/plugin-packages/'+encodeURIComponent(id));if(epoch!==state.epoch)return;state.detail=d;const i=state.installed.findIndex(x=>x.package.id===id);if(i>=0)state.installed[i]=d.installed;state.runtimes[id]=d.runtime||{};renderList();renderDetail(d);if(failure)status.textContent=failure}
  catch(e){if(epoch===state.epoch){status.textContent=failure||e.message;if(button?.isConnected)button.disabled=false}}
 }
 async function saveConfig(event){
  event.preventDefault();if(!state.detail)return;const id=state.selected,epoch=state.epoch,editVersion=state.editVersion,d=state.detail,form=$('#package-config-form');if(!form.reportValidity())return;
  const body={revision:d.configuration.revision,values:{},secrets:{},clear_secrets:[]};
  for(const input of form.querySelectorAll('[data-config-field]')){const name=input.dataset.configField,spec=d.schema.properties[name];if(spec['x-secret'])body.secrets[name]=input.value;else if(spec.enum?.length){if(input.value!=='')body.values[name]=JSON.parse(input.value)}else if(spec.type==='boolean')body.values[name]=input.checked;else if(['integer','number'].includes(spec.type)){if(input.value!=='')body.values[name]=Number(input.value)}else if(input.value!==''||!(d.installed.package.http_routes||[]).some(r=>r.optional&&r.endpoint_config===name))body.values[name]=input.value}
  for(const check of form.querySelectorAll('[data-clear-secret]:checked'))body.clear_secrets.push(check.dataset.clearSecret);
  const save=$('#package-config-save');save.disabled=true;
  try{const result=await api('/api/plugin-packages/'+encodeURIComponent(id)+'/configuration',{method:'POST',json:body});if(epoch!==state.epoch)return;state.detail.configuration=result;if(editVersion!==state.editVersion){status.textContent='已保存提交的配置，后续编辑尚未保存。';return}dirty(false);renderDetail(state.detail);status.textContent='插件配置已保存，未启动服务。'}catch(e){if(epoch===state.epoch)status.textContent=e.message+'；当前编辑已保留。'}finally{if(save.isConnected)save.disabled=false}
 }
 let next=0;
 async function loadJobs(reset){
  const id=state.selected,epoch=state.epoch,box=$('#package-jobs');if(!box)return;if(reset){next=0;box.replaceChildren()}
  try{const page=await api('/api/plugin-packages/'+encodeURIComponent(id)+'/jobs?limit=30&after='+next);if(epoch!==state.epoch||!box.isConnected)return;box.querySelector('[data-more-jobs]')?.remove();next=page.next;
   const labels={pending:'待执行',submitting:'正在提交',running:'运行中',completed:'已完成',failed:'失败',cancelled:'已取消',needs_review:'需要核对'};
   for(const job of page.items||[]){const row=el('div',undefined,'package-job');row.append(el('span',(labels[job.state]||job.state)+(job.cancel_requested&&job.state!=='cancelled'?' · 正在请求取消':'')));if(!['completed','failed','cancelled'].includes(job.state)){const cancel=button('取消任务',async()=>{cancel.disabled=true;try{await api('/api/plugin-packages/'+encodeURIComponent(id)+'/jobs/'+encodeURIComponent(job.id)+'/cancel',{method:'POST',json:{revision:job.revision}});if(epoch===state.epoch)loadJobs(true)}catch(e){status.textContent=e.message;cancel.disabled=false}});cancel.disabled=job.cancel_requested;row.append(cancel)}box.append(row)}
   if(!box.children.length)box.append(el('p','暂无插件任务。','hint'));if(page.has_more){const more=button('继续读取任务',()=>loadJobs(false));more.dataset.moreJobs='';box.append(more)}
  }catch(e){if(epoch===state.epoch)status.textContent=e.message}
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
    const confirm=button('确认安装',async()=>{confirm.disabled=true;try{if(!current())return;await api('/api/plugin-packages/install',{method:'POST',json:{token:p.token,digest:p.digest,permissions:permissions.filter(([,c])=>c.checked).map(([name])=>name),confirm:true}});if(!current())return;modal.close();await load();await choose(p.package.id)}catch(e){if(current()){result.append(el('p',e.message,'error'));confirm.disabled=false}}});confirm.className='primary';result.append(el('p','安装后保持未启用；不会自动启动服务。','hint'),confirm);
   }catch(e){if(current())result.append(el('p',e.message,'error'))}finally{prepare.disabled=false;file.disabled=false;input.disabled=false}
  });modal.body.append(fileLabel,label,prepare,result);
 }
 $('#plugins-refresh').addEventListener('click',()=>{if(!area.hidden)load()});
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail?.section==='plugins'&&!area.hidden)load()});
 window.addEventListener('wechat-admin:logout',()=>{state.epoch++;state.migrationVersion++;state.loaded=false;state.selected='';state.detail=null;state.catalog=[];state.installed=[];state.runtimes={};dirty(false);list.replaceChildren();editor.replaceChildren(status);status.textContent='选择服务查看配置、权限和任务。'});
 mode(true);
})();
