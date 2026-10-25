"""Synthetic fingerprint/token protobuf fixtures, independently encoded.

TrustData.si tag2 is present in the pinned Go ABI but absent in the older
embedded descriptor. The one-field extension is explicit before encoding.
"""
import pathlib,json,hashlib
from google.protobuf import descriptor_pool,message_factory,descriptor_pb2
ROOT=pathlib.Path(__file__).resolve().parents[2]
raw=(ROOT/'research/evidence/send-mm-descriptor.bin').read_bytes()
file=descriptor_pb2.FileDescriptorProto.FromString(raw)
trust=next(m for m in file.message_type if m.name=='TrustData')
assert all(f.number!=2 for f in trust.field)
si=trust.field.add();si.name='si';si.number=2;si.label=1;si.type=9
pool=descriptor_pool.DescriptorPool();pool.AddSerializedFile(file.SerializeToString())
def msg(name):return message_factory.GetMessageClass(pool.FindMessageTypeByName('mm.'+name))()
t=msg('TrustReq')
for key,val in [('deviceid','synthetic-device'),('empty','')]:
    a=t.td.tdi.add();a.Key=key;a.val=val
t.td.si='synthetic-si';t.md='synthetic-md'
f=msg('FPFresh')
b=f.baseRequest;b.sessionKey=b'';b.uin=0;b.deviceId=b'synthetic-id\0';b.clientVersion=402668320;b.deviceType=b'iPad Air iPadOS18.4.1';b.scene=0
f.SessKey=b'0123456789abcdef'
z=f.Ztdata;z.Version='00000008\0';z.Encrypted=1;z.Data=b'synthetic-encrypted-trust';z.TimeStamp=123456;z.Optype=5;z.Uin=0
r=msg('TrustResponse');r.BaseResponse.ret=0
d=r.TrustResponseData;d.deviceToken='synthetic-device-token';d.timeStamp=123457;d.softData.softConfig='synthetic-config';d.softData.softData=b'synthetic-soft-data'
target=ROOT/'internal/loginwire/testdata';target.mkdir(exist_ok=True)
for name,item in [('trust-request',t),('fingerprint-request',f),('trust-response',r)]:
    (target/(name+'.pb')).write_bytes(item.SerializeToString(deterministic=True))
(target/'token-fixtures.json').write_text(json.dumps({'authority':'independent Python protobuf synthetic fixtures; no actual device proof','descriptor_sha256':hashlib.sha256(raw).hexdigest(),'explicit_abi_extension':'mm.TrustData.si optional string tag2'},indent=2),encoding='utf-8')
print('Saved synthetic token protobuf fixtures, including explicit ABI schema extension.')
