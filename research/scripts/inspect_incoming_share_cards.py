"""Inspect structure of the reported share-card inputs, never dump chat XML/URLs."""
import json,pathlib,urllib.request,urllib.parse,xml.etree.ElementTree as ET
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
memory=json.loads((root/'admin/companion/memory.json').read_text())
targets=[]
for scope in memory['scopes'].values():
    if scope['thread']not in ['43929704480@chatroom','50583054216@chatroom']:continue
    for turn in scope['messages']:
        if turn['role']=='user'and '收到了一条分享卡片'in turn['text']and turn.get('message_id'):targets.append((scope['thread'],turn['message_id']))
client=urllib.request.build_opener(urllib.request.ProxyHandler({}));token=(root/'api-token').read_text().strip();results=[]
for thread,id in list(dict.fromkeys(targets))[-6:]:
    req=urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024/messages/'+urllib.parse.quote(id,safe=''),headers={'Authorization':'Bearer '+token})
    with client.open(req,timeout=10)as response:data=json.load(response)
    body=data.get('body')or'';tags=[]
    try:tags=[e.tag for e in ET.fromstring(body).iter()]
    except Exception:pass
    results.append({'thread':thread,'message_id':id,'link_host':urllib.parse.urlparse((data.get('link')or{}).get('url')or'').hostname,'keys':list(data),'object_keys':{k:list(v)for k,v in data.items()if isinstance(v,dict)},'type':data.get('type'),'app_message_type':data.get('appMessageType'),'body_bytes':len(body.encode()),'body_prefix_shape':'xml'if '<'in body[:150]else'plain','xml_tags':tags[:100],'other_string_fields':{k:len(v)for k,v in data.items()if isinstance(v,str)and k not in ['body','id','accountId','threadId','senderId','serverId','createdAt']}})
(workspace/'research/evidence/incoming-share-card-structure.json').write_text(json.dumps(results,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(results,ensure_ascii=False))
