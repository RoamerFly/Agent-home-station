"""Launch the local bot with an optional, previously approved test group."""
import argparse
import json
import os
from pathlib import Path
parser=argparse.ArgumentParser()
parser.add_argument('--with-approved-group',action='store_true')
args=parser.parse_args()
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
allowed=['filehelper']
if args.with_approved_group:
    group=json.loads((root/'approved-group-test.json').read_text())
    assert group['group_name']=='机器人测试群' and group['thread_id'].endswith('@chatroom')
    allowed.append(group['thread_id'])
workspace=Path(__file__).resolve().parents[2]
# Store the runtime copy on the private ext4 directory instead of the Windows
# mount. The Windows input remains an ignored local setup file.
source_key=workspace/'secrets/deepseek-api-key.txt'
private_key=root/'deepseek-api-key'
temporary=root/'deepseek-api-key.temporary'
fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'wb') as f:
    f.write(source_key.read_bytes())
    f.flush()
    os.fsync(f.fileno())
os.replace(temporary,private_key)
env=dict(os.environ)
env.update(WECHAT_NATIVE_URL='http://127.0.0.1:6174',WECHAT_NATIVE_TOKEN_FILE=str(root/'api-token'),WECHAT_NATIVE_ACCOUNT='background-test-20261024',DEEPSEEK_API_KEY_FILE=str(private_key),WECHAT_BOT_STATE_DIR=str(root/'bot-state'),WECHAT_BOT_ALLOWED_THREADS=','.join(allowed))
binary=workspace/'dist/linux-amd64/wechat-bot'
os.execve(binary,[str(binary)],env)
