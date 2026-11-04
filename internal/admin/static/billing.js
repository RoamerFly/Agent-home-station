/* Display conversion only. Original usage ledger currencies remain intact. */
(() => {
 'use strict';
 const rate=6.661635220125786;
 const reference={date:'2026-10-29',source:'https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml',rate};
 const yuan=(value,currency='CNY')=>{
  const amount=Number(value);if(!Number.isFinite(amount))return null;
  return currency==='CNY'?amount:currency==='USD'?amount*rate:null;
 };
 const format=value=>value===null?'待换算':Number(value).toLocaleString('zh-CN',{maximumFractionDigits:6})+' 元';
 const total=costs=>{let amount=0;for(const [currency,value] of Object.entries(costs||{})){const converted=yuan(value,currency);if(converted===null&&Number(value)!==0)return null;amount+=converted||0}return amount};
 const Money=window.WechatMoney=Object.freeze({yuan,format,total,reference,hint:`历史外币金额按 ${reference.date} 参考汇率折合，仅用于展示；实际账单以服务商为准。`});

  const $=s=>document.querySelector(s), api=(...args)=>window.WechatAdmin.api(...args);
  const number=v=>Number(v||0).toLocaleString('zh-CN');
  const money=costs=>Money.format(Money.total(costs));
  const stamp=raw=>{const d=new Date(raw);return !raw||Number.isNaN(d.valueOf())?'—':new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(d).replace(' ','-')};
  let latest=null,dirty=false,balanceEpoch=0;
  function render(data){
    latest=data;const today=data.today||{},total=data.total||{};
    $('#billing-today').textContent=number(Number(today.input_tokens||0)+Number(today.output_tokens||0));
    $('#billing-today-detail').textContent=`输入 ${number(today.input_tokens)} / 输出 ${number(today.output_tokens)} · 缓存 ${number(today.cache_hit_tokens)}`;
    $('#billing-cost').textContent=money(total.estimated_costs);$('#billing-cost').title=Money.hint;
    $('#billing-cost-detail').textContent=`今日 ${money(today.estimated_costs)} · 累计 ${number(Number(total.input_tokens||0)+Number(total.output_tokens||0))} Token`;
    const missing=Number(total.requests||0)-Number(total.reported_requests||0);
    $('#billing-coverage').textContent=`自 ${stamp(data.started_at)} 起统计模型请求，不含此前消耗或其他应用。联网与插件单独收费不计入估算。${missing?' '+number(missing)+' 次请求未返回用量，未按零消耗计入。':''}${total.unpriced_requests?' '+number(total.unpriced_requests)+' 次用量未配置单价。':''}`;
    const table=$('#billing-models');table.replaceChildren();
    for(const item of data.models||[]){const row=document.createElement('tr'),usage=item.total||{},labels=['服务商 / 模型','输入 Token','输出 Token','缓存命中','估算费用'];for(const [index,value] of [item.provider+' / '+item.model,number(usage.input_tokens),number(usage.output_tokens),number(usage.cache_hit_tokens),money(usage.estimated_costs)].entries()){const cell=document.createElement('td');cell.textContent=value;cell.dataset.label=labels[index];row.append(cell)}table.append(row)}
    if(!table.children.length){const row=document.createElement('tr'),cell=document.createElement('td');cell.colSpan=5;cell.textContent='尚无调用记录，下一次真实模型请求后开始累计。';row.append(cell);table.append(row)}
    $('#billing-price-source').textContent='当前模型：'+data.active_model+' · '+(data.custom_prices?'自定义固定单价':data.priced?'DeepSeek 官方参考价格（'+data.price_reference_date+'），按工作日 UTC 峰谷时段估算':'暂无参考单价，可填写服务商公布的价格');
    $('#billing-price-state').textContent=dirty?'单价有未保存的更改，点击保存后才用于后续请求。':'';
    if(!dirty){const prices=data.prices||{};$('#billing-currency').value='CNY';const price=value=>Money.yuan(value,prices.currency||'CNY');$('#billing-price-input').value=data.priced?price(prices.input_per_million):'';$('#billing-price-cache').value=data.priced?price(prices.cache_hit_per_million):'';$('#billing-price-output').value=data.priced?price(prices.output_per_million):'';$('#billing-price-form').dataset.activeId=data.active_id}
  }
  async function refreshBalance(){const epoch=++balanceEpoch;$('#billing-balance-detail').textContent='正在查询余额…';try{const value=await api('/api/billing/balance',{method:'POST',json:{}});if(epoch!==balanceEpoch)return;
    const state=$('#billing-balance'),detail=$('#billing-balance-detail');
    if(value.state==='ok'&&value.data){const balances=value.data.balance_infos||[];const sum=key=>Money.total(Object.fromEntries(balances.map(x=>[x.currency,Number(x[key])])));state.textContent=Money.format(sum('total_balance'));state.title=Money.hint;detail.textContent='赠送 '+Money.format(sum('granted_balance'))+' · 充值 '+Money.format(sum('topped_up_balance'))+(value.data.is_available?'':' · 额度不足')}
    else{state.textContent=value.state==='unsupported'?'暂不支持':value.state==='unconfigured'?'未配置':'查询失败';detail.textContent=value.reason||'请稍后刷新'}
  }catch(error){if(epoch===balanceEpoch){$('#billing-balance').textContent='查询失败';$('#billing-balance-detail').textContent=error.message}}}
  const readUsage=window.WechatRefresh.join(async()=>render(await api('/api/billing')));
  refreshBalance=window.WechatRefresh.join(refreshBalance);
  async function refresh(withBalance=false){if(!window.WechatAdmin?.isAuthenticated())return;const button=$('#billing-refresh');button.disabled=true;try{await readUsage();if(withBalance)await refreshBalance();$('#billing-status').textContent='用量更新于 '+stamp(new Date().toISOString())+' · 费用为估算，余额来自当前服务商'}catch(error){$('#billing-status').textContent='费用暂时无法更新，请稍后刷新'}finally{button.disabled=false}}
  $('#billing-price-form').addEventListener('input',()=>{dirty=true;$('#billing-price-state').textContent='单价有未保存的更改，点击保存后才用于后续请求。'});
  $('#billing-price-form').addEventListener('submit',async event=>{event.preventDefault();const form=event.currentTarget;if(!form.reportValidity())return;const button=$('#billing-save-prices');button.disabled=true;try{const result=await api('/api/billing/prices',{method:'POST',json:{active_id:form.dataset.activeId,currency:$('#billing-currency').value,input_per_million:Number($('#billing-price-input').value),cache_hit_per_million:Number($('#billing-price-cache').value),output_per_million:Number($('#billing-price-output').value)}});dirty=false;render(result);window.WechatAdmin.notice('单价已保存，仅用于后续请求的费用估算。')}catch(error){window.WechatAdmin.notice(error.message,true)}finally{button.disabled=false}});
  $('#billing-reset-prices').addEventListener('click',async()=>{try{const result=await api('/api/billing/prices',{method:'POST',json:{active_id:$('#billing-price-form').dataset.activeId,reset:true}});dirty=false;render(result);window.WechatAdmin.notice('已恢复参考单价。')}catch(error){window.WechatAdmin.notice(error.message,true)}});
  window.WechatRefresh.bind($('#billing-refresh'),()=>refresh(true));
  window.addEventListener('wechat-admin:navigate',event=>{if(event.detail.section==='overview')refresh(true)});
  window.addEventListener('wechat-admin:logout',()=>{balanceEpoch++;dirty=false;latest=null;$('#billing-balance').textContent='—';$('#billing-models').replaceChildren()});
  setInterval(()=>{if(!document.hidden&&$('#overview').classList.contains('active-page'))refresh(false)},15000);
  if(window.WechatAdmin?.isAuthenticated())refresh(true);
})();
