"""Real model/network resource checks. Previews never send to WeChat."""
import pathlib,http.cookiejar,urllib.request,urllib.error,json,time,datetime
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf=''
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None):
 headers={'Origin':base,'X-CSRF-Token':csrf};body=None
 if value is not None:body=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with client.open(urllib.request.Request(base+path,data=body,headers=headers),timeout=500)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
started=time.time();report={'version':'0.6.0-local','actual_wechat_sends':0,'checks':{}}
for key,text in [('pop_request','给我发一首好听的流行风格音乐，发语音；可以选择一首公开可下载的器乐，明确曲名和来源。'),('direct_audio','把这个音频作为微信语音分享给我：https://www.w3schools.com/html/horse.mp3')]:
 code,data=call('/api/companion/preview',{'mode':'direct','text':text});d=data.get('decision',{})
 check={'http_status':code,'action':d.get('action'),'reason':d.get('reason'),'has_real_media_ids':bool(d.get('media_ids')),'dry_run':data.get('dry_run'),'text':d.get('messages',[])}
 report['checks'][key]=check;print(key,json.dumps(check,ensure_ascii=False),flush=True)
surf=call('/api/surf')[1]
items=[x for x in surf['items']if datetime.datetime.fromisoformat(x['time'].replace('Z','+00:00')).timestamp()>=started-5]
# Use IDs from the local epoch boundary instead of exposing any chat/body.
audit=[{'tool':x.get('tool'),'status':x.get('status'),'bytes':x.get('bytes',0),'media_count':x.get('media_count',0),'duration_ms':x.get('duration_ms',0),'origin':x.get('origin')}for x in items if x.get('event')=='web_activity']
report['tool_audit']=audit;report['autonomous_surf_enabled']=surf['settings']['enabled'];report['share_targets_configured']=bool(surf['settings']['threads'])
(workspace/'research/evidence/resources-060-live.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
assert all(x['has_real_media_ids']and x['dry_run']for x in report['checks'].values()),'Resource preview incomplete; inspect evidence'
