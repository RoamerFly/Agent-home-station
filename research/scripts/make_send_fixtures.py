"""Generate golden wire fixtures using Google's protobuf runtime and the recovered descriptor.

These establish codec/schema agreement only, not original-program or WeChat acceptance.
"""
import hashlib, json, pathlib
from google.protobuf import descriptor_pool, message_factory

ROOT=pathlib.Path(__file__).resolve().parents[2]
raw=(ROOT/'research/evidence/send-mm-descriptor.bin').read_bytes()
pool=descriptor_pool.DescriptorPool()
pool.AddSerializedFile(raw)
def message(name):return message_factory.GetMessageClass(pool.FindMessageTypeByName('mm.'+name))()
request=message('NewSendMsgRequest')
request.cnt=1
request.info.toid.string='12345678@chatroom'
request.info.content='后台发送测试\nHello 🌏'
request.info.type=1
request.info.utc=1792771200
request.info.clientMsgId=1234567890123456789
# An absent MsgSource is the ordinary text path; preserve proto2 presence.
ack=message('NewSendMsgRespone')
ack.BaseResponse.ret=0
ack.Count=1
item=ack.List.add()
item.Ret=0
item.ToUsetName.string=request.info.toid.string
item.MsgId=123
item.ClientMsgid=request.info.clientMsgId
item.NewMsgId=9223372036854775000
target=ROOT/'internal/wechatwire/testdata'
target.mkdir(parents=True,exist_ok=True)
for name,msg in [('request',request),('ack',ack)]:
    data=msg.SerializeToString(deterministic=True)
    (target/(name+'.pb')).write_bytes(data)
metadata={
    'authority':'Google Python protobuf serialization against ELF-recovered descriptor; not live observations',
    'descriptor_sha256':hashlib.sha256(raw).hexdigest(),
    'recipient':request.info.toid.string,'content':request.info.content,
    'client_message_id':str(request.info.clientMsgId),'unix_time':request.info.utc,
    'server_message_id':str(item.NewMsgId),
}
(target/'metadata.json').write_text(json.dumps(metadata,ensure_ascii=False,indent=2),encoding='utf8')
print('Generated request/ack fixtures from recovered descriptor; no network or target execution.')
