"""Run opt-in Linux regressions without putting credentials in command arguments."""
import hashlib
import os
import pathlib
import subprocess

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
files=[root/'admin/config.json',root/'storage-key',root/'api-token']
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
before=[digest(p) for p in files]
env=dict(os.environ,WECHAT_GOAL1_ROOT=str(root),WECHAT_GOAL1_WORKSPACE=str(workspace),WECHAT_VIDEO_REGRESSION='1')
result=subprocess.run([str(workspace/'temp/admin-goal1-step1-tests'),'-test.run','^Test(PublicVideoRemuxDropsExtraTracksAndPreservesDuration|LiveGoal1ModelQuestionJudgement)$','-test.v','-test.timeout','6m'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=370)
(workspace/'research/evidence/goal1-step1-linux-tests.log').write_text(result.stdout,encoding='utf8')
print(result.stdout,flush=True)
assert before==[digest(p) for p in files],'Production settings or native keys changed'
print('Production settings and native keys unchanged; no real WeChat send path used.',flush=True)
raise SystemExit(result.returncode)
