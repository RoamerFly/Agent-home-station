"""Exercise isolated proactive model decisions; no real WeChat sender is wired."""
import hashlib
import os
import pathlib
import subprocess

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
files=[root/'admin/config.json',root/'admin/companion/memory.json',root/'storage-key',root/'api-token']
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
before=[digest(p) for p in files]
env=dict(os.environ,WECHAT_GOAL1_ROOT=str(root),WECHAT_GOAL1_WORKSPACE=str(workspace))
result=subprocess.run([str(workspace/'temp/admin-goal1-step3-tests'),'-test.run','^TestLiveGoal1ProactiveGroupAndFriend$','-test.v','-test.timeout','5m'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=310)
(workspace/'research/evidence/goal1-step3-linux-tests.log').write_text(result.stdout,encoding='utf8')
print(result.stdout,flush=True)
assert before==[digest(p) for p in files], 'Production config, memory or native keys changed'
print('Production configuration, memory and native keys unchanged; no real WeChat sends.',flush=True)
raise SystemExit(result.returncode)
