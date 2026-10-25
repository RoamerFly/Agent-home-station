"""Provision private local speech credentials, without printing their contents.
Run --provision before web update, then --apply-context after the update.
"""
import argparse,json,os,http.cookiejar,urllib.request
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
p=argparse.ArgumentParser();p.add_argument('--provision',action='store_true');p.add_argument('--apply-context',action='store_true');a=p.parse_args()
if a.provision:
    key=(workspace/'key阿里云百炼.txt').read_text(encoding='utf-8-sig').strip()
    assert len(key)>=16 and not any(c.isspace() for c in key)
    c={'endpoint':'https://dashscope.aliyuncs.com','key':key,'tts_model':'qwen-audio-3.1-tts-plus','asr_model':'qwen-audio-3.1-asr-flash','voice':'longanlingxin_v3.1','instruction':'年轻活泼、轻松俏皮的中文女声，语气自然，不过度夸张。','tts_enabled':True,'asr_enabled':True,'music_enabled':False,'music_model':'fun-music-v1','yue_enabled':True,'yue_endpoint':'http://172.29.160.1:6188','yue_token':(workspace/'secrets/yue-bridge-token.txt').read_text().strip()}
    target=root/'admin/speech-config.json'
    if target.exists():
        old=json.loads(target.read_text());c={**c,**old};c['key']=key
    tmp=target.with_suffix('.tmp');tmp.write_text(json.dumps(c,ensure_ascii=False),encoding='utf8');tmp.chmod(0o600);tmp.replace(target)
    print('Private speech configuration provisioned; credentials not printed.')
if a.apply_context:
    base='http://127.0.0.1:8081';jar=http.cookiejar.CookieJar();client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
    def call(path,body=None,csrf=''):
        req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers={'Origin':base,'Content-Type':'application/json','X-CSRF-Token':csrf})
        with client.open(req,timeout=35) as r:return json.load(r)
    auth=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})
    c=call('/api/config');c['companion']['memory_messages']=max(c['companion']['memory_messages'],500);c['companion']['memory_days']=max(c['companion']['memory_days'],30);c['companion']['context']={'auto_compress':True,'compress_after':120,'recent_messages':60,'max_input_chars':24000}
    call('/api/config',c,auth['csrf'])
    print('Memory upgraded to at least 500 messages / 30 days; structured auto compression configured. Other saved preferences preserved.')
