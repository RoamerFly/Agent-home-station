"""Read-only final runtime audit. Never print credentials or session contents."""
import hashlib,http.cookiejar,json,subprocess,urllib.request,urllib.error
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,body=None):
    request=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers={'Origin':base,'Content-Type':'application/json'})
    with client.open(request,timeout=15) as response:return json.load(response)
call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})
speech=call('/api/speech');status=call('/api/status');config=call('/api/config')
agent=call('/api/agent')['settings']
private=json.loads((root/'admin/speech-config.json').read_text())
speech['settings'].setdefault('key','');speech['settings'].setdefault('yue_token','')
stored_key=(workspace/'key阿里云百炼.txt').read_text(encoding='utf-8-sig').strip()
memory=json.loads((root/'admin/companion/memory.json').read_text())
assets=json.loads((root/'admin/media-assets.json').read_text())
links={(a['scope'],a['id']):a['source_url'] for a in assets if a.get('source_url')}
linked=0;missing=0
for scope in memory['scopes'].values():
    for turn in scope.get('messages',[]):
        media=turn.get('media') or {};source=links.get((scope['id'],media.get('asset_id')))
        if source:
            linked+=media.get('url')==source
            missing+=not media.get('url')
bridge=urllib.request.build_opener(urllib.request.ProxyHandler({}))
request=urllib.request.Request(private['yue_endpoint']+'/health',headers={'Authorization':'Bearer '+private['yue_token']})
with bridge.open(request,timeout=15) as response:health=json.load(response)
try:
    bridge.open(private['yue_endpoint']+'/health',timeout=10)
    unauthorized=False
except urllib.error.HTTPError as error:unauthorized=error.code==401
result={'version':status['version'],'native_connection':status['native'].get('connection'),'bot':status['bot'],'speech':{'key_configured':speech['key_configured'],'key_matches_updated_file':private['key']==stored_key,'secrets_redacted':not speech['settings']['key'] and not speech['settings']['yue_token'],'private_mode':oct((root/'admin/speech-config.json').stat().st_mode&0o777),'tts_model':private['tts_model'],'asr_model':private['asr_model'],'yue_ready':health['ready'],'bridge_denies_missing_token':unauthorized},'context':config['companion']['context'],'memory_messages':config['companion']['memory_messages'],'memory_days':config['companion']['memory_days'],'linked_media_memories':linked,'missing_known_media_urls':missing,'binary_matches_build':hashlib.sha256((root/'bin/wechat-admin').read_bytes()).digest()==hashlib.sha256((workspace/'dist/linux-amd64/wechat-admin').read_bytes()).digest(),'services_active':all(subprocess.run(['systemctl','--user','is-active','--quiet',u]).returncode==0 for u in ['wechat-bot-web.service','wechat-bot-native.service'])}
result['music_followup']={'agent_enabled':agent['enabled'],'tasks_enabled':agent['tasks_enabled'],'send_voice':config['companion']['permissions']['send_voice'],'send_file':config['companion']['permissions']['send_file']}
result['acknowledge_tools']=config['behavior']['acknowledge_tools']
assert result['speech']['key_matches_updated_file'] and result['speech']['secrets_redacted'] and result['speech']['yue_ready'] and unauthorized
assert result['services_active'] and result['binary_matches_build'] and missing==0
(workspace/'research/evidence/speech-context-runtime.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(result,ensure_ascii=False,indent=2))
