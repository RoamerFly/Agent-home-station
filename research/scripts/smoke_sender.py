"""Black-box checks of our Windows executable and built Linux container.

No account session is supplied: a send MUST return 503, never a fake receipt.
Only localhost HTTP requests are made by this script.
"""
import json,os,pathlib,secrets,socket,subprocess,time,urllib.request,urllib.error,uuid,hashlib
ROOT=pathlib.Path(__file__).resolve().parents[2]
token=secrets.token_hex(32)
env=dict(os.environ,WECHAT_API_TOKEN=token,WECHAT_ALLOWED_RECIPIENTS='filehelper')
env.pop('WECHAT_SESSION_FILE',None)
def request(base,path,body=None,auth=False):
    headers={}
    if body is not None:headers['Content-Type']='application/json'
    if auth:headers['Authorization']='Bearer '+token
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
    try:
        with urllib.request.urlopen(req,timeout=2) as r:return r.status,json.load(r)
    except urllib.error.HTTPError as r:return r.code,json.load(r)
def check(base):
    for i in range(50):
        try:
            health=request(base,'/healthz')
            break
        except (OSError,urllib.error.URLError):time.sleep(.1)
    else:raise RuntimeError('daemon did not become reachable')
    ready=request(base,'/readyz')
    denied=request(base,'/v1/messages/text',{'recipient':'filehelper','text':'local smoke test'})
    send=request(base,'/v1/messages/text',{'recipient':'filehelper','text':'local smoke test'},True)
    assert health[0]==200 and ready[0]==503 and denied[0]==401 and send[0]==503
    assert send[1]['error']=='transport_not_ready'
    return {'health':health[0],'readiness':ready[0],'unauthenticated_send':denied[0],'sessionless_send':send[0],'send_error':send[1]['error']}
with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
env['WECHAT_LISTEN']=f'127.0.0.1:{port}'
exe=ROOT/'dist/windows-amd64/wechat-sendd.exe'
p=subprocess.Popen([str(exe)],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,creationflags=getattr(subprocess,'CREATE_NO_WINDOW',0))
try:windows=check(f'http://127.0.0.1:{port}')
finally:
    p.terminate();p.communicate(timeout=5)
name='wechat-sendd-smoke-'+uuid.uuid4().hex[:10]
try:
    subprocess.run(['docker','run','--detach','--rm','--name',name,'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--env','WECHAT_API_TOKEN','--env','WECHAT_ALLOWED_RECIPIENTS=filehelper','--env','WECHAT_LISTEN=0.0.0.0:8787','--publish','127.0.0.1::8787','wechat-sendd:dev'],env=env,check=True,capture_output=True,timeout=20)
    binding=subprocess.check_output(['docker','port',name,'8787/tcp'],text=True,timeout=5).strip()
    linux=check('http://'+binding)
    image=json.loads(subprocess.check_output(['docker','image','inspect','wechat-sendd:dev'],text=True,timeout=5))[0]
    runtime=json.loads(subprocess.check_output(['docker','inspect',name],text=True,timeout=5))[0]
    assert runtime['HostConfig']['ReadonlyRootfs'] is True
    assert runtime['Config']['User']=='nonroot:nonroot'
finally:
    subprocess.run(['docker','rm','--force',name],capture_output=True,timeout=10)
out={'scope':'our app only; localhost checks without any WeChat session or real message transmission',
     'windows':windows,'linux_container':linux,'image_id':image['Id'],'image_bytes':image['Size'],
     'windows_executable_sha256':hashlib.sha256(exe.read_bytes()).hexdigest(),
     'container_checks':{'nonroot':True,'read_only_root':True,'capabilities_dropped':True,'host_binding':'127.0.0.1'}}
(ROOT/'research/evidence/send-smoke.json').write_text(json.dumps(out,indent=2),encoding='utf8')
print(json.dumps(out,indent=2))
