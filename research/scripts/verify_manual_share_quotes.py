"""Read canonical outgoing quoted messages and their exact source references."""
import json,pathlib,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024';client=urllib.request.build_opener(urllib.request.ProxyHandler({}));token=(root/'api-token').read_text().strip()
def get(path):
    with client.open(urllib.request.Request(base+path,headers={'Authorization':'Bearer '+token}),timeout=15)as r:return json.load(r)
results=[]
for item in json.loads((workspace/'research/evidence/manual-share-replies-20261025.json').read_text())['results']:
    intent=get('/sends/by-idempotency-key?key='+urllib.parse.quote(item['idempotency_key']))
    original=get('/messages/'+urllib.parse.quote(item['original_message_id'],safe=''))
    outgoing=get('/messages/'+urllib.parse.quote(intent['localMessageId'],safe=''))
    quote=outgoing.get('quote')or{}
    row={'thread':item['thread'],'state':intent['state'],'has_remote_receipt':bool(intent.get('serverId')),'outgoing_app_message_type':outgoing.get('appMessageType'),'quoted_original_server_id_matches':quote.get('serverId')==original.get('serverId'),'reply_in_memory':item['reply_in_memory'],'phone_rendering_verified':False}
    assert row['state']=='accepted'and row['has_remote_receipt']and row['outgoing_app_message_type']==57 and row['quoted_original_server_id_matches']and row['reply_in_memory']
    results.append(row)
(workspace/'research/evidence/manual-share-quote-verification.json').write_text(json.dumps(results,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(results,ensure_ascii=False))
