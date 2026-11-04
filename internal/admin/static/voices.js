/* Account-aware voice library, sharing the console session and theme. */
(() => {
 'use strict';
 const $=id=>document.getElementById(id);let items=[],epoch=0,busy=false;
 function el(tag,text,klass){const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(klass)e.className=klass;return e}
 const fileURL=file=>window.WechatAccountContext.url('/api/voice-library/files/'+encodeURIComponent(file.file));
 function render(){
  const q=$('voice-query').value.trim().toLocaleLowerCase(),engine=$('voice-engine').value,box=$('voice-list'),position=box.scrollTop;
  const shown=items.filter(x=>(!engine||x.engine===engine)&&(!q||JSON.stringify([x.role,x.name,x.engine,x.status]).toLocaleLowerCase().includes(q)));
  $('voice-state').textContent=new Set(items.map(x=>x.role||x.name)).size+' 个角色，'+items.length+' 个版本；当前显示 '+shown.length+' 个版本';box.replaceChildren();
  for(const item of shown){
   const card=el('article',undefined,'voice-card'),meta=el('div',undefined,'voice-meta');meta.append(el('span',item.library_scope==='public'?'公共声线':'本账号私有'),el('span',item.engine));card.append(meta,el('h2',item.name),el('p',item.status,'hint'));if(item.notes){const notes=el('div',undefined,'voice-notes');window.WechatDialogs.text(notes,item.notes,{title:item.name+' · 说明'});card.append(notes)}
   for(const sample of [...(item.previews||[]),...(item.references||[]).filter(x=>/\.(wav|mp3|flac|m4a)$/.test(x.file))]){
    card.append(el('p',sample.filename,'hint'));const audio=el('audio');audio.controls=true;audio.preload='none';audio.src=fileURL(sample);audio.setAttribute('aria-label',sample.filename);card.append(audio);
   }
   const files=[...(item.weights||[]),...(item.references||[]).filter(x=>/\.txt$/.test(x.file))];
   const detail=el(files.length>2?'details':'div');if(files.length>2)detail.append(el('summary','下载模型与参考资料'));
   if(item.recommended_weights)detail.append(el('p',item.paired?'GPT / SoVITS 权重已配对，效果以试听为准。':'权重尚未成对。','hint'));
   const ul=el('ul');for(const file of files){const li=el('li');if(file.file){const a=el('a',file.filename);a.href=fileURL(file);a.download=file.filename;li.append(a)}else li.append(el('span',file.filename));ul.append(li)}if(files.length)detail.append(ul);
   if(item.source)detail.append(el('p','来源：'+item.source));if(item.license)detail.append(el('p',item.license));card.append(detail);box.append(card);
  }
  if(!shown.length)box.append(el('p','没有匹配的声线。','empty'));box.scrollTop=position;
 }
 async function refresh(){if(busy||!window.WechatAdmin?.isAuthenticated())return;busy=true;const stamp=epoch;$('voice-refresh').disabled=true;
  try{const data=await window.WechatAdmin.api('/api/voice-library');if(stamp!==epoch)return;items=data.items||[];const select=$('voice-engine'),old=select.value;select.replaceChildren(new Option('全部模型',''));for(const name of new Set(items.map(x=>x.engine)))select.append(new Option(name,name));select.value=old;render()}
  catch(error){if(stamp===epoch)$('voice-state').textContent=error.message}finally{busy=false;$('voice-refresh').disabled=false}
 }

 refresh=window.WechatRefresh.join(refresh);
 $('voice-query').addEventListener('input',render);$('voice-engine').addEventListener('change',render);window.WechatRefresh.bind($('voice-refresh'),refresh);
 window.addEventListener('wechat-admin:navigate',e=>{if(e.detail.section==='voices')refresh()});
 window.addEventListener('wechat-admin:logout',()=>{epoch++;items=[];$('voice-list').replaceChildren();$('voice-query').value=''});
 document.addEventListener('play',e=>{if(e.target.tagName==='AUDIO')for(const a of document.querySelectorAll('audio'))if(a!==e.target)a.pause()},true);
})();
