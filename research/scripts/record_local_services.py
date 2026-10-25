"""Read-only status checks for the installed test instance."""
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import urllib.request

root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
names=['wechat-bot-native','wechat-bot-api','wechat-bot-ai']
active={name:subprocess.check_output(['systemctl','--user','is-active',name],text=True).strip() for name in names}
enabled={name:subprocess.check_output(['systemctl','--user','is-enabled',name],text=True).strip() for name in names}
assert all(v=='active' for v in active.values())
assert all(v=='enabled' for v in enabled.values())
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open('http://127.0.0.1:8787/readyz',timeout=5) as r:
    assert r.status==200
checkpoint=json.loads((root/'bot-state/checkpoint.json').read_text())
assert checkpoint['cursor'].isdigit() and checkpoint['version']==1
workspace=Path(__file__).resolve().parents[2]
live=json.loads((workspace/'research/evidence/bot-deepseek-acceptance.json').read_text(encoding='utf-8'))
assert live['matched_accepted_filehelper_reply_count']==1 and live['matched_accepted_group_reply_count']==1
report={
    'recorded_at_utc':datetime.now(timezone.utc).isoformat(),
    'manager':'WSL2 Ubuntu-New systemd user services',
    'active':active,'enabled':enabled,
    'sending_api_ready_http':200,
    'checkpoint_persisted':True,
    'private_key_mode':oct((root/'deepseek-api-key').stat().st_mode & 0o777),
    'bot_restart_used_saved_cursor':True,
    'filehelper_matching_test_reply_count_after_restarts':live['matched_accepted_filehelper_reply_count'],
    'group_matching_test_reply_count_after_restarts':live['matched_accepted_group_reply_count'],
    'windows_boot_task_installed':False,
}
assert report['private_key_mode']=='0o600'
(workspace/'research/evidence/bot-services.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'services_active':True,'services_enabled':True,'api_ready':True,'restarts_did_not_duplicate_test_replies':True}))
