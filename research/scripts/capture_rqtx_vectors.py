"""Capture only the selected rqtx function in the explicit Linux isolation harness.

Invokes the library, unlike the earlier static-only audit. No target main or
network services are used. Results are differential-test fixtures, not live sends.
"""
import hashlib,json,pathlib,re,subprocess
root=pathlib.Path(__file__).resolve().parents[2]
inputs=['0'*32,'d41d8cd98f00b204e9800998ecf8427e','900150983cd24fb0d6963f7d28e17f72','f'*32]
inputs += [hashlib.sha256(f'wechat-bot-rqtx-vector-{i}'.encode()).hexdigest()[:32] for i in range(128)]
command=['wsl','-d','Ubuntu-New','-u','root','--','sh','/mnt/e/A-Importance/wechat-bot/research/scripts/run_rqtx_oracle.sh',*inputs]
run=subprocess.run(command,capture_output=True,timeout=35)
stdout=run.stdout.decode('utf8','replace')
stderr=run.stderr.decode('utf8','replace')
rows=[{'input':m.group(1),'output':int(m.group(2))} for m in re.finditer(r'^([0-9a-f]{32})\t(\d+)\s*$',stdout,re.M)]
assert run.returncode==0,(run.returncode,stderr)
assert [r['input'] for r in rows]==inputs,'missing or unexpected oracle output'
assert all(0<=r['output']<=0xffffffff for r in rows)
metadata={
    'authority':'selected native rqtx export, isolated local invocation; not complete equivalence or live WeChat evidence',
    'library_sha256':hashlib.sha256((root/'research/samples/libv08.so.bin').read_bytes()).hexdigest(),
    'data_sha256':hashlib.sha256((root/'research/samples/rqtx.dat.bin').read_bytes()).hexdigest(),
    'isolation':['fresh network namespace','fresh PID and mount namespaces','read-only dedicated chroot without host directories','UID/GID 65534','no-new-privileges + seccomp denying network/process/exec/mount syscalls','CPU/memory/file/process limits'],
    'vectors':rows,
}
out=root/'research/evidence/send-rqtx-oracle-vectors.json'
out.write_text(json.dumps(metadata,indent=2),encoding='utf8')
print(f'Captured {len(rows)} isolated rqtx vectors -> {out.name}')
