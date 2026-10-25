"""Live model diagnosis only: no tools executed, songs generated or WeChat sends."""
import argparse,os,subprocess,json,http.cookiejar,urllib.request
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
p=argparse.ArgumentParser();p.add_argument('--download',action='store_true');args=p.parse_args()
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,body=None):
    request=urllib.request.Request('http://127.0.0.1:8081'+path,data=None if body is None else json.dumps(body).encode(),headers={'Origin':'http://127.0.0.1:8081','Content-Type':'application/json'})
    with client.open(request,timeout=15) as response:return json.load(response)
call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})
config=call('/api/config');rows=call('/api/logs')['items']
report={'configured_max_tokens':config['max_tokens'],'events':[{k:r.get(k) for k in ['time','event','category','tool','reason','status','model_stage','model_http_status','model_attempts']} for r in rows if r.get('time','').startswith('2026-10-25T05:30:')]}
(workspace/'research/evidence/reported-song-failure-events.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False,indent=2),flush=True)
env=dict(os.environ,WECHAT_LIVE_MEDIA_ROOT=str(root),WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_SONG_FAILURE_LIVE='1')
if args.download:env['WECHAT_SONG_DOWNLOAD_LIVE']='1'
pattern='TestLiveReportedSongPublicCandidatePreparesVoice' if args.download else 'TestLiveReported(SongFailureWithoutExternalActions|ChineseSongSearchUsesWebSources)'
result=subprocess.run([str(workspace/'temp/admin-song-probe'),'-test.v','-test.run='+pattern],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=210)
(workspace/'research/evidence/reported-song-model-probe.log').write_bytes(result.stdout)
print(result.stdout.decode(errors='replace'));raise SystemExit(result.returncode)
