"""Linux-only isolated Agent validation; no real WeChat sends/config writes."""
import os
import subprocess
from pathlib import Path

workspace = Path(__file__).resolve().parents[2]
env = dict(os.environ)
env.update(WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',
           WECHAT_LIVE_WORKSPACE=str(workspace), WECHAT_AGENT_LIVE='1')
checks = [
    ('agent-upgrade-linux-host.log', 'host-agent-upgrade-tests', []),
    ('agent-upgrade-linux-permissions.log', 'admin-agent-upgrade-tests', ['-test.run=TestAgent']),
    ('agent-upgrade-live-model.log', 'admin-agent-upgrade-tests', ['-test.run=TestLiveAgentHostAndPersistentCheckpoint']),
]
for log, binary, flags in checks:
    result = subprocess.run([str(workspace/'temp'/binary), '-test.v', *flags], env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
    (workspace/'research/evidence'/log).write_bytes(result.stdout)
    print(log, 'PASS' if result.returncode == 0 else 'FAIL', flush=True)
    if result.returncode:
        print(result.stdout.decode('utf8', errors='replace')[-5000:])
        raise SystemExit(result.returncode)
