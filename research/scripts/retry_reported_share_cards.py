"""Owner-requested single replay of the two previously silent share inputs.

No synthetic messages, forced mentions, permission changes or new cursors.
An exclusive marker prevents a second replay if the observer is interrupted.
"""
import datetime,fcntl,hashlib,http.cookiejar,json,os,pathlib,tempfile,time,urllib.parse,urllib.request

root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081'
targets=json.loads((workspace/'research/evidence/incoming-share-card-structure.json').read_text())
assert len(targets)==2 and {x['app_message_type']for x in targets}=={4,51}
traces={x['message_id']:x['thread']for x in targets}
marker=root/'admin/manual-share-retry-20261025.json'
assert not marker.exists(),'This manual retry is already claimed; inspect its results, do not replay.'
queue_path=root/'bot-state/messages-v2.json'
config_path=root/'admin/config.json';config_bytes=config_path.read_bytes()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(route,body=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+route,data=None if body is None else json.dumps(body).encode(),headers=headers),timeout=20)as response:return json.load(response)

csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
status=call('/api/status');assert status['native']['authenticated']and status['bot']['state']=='running'
ticket=call('/api/runtime/drain',{},csrf)['ticket'];released=False
try:
    for attempt in range(600):
        if call('/api/runtime/drain?ticket='+urllib.parse.quote(ticket))['ready']:break
        if attempt%30==0:print('Waiting for current work to finish without cancellation.',flush=True)
        time.sleep(1)
    else:raise RuntimeError('Worker remains busy; no message changed')
    assert config_path.read_bytes()==config_bytes
    for target in targets:
        message_url='http://127.0.0.1:6174/v1/accounts/background-test-20261024/messages/'+urllib.parse.quote(target['message_id'],safe='')
        with client.open(urllib.request.Request(message_url,headers={'Authorization':'Bearer '+(root/'api-token').read_text().strip()}),timeout=15)as r:message=json.load(r)
        assert message['threadId']==target['thread']and message['type']==49 and message['appMessageType']==target['app_message_type']
        assert message['direction']=='incoming'and(message.get('link')or{}).get('title')
    with(queue_path.parent/'worker.lock').open('r+b')as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        original=queue_path.read_bytes();queue=json.loads(original)
        items=[x for x in queue['inbox']if x['event']['data'].get('messageId')in traces]
        assert len(items)==2
        for item in items:
            trace=item['event']['data']['messageId']
            assert item['phase']=='silent'and not item.get('tool_started')
            assert item['event']['data']['threadId']==traces[trace]
            assert not any(x.get('trace')==trace for x in queue.get('deliveries',[])+queue.get('media',[])+queue.get('media_replies',[]))
        with(root/'admin/manual-share-retry-before-20261025.json').open('xb')as f:
            os.fchmod(f.fileno(),0o600);f.write(original);f.flush();os.fsync(f.fileno())
        with marker.open('x')as f:
            os.fchmod(f.fileno(),0o600);json.dump({'state':'claimed_once','targets':traces,'original_queue_sha256':hashlib.sha256(original).hexdigest(),'claimed_at':datetime.datetime.now(datetime.timezone.utc).isoformat()},f);f.flush();os.fsync(f.fileno())
        for item in items:
            item['phase']='received';item['updated']=datetime.datetime.now(datetime.timezone.utc).isoformat();item.pop('retry_after',None)
        with tempfile.NamedTemporaryFile(dir=queue_path.parent,delete=False)as f:
            pending=pathlib.Path(f.name);os.fchmod(f.fileno(),0o600);f.write(json.dumps(queue,ensure_ascii=False).encode());f.flush();os.fsync(f.fileno())
        pending.replace(queue_path)
        dirfd=os.open(queue_path.parent,os.O_RDONLY);os.fsync(dirfd);os.close(dirfd)
    call('/api/runtime/undrain',{'ticket':ticket},csrf);released=True
finally:
    if not released:
        try:call('/api/runtime/undrain',{'ticket':ticket},csrf)
        except Exception:pass

print('Both exact original shares queued once; normal group rules and budgets retained.',flush=True)
started=json.loads(marker.read_text())['claimed_at'];last=None
for attempt in range(480):
    queue=json.loads(queue_path.read_text())
    states=[{'thread':x['event']['data']['threadId'],'trace':x['event']['data']['messageId'],'phase':x['phase']}for x in queue['inbox']if x['event']['data'].get('messageId')in traces]
    if states!=last:print(json.dumps(states,ensure_ascii=False),flush=True);last=states
    if len(states)==2 and all(x['phase']in ['replied','silent','failed','blocked','completed','needs_review','stopped','interrupted']for x in states):break
    time.sleep(1)
logs=[{k:x.get(k)for k in ['time','event','category','thread_id','message_id','status','reason','content','send_id']}for x in json.loads((root/'admin/activity.json').read_text())if x.get('message_id')in traces and x['time']>=started]
report={'manual_retry_once':True,'original_messages_retained':True,'synthetic_mentions':False,'states':states,'events':logs,'configuration_unchanged':config_path.read_bytes()==config_bytes,'phone_display_verified':False}
(workspace/'research/evidence/manual-share-retry-20261025.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False),flush=True)
