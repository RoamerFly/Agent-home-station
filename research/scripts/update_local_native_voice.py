"""Install source-built voice-range candidate; no image creation/export.
Keeps native binary backup and all account/config files. Refuses active media jobs.
"""
import pathlib,hashlib,sqlite3,subprocess,os,time,urllib.request,json,argparse
parser=argparse.ArgumentParser();parser.add_argument('--goal1',action='store_true');args=parser.parse_args()
workspace=pathlib.Path(__file__).resolve().parents[2];root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
candidate=workspace/('temp/wx-voice-paced'if args.goal1 else'temp/wx-voice16k');binary=root/'bin/wx'
assert candidate.read_bytes()[:4]==b'\x7fELF'
assert root.stat().st_uid==os.getuid()and not root.stat().st_mode&0o077
files=[root/'admin/config.json',root/'storage-key',root/'api-token'];before=[hashlib.sha256(p.read_bytes()).hexdigest()for p in files]
c=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
assert c.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0]==0,'Active native media job; defer update'
oldhash=hashlib.sha256(binary.read_bytes()).hexdigest();backup=binary.with_name('wx.previous-'+oldhash[:12])
if not backup.exists():backup.write_bytes(binary.read_bytes());backup.chmod(0o700)
subprocess.run(['systemctl','--user','stop','wechat-bot-web.service'],check=True)
try:
 assert c.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0]==0,'A media job started; defer update'
 subprocess.run(['systemctl','--user','stop','wechat-bot-native.service'],check=True)
 pending=binary.with_name('wx.voice16k-pending');pending.write_bytes(candidate.read_bytes());pending.chmod(0o700);pending.replace(binary)
finally:
 subprocess.run(['systemctl','--user','start','wechat-bot-native.service'],check=True)
 subprocess.run(['systemctl','--user','start','wechat-bot-web.service'],check=True)
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
for _ in range(30):
 try:
  with client.open('http://127.0.0.1:8081/healthz',timeout=2)as f:assert f.status==200
  break
 except Exception:time.sleep(0.5)
else:raise RuntimeError('Web health did not recover; native backup retained')
for unit in['wechat-bot-native.service','wechat-bot-web.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
assert before==[hashlib.sha256(p.read_bytes()).hexdigest()for p in files]
(workspace/'dist/linux-amd64/wx-linux-native').write_bytes(candidate.read_bytes())
report={'source_change':'voice_pacing_and_pre_submission_handshake_recovery'if args.goal1 else'voice_cancel_flag_and_silk_format_fields_and_16KiB_range','native_binary_sha256':hashlib.sha256(candidate.read_bytes()).hexdigest(),'config_and_keys_unchanged':True,'old_binary_backup_retained':True,'native_and_web_active':True,'docker_images_built':0,'docker_exports':0}
(workspace/('research/evidence/goal1-native-voice-update.json'if args.goal1 else'research/evidence/native-voice-range-update.json')).write_text(json.dumps(report,indent=2),encoding='utf8');print(json.dumps(report),flush=True)
