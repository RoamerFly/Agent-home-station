import os,subprocess
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
env=dict(os.environ,WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_CHANNELS_LIVE='1',WECHAT_CHANNELS_DIAGNOSTIC='1')
r=subprocess.run([str(workspace/'temp/admin-channels-tests'),'-test.v','-test.run=TestLiveChannelsActualObservationWithoutWechatSend'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=240)
(workspace/'research/evidence/channels-actual-observation.log').write_bytes(r.stdout)
print(r.stdout.decode(errors='replace'))
raise SystemExit(r.returncode)
