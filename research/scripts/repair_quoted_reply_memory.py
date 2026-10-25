"""Inspect canonical quote messages; optionally repair their old derived placeholders.
Does not enqueue processing, call a model, or send any WeChat message.
"""
import hashlib,http.cookiejar,json,pathlib,sys,urllib.parse,urllib.request
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
token=(root/'api-token').read_text().strip()
def web(path,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+path,data=None if data is None else json.dumps(data).encode(),headers=headers),timeout=90)as r:return json.load(r)
def native(path):
    with client.open(urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024'+path,headers={'Authorization':'Bearer '+token}),timeout=10)as r:return json.load(r)
csrf=web('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
report={'canonical_quoted_replies':0,'with_user_text':0,'resolved_originals':0,'verified_bot_originals':0,'repaired_placeholders':0,'identities_and_times_preserved':True,'wechat_sends':0,'model_calls':0,'scopes':[]}
for scope in web('/api/companion/state')['scopes']:
    before=web('/api/companion/memory/'+scope['id'])
    quotes=[]
    for turn in before['messages']:
        if turn['role']!='user' or not turn.get('message_id') or not turn['text'].startswith('收到了一条分享卡片'):continue
        msg=native('/messages/'+urllib.parse.quote(turn['message_id'],safe=''))
        if msg.get('appMessageType')!=57:continue
        report['canonical_quoted_replies']+=1
        report['with_user_text']+=bool(msg.get('body','').strip())
        quote=native('/messages/'+urllib.parse.quote(turn['message_id'],safe='')+'/quote')
        original=quote.get('message')or{}
        valid=quote.get('state')=='resolved' and quote.get('threadId')==scope['thread'] and original.get('threadId')==scope['thread'] and original.get('serverId')==(msg.get('quote')or{}).get('serverId')
        report['resolved_originals']+=valid
        report['verified_bot_originals']+=valid and original.get('direction')=='outgoing'
        quotes.append((turn['message_id'],msg.get('body','')))
    if not quotes:continue
    if '--repair' in sys.argv:
        identities=[(x.get('message_id'),x.get('sender'),x.get('time'))for x in before['messages']]
        web('/api/companion/memory/'+scope['id']+'/refresh-shares',{},csrf)
        after=web('/api/companion/memory/'+scope['id'])
        assert identities==[(x.get('message_id'),x.get('sender'),x.get('time'))for x in after['messages']], 'memory changed while repairing; inspect before further changes'
        by_id={x.get('message_id'):x for x in after['messages']if x['role']=='user'}
        for mid,body in quotes:
            assert body and by_id[mid]['text'].startswith(body) and not by_id[mid]['text'].startswith('收到了一条分享卡片')
            report['repaired_placeholders']+=1
    report['scopes'].append({'scope':scope['id'],'quoted_placeholders':len(quotes)})
name='quoted-reply-memory-repaired.json'if '--repair'in sys.argv else'quoted-reply-canonical-inspection.json'
(workspace/'research/evidence'/name).write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
