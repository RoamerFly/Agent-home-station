(()=>{
  'use strict';
  const $=selector=>document.querySelector(selector);
  const state={initialized:false,busy:false,kind:'contacts',thread:'',nextCursor:null,items:[],recipients:[],revision:0,controller:null,recipientsRevision:0};
  const labels={contacts:'联系人',groups:'群聊',threads:'会话',messages:'消息',members:'群成员'};
  const needsThread=kind=>kind==='messages'||kind==='members';
  function add(parent,tag,value,klass){const node=document.createElement(tag);if(klass)node.className=klass;if(value!==undefined&&value!==null)node.textContent=String(value);parent.append(node);return node;}
  function empty(node){while(node.firstChild)node.removeChild(node.firstChild);}
  function api(){return window.WechatAdmin&&window.WechatAdmin.api;}
  function stateText(value){$('#catalog-state').textContent=value;}
  function display(value,fallback){return value===undefined||value===null||String(value).trim()===''?fallback:String(value);}
  function setCoverage(value){const node=$('#catalog-coverage');if(!value){node.textContent='';return;}if(typeof value==='string'){node.textContent=value;return;}const pieces=[];if(value.updated_at)pieces.push('同步时间：'+value.updated_at);if(value.limit)pieces.push('每页最多 '+value.limit+' 条');if(value.note)pieces.push(value.note);node.textContent=pieces.join(' · ');}
  function currentThreadOptions(){const all=state.recipients||[];return state.kind==='members'?all.filter(item=>String(item.id||'').endsWith('@chatroom')):all;}
  function renderThreads(){const select=$('#catalog-thread');const previous=state.thread;empty(select);const candidates=currentThreadOptions();if(!candidates.length){const option=document.createElement('option');option.value='';option.textContent=state.kind==='members'?'暂无已授权群聊':'暂无已授权会话';select.append(option);state.thread='';return;}
    const hint=document.createElement('option');hint.value='';hint.textContent='请选择一个已授权会话';select.append(hint);
    candidates.forEach(item=>{const option=document.createElement('option');option.value=String(item.id||'');option.textContent=display(item.name,item.id);select.append(option);});
    select.value=candidates.some(item=>String(item.id||'')===previous)?previous:'';state.thread=select.value;
  }
  function updateControls(){state.kind=$('#catalog-kind').value;const needed=needsThread(state.kind);$('#catalog-thread-wrap').hidden=!needed;if(needed)renderThreads();$('#catalog-query').disabled=state.busy;$('#catalog-more').disabled=state.busy;$('#catalog-more').hidden=!state.nextCursor;const refresh=$('#catalog-refresh-members');refresh.hidden=state.kind!=='members';refresh.disabled=state.busy||!state.thread||!!window.WechatCatalogSync?.isBusy();}
  async function loadRecipients(){const request=api();if(!request)return;const stamp=++state.recipientsRevision;try{const result=await request('/api/tools/state');if(stamp!==state.recipientsRevision)return;state.recipients=Array.isArray(result.recipients)?result.recipients:[];renderThreads();}catch(error){if(stamp!==state.recipientsRevision)return;state.recipients=[];renderThreads();stateText('无法加载已授权会话：'+error.message);}}
  function field(card,label,value){if(value===undefined||value===null||String(value).trim()==='')return;if(label.includes('ID')||label==='微信号'){let detail=card.querySelector('.catalog-identifiers');if(!detail){detail=add(card,'details',undefined,'catalog-identifiers');add(detail,'summary','标识信息')}card=detail}const row=add(card,'div',undefined,'catalog-field');add(row,'span',label);add(row,'strong',String(value));}
  function describeItem(item){const card=document.createElement('article');card.className='catalog-item';const title=add(card,'h3');const id=item.roomId||item.username||item.peerId||item.id;window.WechatNames.remember(id,item.remark,item.roomDisplayName,item.nickname,item.name,item.displayName);
    if(state.kind==='contacts'){title.textContent=window.WechatNames.display(id);field(card,'微信号',item.username);field(card,'昵称',item.nickname);field(card,'备注',item.remark);field(card,'别名',item.alias);field(card,'关系',item.relation);field(card,'目录 ID',item.id);}
    else if(state.kind==='groups'){title.textContent=window.WechatNames.display(id);field(card,'群名称',item.name);field(card,'备注',item.remark);field(card,'群 ID',item.roomId||item.id);field(card,'群主 ID',item.ownerId);field(card,'我的群昵称',item.myDisplayName);field(card,'群公告',item.announcement);field(card,'已免打扰',item.muted===undefined?'':(item.muted?'是':'否'));}
    else if(state.kind==='threads'){title.textContent=window.WechatNames.display(id);field(card,'会话类型',item.kind);field(card,'对方 ID',item.peerId);field(card,'未读数',item.unreadCount);field(card,'会话 ID',item.id);}
    else if(state.kind==='members'){title.textContent=window.WechatNames.display(id);field(card,'成员昵称',item.displayName);field(card,'微信号',item.username);field(card,'群内昵称',item.roomDisplayName);field(card,'成员 ID',item.id);}
    else {title.textContent=(item.direction==='outgoing'?'我发送的消息':'收到的消息');field(card,'时间',item.createdAt);field(card,'消息类型',item.type);field(card,'发送者',window.WechatNames.display(item.senderId,item.sender_name));field(card,'会话',window.WechatNames.display(item.threadId));const body=add(card,'p',display(item.body,'（无可显示正文）'),'catalog-message');body.setAttribute('aria-label','消息内容');if(item.serverId&&item.id){const actions=add(card,'div',undefined,'catalog-actions');const quote=add(actions,'button','引用此消息','secondary');quote.type='button';quote.addEventListener('click',()=>{const target=document.querySelector('.nav-link[data-section="tools"]');if(target)target.click();window.dispatchEvent(new CustomEvent('wechat-admin:quote',{detail:{message_id:item.id,server_id:item.serverId,thread_id:item.threadId||state.thread,body:item.body||''}}));});if(item.direction==='outgoing'){const recall=add(actions,'button','准备撤回','secondary');recall.type='button';recall.addEventListener('click',()=>{const target=document.querySelector('.nav-link[data-section="account-actions"]');if(target)target.click();window.dispatchEvent(new CustomEvent('wechat-admin:recall',{detail:{message_id:item.id,server_id:item.serverId,thread_id:item.threadId||state.thread,body:item.body||''}}));});}}}
    return card;
  }
  function renderItems(){const list=$('#catalog-list');empty(list);if(!state.items.length)return;state.items.forEach(item=>list.append(describeItem(item)));}

  function cancelQuery(){state.revision++;state.controller?.abort();state.controller=null;state.busy=false;}
  async function query(more){
    if(state.busy)return;const request=api();if(!request){stateText('管理端尚未准备好，请稍后重试。');return;}
    state.kind=$('#catalog-kind').value;state.thread=$('#catalog-thread').value||'';
    const kind=state.kind,thread=state.thread,search=$('#catalog-search').value.trim();
    if(needsThread(kind)&&!thread){stateText(kind==='members'?'请先选择一个已授权群聊。':'请先选择一个已授权会话。');return;}
    const revision=++state.revision,controller=new AbortController();state.controller=controller;let timedOut=false;
    const timer=setTimeout(()=>{timedOut=true;controller.abort()},15000);
    state.busy=true;updateControls();stateText(more?'正在加载更多目录项…':'正在查询目录…');$('#catalog-list').setAttribute('aria-busy','true');
    try{
      const params=new URLSearchParams({kind});if(search)params.set('search',search);if(needsThread(kind))params.set('thread',thread);if(more&&state.nextCursor)params.set('cursor',state.nextCursor);
      const result=await request('/api/catalog?'+params.toString(),{signal:controller.signal});if(revision!==state.revision)return;
      const items=Array.isArray(result.items)?result.items:[];
      state.items=more?state.items.concat(items):items;state.nextCursor=result.next_cursor||null;setCoverage(result.coverage);renderItems();
      if(state.items.length)stateText('已显示 '+state.items.length+' 条'+labels[kind]+'。'+(state.nextCursor?'可继续加载更多。':''));
      else if(kind==='members')stateText(search?'未找到匹配的成员。可调整搜索，或刷新当前群成员资料。':'本地尚未同步这个群的成员名单，请点击“刷新当前群成员”；这不表示群里没有成员。');
      else stateText('没有符合条件的'+labels[kind]+'。');
    }catch(error){
      if(revision!==state.revision)return;
      // Keep a successfully loaded page on a later-page error.
      if(!more){state.items=[];renderItems()}
      stateText(timedOut?'目录查询超时，已停止等待。可以重新查询，或刷新当前群成员资料。':'查询目录失败：'+error.message);
    }finally{
      clearTimeout(timer);
      if(revision===state.revision){state.busy=false;state.controller=null;$('#catalog-list').setAttribute('aria-busy','false');updateControls()}
    }
  }
  function changed(text){cancelQuery();state.nextCursor=null;state.items=[];$('#catalog-list').setAttribute('aria-busy','false');renderItems();updateControls();stateText(text)}
  function initialize(){
    if(state.initialized)return;state.initialized=true;
    $('#catalog-kind').addEventListener('change',()=>changed('请选择条件后手动查询。'));
    $('#catalog-thread').addEventListener('change',event=>{state.thread=event.target.value;changed('会话已切换，请查询当前会话。')});
    $('#catalog-search').addEventListener('input',()=>changed('搜索条件已更改，请重新查询。'));
    $('#catalog-query').addEventListener('click',()=>query(false));$('#catalog-more').addEventListener('click',()=>query(true));
    $('#catalog-search').addEventListener('keydown',event=>{if(event.key==='Enter'){event.preventDefault();query(false)}});
    updateControls();loadRecipients();
  }
  window.addEventListener('wechat-admin:catalog-synced',event=>{if(state.initialized){loadRecipients();if(!needsThread(state.kind)||event.detail?.kind==='members'&&event.detail.thread===state.thread&&state.kind==='members')query(false)}});
  window.addEventListener('wechat-admin:logout',()=>{cancelQuery();state.recipientsRevision++;state.items=[];state.recipients=[];state.nextCursor=null;state.thread='';stateText('请选择类型后手动查询。');renderItems();updateControls()});
  window.addEventListener('wechat-admin:navigate',event=>{if(event.detail?.section==='catalog')initialize();else if(state.busy)changed('查询已停止，返回后可重新查询。')});
  window.addEventListener('wechat-admin:catalog-target',async event=>{initialize();await loadRecipients();state.thread=event.detail.thread;state.kind=state.thread.endsWith('@chatroom')?'members':'friends';$('#catalog-kind').value=state.kind;$('#catalog-thread').value=state.thread;updateControls();query(false)});
})();
