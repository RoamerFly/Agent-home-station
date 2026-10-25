"""Real model -> local test-tone API plugin -> native upload -> dry-run decision.
No singing provider is simulated as real. Never sends a WeChat message.
Run as fly in Ubuntu-New, using isolated temporary private admin state.
"""
import http.cookiejar, http.server, io, json, math, os, struct, subprocess
import tempfile, threading, time, urllib.request, wave
from pathlib import Path

root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=Path(__file__).resolve().parents[2]
count=0
sample=io.BytesIO()
with wave.open(sample,'wb') as audio:
    audio.setnchannels(1);audio.setsampwidth(2);audio.setframerate(16000)
    audio.writeframes(b''.join(struct.pack('<h',int(3000*math.sin(2*math.pi*440*i/16000))) for i in range(4800)))
class Fixture(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        global count
        assert self.path=='/tone'
        self.rfile.read(int(self.headers.get('Content-Length','0')))
        count+=1
        self.send_response(200);self.send_header('Content-Type','audio/wav');self.end_headers()
        self.wfile.write(sample.getvalue())
fixture=http.server.ThreadingHTTPServer(('127.0.0.1',0),Fixture)
threading.Thread(target=fixture.serve_forever,daemon=True).start()
def call(client,base,path,value=None,csrf=None):
    headers={'Origin':base}
    if csrf:headers['X-CSRF-Token']=csrf
    raw=None
    if value is not None:raw=json.dumps(value).encode();headers['Content-Type']='application/json'
    with client.open(urllib.request.Request(base+path,data=raw,headers=headers),timeout=180) as response:return json.load(response)
report={}
try:
    with tempfile.TemporaryDirectory(prefix='wechat-plugin-check-',dir=root) as temporary:
        folder=Path(temporary)
        config=json.loads((root/'admin/config.json').read_text())
        config['enabled']=False
        config['companion']['enabled']=True
        config['companion']['permissions'].update({'send_voice':True,'send_image':False,'send_video':False,'send_file':False,'remember':False,'proactive':False})
        path=folder/'config.json';path.write_text(json.dumps(config));path.chmod(0o600)
        env=dict(os.environ,WECHAT_ADMIN_DATA_DIR=str(folder),WECHAT_ADMIN_LISTEN='127.0.0.1:8094',WECHAT_NATIVE_TOKEN_FILE=str(root/'api-token'),WECHAT_NATIVE_ACCOUNT='background-test-20261024',WECHAT_NATIVE_URL='http://127.0.0.1:6174',WECHAT_BOT_STATE_DIR=str(folder/'checkpoint'))
        proc=subprocess.Popen([str(root/'bin/wechat-admin')],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try:
            client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            base='http://127.0.0.1:8094'
            for attempt in range(40):
                try:call(client,base,'/healthz');break
                except OSError:time.sleep(.2)
            csrf=call(client,base,'/api/login',{'password':(folder/'admin-token').read_text().strip()})['csrf']
            manifest={'id':'audio_fixture','name':'测试音频','version':'1','tools':[{'name':'tone','description':'生成一段短测试音，仅用于管线验证，不是人声或歌声。','input_schema':{'type':'object','properties':{'text':{'type':'string'}},'required':['text']},'url':f'http://127.0.0.1:{fixture.server_port}/tone','method':'POST','media_kind':'voice'}]}
            call(client,base,'/api/plugins/install',{'manifest':manifest},csrf)
            call(client,base,'/api/plugins/audio_fixture',{'enabled':True},csrf)
            result=call(client,base,'/api/companion/preview',{'mode':'direct','text':'请使用已配置的 audio_fixture__tone 生成一段测试音频并把该音频作为回复。无需文字。这是测试音，不是人声或歌曲。'},csrf)
            decision=result['decision']
            assert result['dry_run'] is True and decision['action']=='reply' and len(decision.get('media_ids',[]))==1, 'Model did not select the generated audio'
            assert count==1,'Plugin API was called repeatedly'
            assert not call(client,base,'/api/companion/state')['scopes']
            report={'real_model_called':True,'plugin_api':'local deterministic test-tone fixture','provider_is_real_tts_or_singing':False,'plugin_create_calls':count,'native_upload_completed':True,'decision_action':decision['action'],'generated_media_count':len(decision['media_ids']),'real_wechat_sent':False,'real_memory_read':False,'production_config_changed':False}
        finally:
            proc.terminate();proc.wait(timeout=20)
finally:
    fixture.shutdown();fixture.server_close()
(workspace/'research/evidence/plugin-live-preview.json').write_text(json.dumps(report,indent=2),encoding='utf8')
print(json.dumps(report),flush=True)
