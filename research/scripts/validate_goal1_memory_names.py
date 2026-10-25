"""Back up memory, then verify exact native names and persisted API backfill.
No message sends, account refreshes or model requests are performed.
"""
import argparse,collections,hashlib,http.cookiejar,json,pathlib,unicodedata,urllib.request,urllib.error,urllib.parse

parser=argparse.ArgumentParser();parser.add_argument('--prepare',action='store_true');args=parser.parse_args()
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
memory=root/'admin/companion/memory.json';backup=memory.with_name('memory-before-goal1-step2.json')
if args.prepare:
    assert not backup.exists(),'Baseline already exists; do not overwrite it'
    with backup.open('xb') as f:backup.chmod(0o600);f.write(memory.read_bytes())
    print('Private memory baseline retained. No names, messages or credentials printed.')

native=urllib.request.build_opener(urllib.request.ProxyHandler({}))
token=(root/'api-token').read_text().strip()
def native_get(path):
    try:
        with native.open(urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024'+path,headers={'Authorization':'Bearer '+token}),timeout=8) as r:return json.load(r)
    except urllib.error.HTTPError as e:
        if e.code==404:return None
        raise
def clean(value):
    value=''.join(c for c in (value or '') if unicodedata.category(c)!='Cc').strip()
    return value.encode()[:256].decode('utf8',errors='ignore')
def expected(thread,sender):
    contact=native_get('/contacts/'+urllib.parse.quote(sender,safe=''))or{}
    assert not contact or contact.get('username')==sender
    remark=clean(contact.get('remark'))
    if remark and remark!=sender:return remark,'remark'
    member={}
    if thread.endswith('@chatroom'):
        lookup=native_get('/groups/'+urllib.parse.quote(thread,safe='')+'/members/'+urllib.parse.quote(sender,safe=''))or{}
        assert not lookup or lookup.get('groupId')==thread and lookup.get('username')==sender
        member=lookup.get('member')or{}
    nick=contact.get('nickname')or contact.get('displayName')or''
    if not clean(nick)or nick==sender:nick=member.get('displayName')or''
    for name,source in [(member.get('roomDisplayName'),'room'),(nick,'nickname')]:
        name=clean(name)
        if name and name!=sender:return name,source
    return sender,'id'

original=json.loads(backup.read_text());pairs={}
for scope in original['scopes'].values():
    if scope['channel']!='wechat' or scope['account']!='background-test-20261024':continue
    for turn in scope['messages']:
        if turn['role']=='user'and turn.get('sender'):pairs[(scope['thread'],turn['sender'])]=expected(scope['thread'],turn['sender'])
coverage=collections.Counter(source for _,source in pairs.values())
if args.prepare:
    print(json.dumps({'existing_conversations':len(original['scopes']),'conversation_speaker_pairs':len(pairs),'available_name_sources':dict(coverage)},ensure_ascii=False));raise SystemExit(0)

base='http://127.0.0.1:8081';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(req,timeout=10)as r:assert r.status==200
def get(path):
    with client.open(base+path,timeout=15)as r:return json.load(r)
state=get('/api/companion/state');checked=0;named=0;fallback=0;assistant=0
for summary in state['scopes']:
    scope=get('/api/companion/memory/'+summary['id'])
    for turn in scope['messages']:
        if turn['role']=='assistant':
            assert turn.get('sender_name') and turn.get('sender_name_source')=='character';assistant+=1;continue
        if turn['role']!='user'or not turn.get('sender'):continue
        key=(scope['thread'],turn['sender'])
        if key not in pairs:pairs[key]=expected(*key)
        name,source=pairs[key]
        checked+=1
        if source!='id':assert turn.get('sender_name')==name and turn.get('sender_name_source')==source;named+=1
        else:fallback+=1
updated=json.loads(memory.read_text())
def without_names(data):
    result=json.loads(json.dumps(data))
    for scope in result['scopes'].values():
        for turn in scope['messages']:turn.pop('sender_name',None);turn.pop('sender_name_source',None)
    return result
assert without_names(original)==without_names(updated),'Memory content, identities, scopes or counters changed during name backfill'
assert checked>0 and named>0
for scope in updated['scopes'].values():
    if scope['account']!='background-test-20261024' or scope['channel']!='wechat':continue
    for turn in scope['messages']:
        if turn['role']=='user'and (scope['thread'],turn.get('sender')) in pairs:
            name,source=pairs[(scope['thread'],turn['sender'])]
            if source!='id':assert turn.get('sender_name')==name
report={'objective_item':2,'native_exact_directory_lookups':True,'source_priority':['remark','room','nickname','id'],'conversation_speaker_pairs':len(pairs),'available_name_sources':dict(coverage),'user_turns_checked':checked,'named_user_turns':named,'fallback_user_turns':fallback,'named_assistant_turns':assistant,'names_persisted':True,'original_text_identities_times_notes_and_claims_unchanged':True,'private_baseline_retained':True,'new_wechat_sends':0,'model_calls':0,'contacts_changed':0,'docker_images_built_or_exported':0}
(workspace/'research/evidence/goal1-step2-live-memory.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
