(() => {
  'use strict';
  const connection = new URLSearchParams(location.search).get('connection') || '';
  const endpoint = '/api/apps/wechat/connections/' + encodeURIComponent(connection);
  const state = {csrf:'', status:null, config:null, rounds:[]};
  const lifetime = new AbortController();
  window.WechatAccountContext = {id:connection};
  function notice(text, error=false) {
    const node=document.querySelector('#notice');node.textContent=text;node.hidden=false;
    node.setAttribute('role',error?'alert':'status');
  }
  async function fetchJSON(path, options={}) {
    const headers = new Headers(options.headers || {});
    if(options.json !== undefined) {headers.set('Content-Type','application/json');options.body=JSON.stringify(options.json)}
    if(options.method && options.method !== 'GET') headers.set('X-CSRF-Token',state.csrf);
    const response=await fetch(path,{...options,headers,credentials:'same-origin',signal:lifetime.signal});
    const result=await response.json();
    if(!response.ok) {const error=new Error(result.error||'读取未完成');error.status=response.status;throw error}
    return result;
  }
  async function api(path, options) {
    if(path==='/api/canvas') {
      const result=await fetchJSON(endpoint+'/canvas',options);
      if(result.connection?.id !== connection) throw new Error('微信连接已变化，请重新打开详细配置');
      const status=result.connection, policy=status.configured_policy||{};
      state.status={bot:{state:status.worker},native:{authenticated:status.login==='authenticated'}};
      state.config={group_rules:policy.group_rules||[],behavior:{
        read_images:policy.station_image_vision,read_videos:policy.station_video_vision
      },model:'工作站公共模型',companion:{permissions:{}}};
      return result;
    }
    if(path==='/api/plugins') {
      const result=await fetchJSON('/api/local/project-tools');
      return {installed:(result.tools||[]).filter(tool=>tool.installed)};
    }
    throw new Error('这项能力请在当前应用或工作站公共工具中打开');
  }
  function openSection(section) {
    if(window.parent===window) return notice('请从工作站的微信详细配置打开相关功能。');
    parent.postMessage({type:'ahs:wechat-section',connection,section},location.origin);
  }
  const toolLabels={memory_search:'搜索会话记忆',memory_read:'读取相关原话',image_inspect:'理解图片',video_inspect:'理解视频',
    web_search:'搜索网页',web_fetch:'阅读网页',document_inspect:'读取文档',document_export:'制作文档',plugin_job_status:'查询工具进度'};
  function description(item) {
    if(item.tool) return toolLabels[item.tool]||'调用应用工具';
    return ({received:'收到消息',decision:'判断与准备',sent:'微信接受回复',error:'处理遇到问题'})[item.category]||'处理进度已更新';
  }
  let refreshing;
  async function refreshRounds() {
    if(refreshing) return refreshing;
    refreshing=(async()=>{
      const items=[];let before='';
      do {
        const query=new URLSearchParams({limit:'100'});if(before)query.set('before',before);
        const result=await fetchJSON(endpoint+'/execution-rounds?'+query);
        for(const item of result.steps||[]) items.push({...item,id:String(item.sequence),time:item.at,thread_id:item.thread,
          message_id:item.message,related_message_id:item.related_message,send_id:item.receipt,
          source:item.thread?.endsWith('@chatroom')?'群聊':'私聊',source_kind:item.thread?.endsWith('@chatroom')?'group':'private',
          content:description(item)});
        before=result.next_before||'';
      } while(before);
      state.rounds=window.WechatRounds.project(items);
      window.dispatchEvent(new Event('wechat-admin:activity-updated'));
    })().catch(error=>{if(!lifetime.signal.aborted)notice('回合记录暂时不可读：'+error.message,true)}).finally(()=>{refreshing=null});
    return refreshing;
  }
  function openRound(round) {
    const popup=window.WechatDialogs.open({title:'回合处理过程',subtitle:'按当前连接的实际记录展示'});
    const list=document.createElement('ol');list.className='bridge-timeline';
    for(const item of round.timeline) {
      const row=document.createElement('li');row.textContent=new Date(item.time).toLocaleTimeString('zh-CN')+' · '+description(item);list.append(row);
    }
    popup.body.append(list);
  }
  window.WechatActivity={rounds:()=>state.rounds,time:raw=>new Date(raw).toLocaleString('zh-CN'),
    status:round=>window.WechatRounds.groupLabels[round.group],refresh:refreshRounds,loadPending:()=>{},openRound};
  window.WechatAdmin={api,notice,isAuthenticated:()=>!!state.csrf,getConfig:()=>state.config,getStatus:()=>state.status,getScopes:()=>[],openDeviceArchive:()=>openSection('plugins')};
  window.WechatApplicationHost={openSection,async start(){
    if(!/^[a-zA-Z0-9_-]+$/.test(connection))throw Error('请选择当前微信账号后打开流程');
    const session=await fetchJSON('/api/session');state.csrf=session.csrf;
    window.WechatFlowModel.setDeviceControlRetired?.(true);
    window.dispatchEvent(new CustomEvent('wechat-admin:navigate',{detail:{section:'workbench'}}));
  }};
  window.addEventListener('pagehide',()=>lifetime.abort(),{once:true});
})();
