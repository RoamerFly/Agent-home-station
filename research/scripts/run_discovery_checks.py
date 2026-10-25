"""Run isolated discovery checks against public sources; no WeChat messages."""
import os,pathlib,subprocess,sys
workspace=pathlib.Path(__file__).resolve().parents[2]
env=dict(os.environ,WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_DISCOVERY_LIVE='1')
kind='model'if '--model'in sys.argv else'sources'
binary='discovery-admin-tests'if kind=='model'else'discovery-tools-tests'
pattern='TestLiveDiscoveryModelPreparesMedia'if kind=='model'else'TestLiveDiscoveryPublicSources'
if '--reported' in sys.argv:pattern='TestLiveReportedSurfListAndRelatedRead';kind='reported'
p=subprocess.run([str(workspace/'temp'/binary),'-test.run='+pattern,'-test.v'],env=env,cwd=workspace,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
(workspace/'research/evidence'/('discovery-'+kind+'-live.log')).write_text(p.stdout,encoding='utf8');print(p.stdout);raise SystemExit(p.returncode)
