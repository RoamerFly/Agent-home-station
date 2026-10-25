import os,subprocess
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
env=dict(os.environ,WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_MUSIC_INTENT_LIVE='1')
r=subprocess.run([str(workspace/'temp/admin-music-upgrade-tests'),'-test.v','-test.run=TestLiveMusic(SemanticIntent|BothFormats)'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=360)
(workspace/'research/evidence/music-semantic-validation.log').write_bytes(r.stdout)
print(r.stdout.decode(errors='replace'))
raise SystemExit(r.returncode)
