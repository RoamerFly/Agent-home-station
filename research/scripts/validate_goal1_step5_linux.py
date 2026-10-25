"""Run real public resource/model calls with isolated memory and fake uploads."""
import hashlib,os,pathlib,subprocess
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
files=[root/'admin/config.json',root/'storage-key',root/'api-token']
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
before=[digest(p) for p in files]
env=dict(os.environ,WECHAT_GOAL1_ROOT=str(root),WECHAT_GOAL1_WORKSPACE=str(workspace))
result=subprocess.run([str(workspace/'temp/admin-goal1-step5-tests'),'-test.run','^TestLiveGoal1ResourceSearchDownloadAndModelChoice$','-test.v','-test.timeout','9m'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=550)
(workspace/'research/evidence/goal1-step5-linux-tests.log').write_text(result.stdout,encoding='utf8')
print(result.stdout,flush=True)
assert before==[digest(p) for p in files], 'Production settings or keys changed during isolated regression'
print('No WeChat sends; only fixture staging and memory used. Production settings/native keys unchanged.',flush=True)
raise SystemExit(result.returncode)
