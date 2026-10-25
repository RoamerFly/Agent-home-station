"""Install the verified Channels metadata parser after graceful worker drain.
No image build/export, profile reset or account-data modification.
"""
import hashlib,http.cookiejar,json,os,pathlib,sqlite3,subprocess,tempfile,time,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
candidate=workspace/'temp/wx-channels-cards';binary=root/'bin/wx';assert candidate.read_bytes()[:4]==b'\x7fELF'
base='http://127.0.0.1:8081';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+path,data=None if data is None else json.dumps(data).encode(),headers=headers),timeout=15)as r:return json.load(r)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
ticket=call('/api/runtime/drain',{},csrf)['ticket'];stopped=False
try:
    for i in range(1800):
        if call('/api/runtime/drain?ticket='+urllib.parse.quote(ticket))['ready']:break
        if i%30==0:print('Waiting for current jobs to finish before native update; no cancellation.',flush=True)
        time.sleep(1)
    else:raise RuntimeError('Drain deadline; native executable not changed')
    digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
    protected=[root/'admin/config.json',root/'storage-key',root/'api-token'];before=[digest(p)for p in protected]
    db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
    assert db.execute("select count(*)from media_jobs where state in ('queued','running')").fetchone()[0]==0
    old_hash=digest(binary);backup=binary.with_name('wx.previous-'+old_hash[:12])
    if not backup.exists():backup.write_bytes(binary.read_bytes());backup.chmod(0o700)
    subprocess.run(['systemctl','--user','stop','wechat-bot-web.service'],check=True);stopped=True
    try:
        assert db.execute("select count(*)from media_jobs where state in ('queued','running')").fetchone()[0]==0
        subprocess.run(['systemctl','--user','stop','wechat-bot-native.service'],check=True)
        with tempfile.NamedTemporaryFile(dir=binary.parent,delete=False)as f:
            pending=pathlib.Path(f.name);os.fchmod(f.fileno(),0o700);f.write(candidate.read_bytes());f.flush();os.fsync(f.fileno())
        pending.replace(binary)
    finally:
        subprocess.run(['systemctl','--user','start','wechat-bot-native.service'],check=True)
        subprocess.run(['systemctl','--user','start','wechat-bot-web.service'],check=True)
    assert before==[digest(p)for p in protected]
    logged_in=False
    for i in range(90):
        try:
            if not logged_in:
                csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf'];logged_in=True
            status=call('/api/status')
            if status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running':break
        except Exception:pass
        time.sleep(1)
    else:raise RuntimeError('Native/web still reconnecting; previous binary and profile retained')
    (workspace/'dist/linux-amd64/wx-linux-native').write_bytes(candidate.read_bytes())
    report={'native_binary_sha256':digest(binary),'previous_native_sha256':old_hash,'source_change':'received_finder_card_metadata','config_and_keys_unchanged':True,'native_authenticated':True,'web_running':True,'account_reset':False,'docker_images_built_or_exported':0}
    (workspace/'research/evidence/native-channels-update.json').write_text(json.dumps(report,indent=2),encoding='utf8');print(json.dumps(report),flush=True)
finally:
    if not stopped:
        try:call('/api/runtime/undrain',{'ticket':ticket},csrf)
        except Exception:pass
