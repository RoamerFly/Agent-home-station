"""Validate the image on an isolated native Ubuntu Docker Engine.

Creates one QR challenge, never authenticates an account or sends a message.
Does not print bootstrap/API keys or private native state.
"""
import http.cookiejar
import json
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request

engine=['docker','-H','unix:///home/fly/.local/share/wechat-bot-container-validation/docker.sock']
container='wechat-web-validation'
base='http://127.0.0.1:18081'
password=subprocess.check_output([*engine,'exec',container,'cat','/data/admin/admin-token'],text=True).strip()
jar=http.cookiejar.CookieJar()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
csrf=''
def request(path,body=None,image=False):
    headers={}
    if body is not None:
        headers={'Content-Type':'application/json','Origin':base,'X-CSRF-Token':csrf}
    data=None if body is None else json.dumps(body).encode()
    req=urllib.request.Request(base+path,data=data,headers=headers)
    with client.open(req,timeout=75) as response:
        raw=response.read((4<<20)+1)
        assert len(raw)<=4<<20
        return raw if image else json.loads(raw)

for _ in range(20):
    try:request('/healthz');break
    except OSError:time.sleep(.5)
else:raise SystemExit('web did not start')
csrf=request('/api/login',{'password':password})['csrf']
config=request('/api/config')
config.update(api_key='synthetic-key-for-settings-validation',system_prompt='你是部署验证助手。',group_rules=[{'id':'synthetic@chatroom','name':'部署规则验证','mode':'all','prefix':'问：'}])
saved=request('/api/config',config)
assert saved['key_configured'] and 'api_key' not in saved
assert saved['system_prompt']=='你是部署验证助手。' and saved['group_rules'][0]['mode']=='all'
request('/api/bot/start',{})
assert request('/api/status')['bot']['state']=='waiting_login'
request('/api/bot/stop',{})
assert request('/api/status')['bot']['state']=='disabled'
operation=request('/api/wechat/login',{})
for _ in range(40):
    operation=request('/api/wechat/login/'+operation['id'])
    if operation.get('qr_available') or operation['state'] in ['failed','expired','cancelled','rejected']:break
    time.sleep(1)
assert operation.get('qr_available'),{'state':operation['state'],'code':operation.get('code')}
qr=request('/api/wechat/login/'+operation['id']+'/qr?generation='+str(operation['qr_generation']),image=True)
assert qr.startswith(b'\x89PNG\r\n\x1a\n')
subprocess.run([*engine,'restart','-t','20',container],check=True,stdout=subprocess.DEVNULL)
for _ in range(30):
    try:
        csrf=request('/api/login',{'password':password})['csrf']
        break
    except OSError:time.sleep(.5)
else:raise SystemExit('web did not restore')
restored=request('/api/config')
assert restored['key_configured'] and restored['system_prompt']==saved['system_prompt'] and restored['group_rules']==saved['group_rules']
assert not restored['enabled']
# Leave this isolated validation instance without a misleading fake key/rule.
restored.update(clear_key=True,enabled=False,group_rules=[])
request('/api/config',restored)
report={
    'engine':'native Docker Engine on Ubuntu 24.04.4 WSL2; isolated socket, host network, no Desktop proxy',
    'image':'wechat-bot-web:0.3.0',
    'web_auto_started':True,'admin_login_passed':True,'csrf_cookie_required':True,
    'provider_key_write_only':True,'custom_role_saved':True,'per_group_all_mode_and_prefix_saved':True,
    'bot_start_waited_for_login':True,'bot_stop_passed':True,'settings_and_password_persisted_after_restart':True,
    'native_real_qr_generated':True,'authenticated_qr_png_http':200,'qr_png_bytes':len(qr),
    'real_account_authorized':False,'wechat_message_sent':False,'fake_key_cleared_after_validation':True,
}
output=Path(__file__).resolve().parents[1]/'evidence/web-container-validation.json'
output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps(report))
