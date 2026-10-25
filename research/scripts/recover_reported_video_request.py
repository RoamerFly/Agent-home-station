"""Recover exactly the owner's reported never-sent video request once.

Drain the sole writer and hold its flock before changing the inbox. Keep a
private byte-for-byte backup and an exclusive recovery marker. No checkpoint,
other conversation, configuration, secret or native login is changed.
"""
import datetime,fcntl,hashlib,http.cookiejar,json,os,pathlib,subprocess,tempfile,time,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';thread='50583054216@chatroom'
trace='msg_eyJkYXRhYmFzZSI6Im1lc3NhZ2UvbWVzc2FnZV8wLmRiIiwidGFibGUiOiJNc2dfMjhlOGJlMWY0NjlhODEzODM5MzUzMjcwMGYxNWM3YWEiLCJsb2NhbElkIjoiNTEifQ'
queue_path=root/'bot-state/messages-v2.json';config_path=root/'admin/config.json'
marker=root/'admin/reported-video-request-recovery.json'
assert not marker.exists(),'This exact request already has a recovery marker; inspect its existing state instead of replaying'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(route,body=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    req=urllib.request.Request(base+route,data=None if body is None else json.dumps(body).encode(),headers=headers)
    with client.open(req,timeout=15)as response:return json.load(response)
config_bytes=config_path.read_bytes();config=json.loads(config_bytes)
assert config['enabled']and config['companion']['enabled']and config['companion']['permissions']['send_video']
assert config['behavior']['recover_backlog']and any(x['id']==thread for x in config['group_rules'])
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
status=call('/api/status');assert status['native']['authenticated']and status['bot']['state']=='running'
ticket=call('/api/runtime/drain',{},csrf)['ticket'];released=False
try:
    for attempt in range(600):
        if call('/api/runtime/drain?ticket='+urllib.parse.quote(ticket))['ready']:break
        if attempt%30==0:print('Waiting for current work to finish; no cancellation.',flush=True)
        time.sleep(1)
    else:raise RuntimeError('Current worker is still busy; no message changed')
    assert config_path.read_bytes()==config_bytes,'Configuration changed; recovery aborted'
    activity=json.loads((root/'admin/activity.json').read_text())
    related=[x for x in activity if x.get('message_id')==trace]
    assert any(x.get('event')=='character_decision'and x.get('reason')=='invalid_model_decision'for x in related)
    assert not any(x.get('category')in ['sent','tool']or x.get('send_id')for x in related),'Request already had effects; do not replay'
    with(queue_path.parent/'worker.lock').open('r+b')as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        original=queue_path.read_bytes();queue=json.loads(original)
        matches=[x for x in queue['inbox']if x['event']['data'].get('messageId')==trace]
        assert len(matches)==1
        item=matches[0];assert item['phase']=='silent'and not item.get('tool_started')
        assert item['event']['data']['threadId']==thread
        assert not any(x.get('trace')==trace for x in queue.get('deliveries',[])+queue.get('media',[]))
        backup=root/'admin/reported-video-inbox-before-recovery.json'
        with backup.open('xb')as f:os.fchmod(f.fileno(),0o600);f.write(original);f.flush();os.fsync(f.fileno())
        with marker.open('x')as f:
            os.fchmod(f.fileno(),0o600);json.dump({'trace':trace,'thread':thread,'state':'claimed_once','original_queue_sha256':hashlib.sha256(original).hexdigest()},f);f.flush();os.fsync(f.fileno())
        item['phase']='received';item['updated']=datetime.datetime.now(datetime.timezone.utc).isoformat();item.pop('retry_after',None)
        with tempfile.NamedTemporaryFile(dir=queue_path.parent,delete=False)as f:
            pending=pathlib.Path(f.name);os.fchmod(f.fileno(),0o600);f.write(json.dumps(queue,ensure_ascii=False).encode());f.flush();os.fsync(f.fileno())
        pending.replace(queue_path)
        dirfd=os.open(queue_path.parent,os.O_RDONLY);os.fsync(dirfd);os.close(dirfd)
        fcntl.flock(lock,fcntl.LOCK_UN)
    assert config_path.read_bytes()==config_bytes
    call('/api/runtime/undrain',{'ticket':ticket},csrf);released=True
finally:
    if not released:
        try:call('/api/runtime/undrain',{'ticket':ticket},csrf)
        except Exception:pass
print('The exact never-sent video request is queued once; observing its existing task.',flush=True)
last=None
for attempt in range(360):
    q=json.loads(queue_path.read_text());items=[x for x in q.get('media',[])if x.get('trace')==trace]
    inbox=next(x for x in q['inbox']if x['event']['data'].get('messageId')==trace)
    state={'inbox':inbox['phase'],'media':[{'id':x['id'],'state':x['state'],'phase':x['phase'],'reason':x['reason']}for x in items]}
    if state!=last:print(json.dumps(state),flush=True);last=state
    if items and all(x['state']=='accepted'for x in items):break
    if inbox['phase']in ['failed','blocked','interrupted','silent']and not any(not x['state']in ['accepted','stopped','needs_review']for x in items):break
    time.sleep(1)
events=[{k:x.get(k)for k in ['time','event','category','content','reason','status','tool','target_url','bytes','send_id']}for x in json.loads((root/'admin/activity.json').read_text())if x.get('message_id')==trace]
report={'same_original_request_recovered_once':True,'thread':thread,'trace':trace,'state':state,'events':events,'phone_receipt_or_playback_verified':False,'config_unchanged':config_path.read_bytes()==config_bytes,'native_login_restarted':False,'docker_exported':False}
(workspace/'research/evidence/reported-video-live-recovery.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({'observed_state':state,'video_server_accepted':bool(items)and all(x['state']=='accepted'for x in items),'no_automatic_second_request_replay':True}),flush=True)
