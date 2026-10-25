from pathlib import Path
root=Path(__file__).resolve().parents[2]
p=root/'internal/admin/static/index.html';s=p.read_text(encoding='utf8');anchor='<details class="panel advanced-card"><summary>更多角色与记忆设置</summary>'
assert s.count(anchor)==1 and 'id="conversation-enabled"'not in s
block='''<details class="panel conversation-settings"><summary>连续发言与等待</summary><div class="field-grid">
<div><label class="switch-row"><span>按语义安排可中断文字消息</span><input id="conversation-enabled" type="checkbox"><span class="switch" aria-hidden="true"></span></label></div>
<div><label class="switch-row"><span>有新插话时复核未发送内容</span><input id="conversation-recheck-on-message" type="checkbox"><span class="switch" aria-hidden="true"></span></label></div>
<div><label for="conversation-aggregate-ms">同一人短消息聚合窗口（毫秒）</label><input id="conversation-aggregate-ms" type="number" min="0" max="5000"></div>
<div><label class="switch-row"><span>允许模型安排延时补充</span><input id="conversation-allow-wake" type="checkbox"><span class="switch" aria-hidden="true"></span></label></div>
<div><label for="conversation-wake-min-seconds">延时重新评估最短等待（秒）</label><input id="conversation-wake-min-seconds" type="number" min="15" max="3600"></div>
<div><label for="conversation-wake-max-seconds">延时重新评估最长等待（秒）</label><input id="conversation-wake-max-seconds" type="number" min="15" max="3600"></div>
</div><p class="hint">模型决定发几条、何时收尾；新上下文可暂停、改写或取消未发文字。已提交消息和文件任务单独核对。延时补充仅限已选择的主动问候会话，沿用主动权限、每日预算和安静时段；重新评估后仍可保持安静。</p><button id="save-conversation" class="primary" type="button">保存对话调度设置</button></details>'''
s=s.replace(anchor,block+anchor);p.write_text(s,encoding='utf8')
p=root/'internal/admin/static/app.js';s=p.read_text(encoding='utf8')
anchor='  function defaultCompanion()'
helper='''  const conversationDefaults={enabled:true,aggregate_ms:2000,recheck_on_message:true,allow_wake:false,wake_min_seconds:30,wake_max_seconds:600};
  const conversationId=k=>'#conversation-'+k.replaceAll('_','-');
  function conversationToForm(c){const d={...conversationDefaults,...(c||{})};for(const k of Object.keys(conversationDefaults)){const field=$(conversationId(k));if(typeof conversationDefaults[k]==='boolean')field.checked=!!d[k];else field.value=d[k]}}
  function conversationFromForm(){const d={};for(const k of Object.keys(conversationDefaults)){const field=$(conversationId(k));if(!field.checkValidity())throw new Error('请检查对话聚合与延时范围');d[k]=typeof conversationDefaults[k]==='boolean'?field.checked:Number(field.value)}if(d.wake_max_seconds<d.wake_min_seconds)throw new Error('最长重新评估等待不能小于最短等待');return d}
'''
assert s.count(anchor)==1;s=s.replace(anchor,helper+anchor)
s=s.replace('window.WechatProactive?.load(c);','conversationToForm(c.conversation);window.WechatProactive?.load(c);')
anchor='return {...c,enabled:';assert s.count(anchor)==1;s=s.replace(anchor,'return {...c,conversation:conversationFromForm(),enabled:')
s=s.replace("'#save-behavior','#save-config'","'#save-conversation','#save-behavior','#save-config'")
anchor="$('#save-companion').addEventListener('click',saveConfig);";assert s.count(anchor)==1;s=s.replace(anchor,anchor+"$('#save-conversation').addEventListener('click',saveConfig);")
p.write_text(s,encoding='utf8')
p=root/'internal/admin/static/app.css';s=p.read_text(encoding='utf8');s+='\n.conversation-settings>summary{cursor:pointer;padding:8px 0;font-weight:600}.conversation-settings .field-grid>div{min-width:0}.conversation-settings input{width:100%}.conversation-settings .switch-row input{width:1px}\n';p.write_text(s,encoding='utf8')
