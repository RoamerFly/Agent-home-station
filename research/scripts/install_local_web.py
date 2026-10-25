"""Move the existing approved local bot under the web controller.

Retains the original native session, message checkpoint, key and allowed test
group. Stops/disables the former AI worker before enabling the web-owned one.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

workspace=Path(__file__).resolve().parents[2]
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
assert root.is_dir() and root.stat().st_uid==os.getuid() and not root.stat().st_mode & 0o077
admin=root/'admin'
admin.mkdir(mode=0o700,exist_ok=True)
def atomic(path,data,mode=0o600):
    with tempfile.NamedTemporaryFile(dir=path.parent,delete=False) as f:
        temporary=Path(f.name);os.fchmod(f.fileno(),mode);f.write(data);f.flush();os.fsync(f.fileno())
    temporary.replace(path)
atomic(root/'bin/wechat-admin',(workspace/'dist/linux-amd64/wechat-admin').read_bytes(),0o700)
config_file=admin/'config.json'
if not config_file.exists():
    # Import the dashboard's saved provider/role if available, without logging
    # its key. The read is from the explicitly owned local preview container.
    saved=subprocess.run(['docker','exec','wechat-bot-web','cat','/data/admin/config.json'],capture_output=True,check=False)
    config=json.loads(saved.stdout) if saved.returncode==0 else {
        'provider':'deepseek','base_url':'https://api.deepseek.com','model':'deepseek-flash',
        'system_prompt':'你是微信群中的 AI 助手。用简洁、清楚的中文回答当前问题，尽量控制在500字以内。你只能生成回复文字，不能声称执行了外部操作。',
        'prefix':'/ai ','cooldown_seconds':5,'max_age_seconds':120,'max_tokens':768,
        'private_enabled':False,'filehelper_enabled':True,'direct_ids':[],'group_rules':[],
        'api_key':(root/'deepseek-api-key').read_text().strip(),
    }
    group=json.loads((root/'approved-group-test.json').read_text())
    if not any(r['id']==group['thread_id'] for r in config['group_rules']):
        config['group_rules'].append({'id':group['thread_id'],'name':group['group_name'],'mode':'mention','prefix':''})
    config['enabled']=True
    atomic(config_file,json.dumps(config,ensure_ascii=False,indent=2).encode())
subprocess.run(['systemctl','--user','disable','--now','wechat-bot-ai.service'],check=True)
unit=f'''[Unit]
Description=WeChat web control panel and AI bot
After=wechat-bot-native.service
Requires=wechat-bot-native.service
PartOf=wechat-bot-native.service

[Service]
Type=simple
ExecStart={root}/bin/wechat-admin
Environment=WECHAT_ADMIN_DATA_DIR={admin}
Environment=WECHAT_ADMIN_LISTEN=127.0.0.1:8081
Environment=WECHAT_NATIVE_URL=http://127.0.0.1:6174
Environment=WECHAT_NATIVE_TOKEN_FILE={root}/api-token
Environment=WECHAT_NATIVE_ACCOUNT=background-test-20261024
Environment=WECHAT_BOT_STATE_DIR={root}/bot-state
WorkingDirectory={root}
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
'''
units=Path.home()/'.config/systemd/user'
(units/'wechat-bot-web.service').write_text(unit)
subprocess.run(['systemctl','--user','daemon-reload'],check=True)
subprocess.run(['systemctl','--user','enable','--now','wechat-bot-web.service'],check=True)
print('Local web panel enabled on 127.0.0.1:8081; original AI worker disabled; approved session/checkpoint retained.')
