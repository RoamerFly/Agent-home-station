"""Inspect private source shape; never print media URL, key or object nonce."""
import json,urllib.request,urllib.parse
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=Path(__file__).resolve().parents[2]
cards=json.loads((workspace/'research/evidence/incoming-share-card-structure.json').read_text())
card=next(c for c in cards if c['app_message_type']==51)
req=urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024/messages/'+urllib.parse.quote(card['message_id'],safe='')+'/channels-media',headers={'Authorization':'Bearer '+(root/'api-token').read_text().strip()})
with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=20) as response:source=json.load(response)
path=root/'admin/validation-media/channels-source.json'
path.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
path.write_text(json.dumps(source),encoding='utf-8');path.chmod(0o600)
report={'state':source.get('state'),'media_host':urllib.parse.urlparse(source.get('mediaUrl','')).hostname,'has_decode_key':bool(source.get('decodeKey')),'has_object_id':bool(source.get('objectId')),'has_object_nonce':bool(source.get('objectNonceId')),'source_fields':list(source)}
(workspace/'research/evidence/channels-source-shape.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report))
