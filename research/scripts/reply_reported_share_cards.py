"""Complete the explicitly requested replies through the authenticated API.

Stable keys recover the existing result on repeated invocation; they never
start a second model generation or resend an accepted reply.
"""
import hashlib,http.cookiejar,json,pathlib,time,urllib.error,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2];base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(route,body=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    try:
        with client.open(urllib.request.Request(base+route,data=None if body is None else json.dumps(body).encode(),headers=headers),timeout=210)as response:return json.load(response)
    except urllib.error.HTTPError as error:
        print('API error: '+error.read(2048).decode('utf8'),flush=True)
        raise
config=(root/'admin/config.json').read_bytes()
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
usage_before=call('/api/billing')
scopes={x['thread']:x['id']for x in call('/api/companion/state')['scopes']}
targets=json.loads((workspace/'research/evidence/incoming-share-card-structure.json').read_text())
ticket=call('/api/runtime/drain',{},csrf)['ticket'];report=[]
try:
    for attempt in range(600):
        if call('/api/runtime/drain?ticket='+urllib.parse.quote(ticket))['ready']:break
        if attempt%30==0:print('Waiting for current tasks without cancellation.',flush=True)
        time.sleep(1)
    else:raise RuntimeError('Worker busy; no manual reply submitted')
    for target in targets:
        original_key='owner-share-reply-20261025-'+str(target['app_message_type'])
        old=json.loads((root/'admin/tool-state.json').read_text())['sends'].get(original_key)
        assert old and old['status']['state']=='stopped'and old['status']['reason']=='provider_configuration_error'and not old['status'].get('id'),'Earlier provider validation must have stopped before model/send'
        failed_key=original_key+'-configured'
        native_url='http://127.0.0.1:6174/v1/accounts/background-test-20261024/sends/by-idempotency-key?key='+urllib.parse.quote(failed_key)
        try:
            with client.open(urllib.request.Request(native_url,headers={'Authorization':'Bearer '+(root/'api-token').read_text().strip()}),timeout=15)as response:
                raise RuntimeError('Earlier native intent exists; inspect its receipt before changing the request')
        except urllib.error.HTTPError as error:
            assert error.code==404,'Cannot establish native absence; no new send allowed'
        key=original_key+'-quoted'
        print('Rereading and replying once: '+('Channels in parent group'if target['app_message_type']==51 else'Bilibili in robot group'),flush=True)
        result=call('/api/companion/memory/'+scopes[target['thread']]+'/reply-share',{'message_id':target['message_id'],'idempotency_key':key},csrf)
        record={'thread':target['thread'],'app_message_type':target['app_message_type'],'original_message_id':target['message_id'],'idempotency_key':key,**result}
        after=call('/api/companion/memory/'+scopes[target['thread']])
        record['reply_in_memory']=bool(result.get('reply'))and any(x['role']=='assistant'and x['text']==result['reply']for x in after['messages'])
        report.append(record)
        (workspace/'research/evidence/manual-share-replies-20261025.json').write_text(json.dumps({'results':report,'config_unchanged':(root/'admin/config.json').read_bytes()==config,'phone_display_verified':False},ensure_ascii=False,indent=2),encoding='utf8')
        print(json.dumps({'thread':target['thread'],'status':result.get('status'),'reply':result.get('reply'),'reply_in_memory':record['reply_in_memory']},ensure_ascii=False),flush=True)
finally:
    call('/api/runtime/undrain',{'ticket':ticket},csrf)
usage_after=call('/api/billing');balance=call('/api/billing/balance',{},csrf)
status=call('/api/status')
usage_report={'before':usage_before['total'],'after':usage_after['total'],'real_manual_model_calls_counted':usage_after['total']['requests']>usage_before['total']['requests'],'balance_state':balance['state'],'balance_currency':[x['currency']for x in(balance.get('data')or{}).get('balance_infos',[])],'balance_values_redacted':True}
usage_path=workspace/'research/evidence/billing-live-20261025.json'
if not usage_path.exists():usage_path.write_text(json.dumps(usage_report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({'native_online':status['native']['connection']=='online','authenticated':status['native']['authenticated'],'bot':status['bot']['state'],'config_unchanged':(root/'admin/config.json').read_bytes()==config}),flush=True)
