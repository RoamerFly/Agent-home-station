"""Install this workspace's verified WSL instance as three user services.

Does not log in, send messages, enable Windows boot tasks or delete profiles.
All mutable files stay in the existing private Linux data directory.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

workspace=Path(__file__).resolve().parents[2]
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
assert root.is_dir() and root.stat().st_uid==os.getuid() and not root.stat().st_mode & 0o077
bin_dir=root/'bin'
bin_dir.mkdir(mode=0o700,exist_ok=True)
for name,source in [('wx','wx-linux-native'),('wechat-sendd','wechat-sendd'),('wechat-bot','wechat-bot')]:
    with tempfile.NamedTemporaryFile(dir=bin_dir,delete=False) as f:
        path=Path(f.name)
        with (workspace/'dist/linux-amd64'/source).open('rb') as s:shutil.copyfileobj(s,f)
        os.fchmod(f.fileno(),0o700)
        f.flush();os.fsync(f.fileno())
    path.replace(bin_dir/name)

# No plaintext keys in the unit, argv or journal. Copy the user's setup input
# into the already private Linux root before loading it at service startup.
key=(workspace/'secrets/deepseek-api-key.txt').read_bytes()
assert 16<=len(key.strip())<=4096
with tempfile.NamedTemporaryFile(dir=root,delete=False) as f:
    path=Path(f.name);os.fchmod(f.fileno(),0o600);f.write(key);f.flush();os.fsync(f.fileno())
path.replace(root/'deepseek-api-key')

launcher='''import json,os,sys
from pathlib import Path
root=Path(__file__).resolve().parent
env=dict(os.environ)
env.update(WECHAT_TRANSPORT='linux-native',WECHAT_NATIVE_URL='http://127.0.0.1:6174',WECHAT_NATIVE_TOKEN_FILE=str(root/'api-token'),WECHAT_NATIVE_ACCOUNT='background-test-20261024')
if sys.argv[1]=='api':
    binary=root/'bin/wechat-sendd'
    env.update(WECHAT_API_TOKEN=(root/'send-api-token').read_text().strip(),WECHAT_LISTEN='127.0.0.1:8787',WECHAT_ALLOWED_RECIPIENTS='filehelper')
else:
    binary=root/'bin/wechat-bot'
    group=json.loads((root/'approved-group-test.json').read_text())
    assert group['group_name']=='机器人测试群' and group['thread_id'].endswith('@chatroom')
    env.update(DEEPSEEK_API_KEY_FILE=str(root/'deepseek-api-key'),WECHAT_BOT_STATE_DIR=str(root/'bot-state'),WECHAT_BOT_ALLOWED_THREADS='filehelper,'+group['thread_id'])
os.execve(binary,[str(binary)],env)
'''
launch_path=root/'service-launch.py'
launch_path.write_text(launcher,encoding='utf-8');launch_path.chmod(0o600)
units=Path.home()/'.config/systemd/user'
units.mkdir(parents=True,exist_ok=True)
commands={
    'wechat-bot-native':f'{bin_dir}/wx --data-dir {root} --listen 127.0.0.1:6174 serve',
    'wechat-bot-api':f'/usr/bin/python3 {launch_path} api',
    'wechat-bot-ai':f'/usr/bin/python3 {launch_path} bot',
}
for name,command in commands.items():
    order='' if name=='wechat-bot-native' else 'After=wechat-bot-native.service\nRequires=wechat-bot-native.service\nPartOf=wechat-bot-native.service\n'
    unit=f'''[Unit]
Description=Local WeChat {name} service
{order}
[Service]
Type=simple
ExecStart={command}
WorkingDirectory={root}
Restart=on-failure
RestartSec=5
TimeoutStopSec=20
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
'''
    (units/(name+'.service')).write_text(unit,encoding='utf-8')
subprocess.run(['systemctl','--user','daemon-reload'],check=True)
subprocess.run(['systemctl','--user','enable',*[name+'.service' for name in commands]],check=True)
subprocess.run(['systemctl','--user','restart',*[name+'.service' for name in commands]],check=True)
print('Installed and started the native, sending API and AI bot user services. No WeChat message submitted.')
