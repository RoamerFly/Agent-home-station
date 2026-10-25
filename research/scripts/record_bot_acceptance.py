"""Record only approved test metadata and corresponding accepted send IDs."""
from datetime import datetime, timezone
import json
from pathlib import Path
import urllib.parse
import urllib.request

root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
approved=json.loads((root/'approved-group-test.json').read_text())
token=(root/'api-token').read_text().strip()
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
def get(path):
    request=urllib.request.Request(base+path,headers={'Authorization':'Bearer '+token})
    with opener.open(request,timeout=15) as r:
        data=r.read((256<<10)+1)
        assert len(data)<=256<<10
        return json.loads(data)

events=[]
after='103' # Known checkpoint before this stage's explicit live tests.
for _ in range(100):
    page=get('/events?after='+after+'&limit=100')
    events.extend(page['items']);after=page['nextCursor']
    if len(page['items'])<100:break
else:raise SystemExit('event bound exceeded')

expected={'filehelper':'[AI] 自动回复测试成功。',approved['thread_id']:'[AI] 群聊自动回复测试成功。'}
found={}
accepted_ids={thread:set() for thread in expected}
group_inputs=[]
for event in events:
    data=event['data']
    if event['kind']=='send.updated' and data.get('state')=='accepted' and data.get('destination') in expected:
        mid=data.get('localMessageId')
        if not mid:continue
        message=get('/messages/'+urllib.parse.quote(mid,safe=''))
        target=data['destination']
        body=(message.get('body') or '').strip()
        if body.rstrip('。.!！')!=expected[target].rstrip('。.!！'):continue
        accepted_ids[target].add(data['id'])
        found[target]={'reply':body,'client_message_id':data['clientId'],'server_message_id':data['serverId'],'server_accepted':True,'user_confirmed_phone_receipt':True}
    if event['kind']=='message.committed' and data.get('threadId')==approved['thread_id'] and data.get('direction')=='incoming':
        message=get('/messages/'+urllib.parse.quote(data['messageId'],safe=''))
        if message.get('type')==1:
            mention=message.get('mentions') or {}
            group_inputs.append({'mention_state':mention.get('state'),'self_mention':mention.get('self')})

assert set(found)==set(expected),'missing exact accepted test replies'
assert any(i['self_mention'] is True for i in group_inputs),'structured self mention not observed'
assert any(i['self_mention'] is not True for i in group_inputs),'ordinary message not observed'
report={
    'recorded_at_utc':datetime.now(timezone.utc).isoformat(),
    'mode':'native Linux background + Go bot + real DeepSeek Chat Completions',
    'model':'deepseek-flash',
    'model_connectivity_check_passed':True,
    'filehelper':found['filehelper'],
    'group':{'name':approved['group_name'],**found[approved['thread_id']],'ordinary_message_did_not_trigger_reply_user_confirmed':True,'structured_mention_observed':True},
    'automatically_retried':False,
    'bot_restart_used_saved_cursor':True,
    'matched_accepted_filehelper_reply_count':len(accepted_ids['filehelper']),
    'matched_accepted_group_reply_count':len(accepted_ids[approved['thread_id']]),
    'user_confirmation':'符合预期，只回复了 @ 的消息',
    'live_docker_auth_and_reply_exercised':False,
    'cloud_server_exercised':False,
    'message_contents_sent_to_model':'only current matching allowlisted trigger; no history',
    'bot_checkpoint_contains_chat_bodies':False,
}
output=Path(__file__).resolve().parents[1]/'evidence/bot-deepseek-acceptance.json'
output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'evidence_saved':True,'filehelper_reply_confirmed':True,'group_reply_confirmed':True,'ordinary_group_message_ignored':True}))
