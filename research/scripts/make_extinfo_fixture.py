"""Synthetic fixture only: independent Python protobuf container encoding."""
import pathlib,json,hashlib
from google.protobuf import descriptor_pool,message_factory
ROOT=pathlib.Path(__file__).resolve().parents[2]
raw=(ROOT/'research/evidence/send-mm-descriptor.bin').read_bytes()
pool=descriptor_pool.DescriptorPool();pool.AddSerializedFile(raw)
def msg(name):return message_factory.GetMessageClass(pool.FindMessageTypeByName('mm.'+name))()
c=msg('CryptoData');c.version=b'00000008';c.type=1;c.encryptData=b'synthetic-encrypted-device-data';c.timestamp=123456;c.unknown5=5;c.unknown6=0
t=msg('DeviceToken');t.Version='';t.Encrypted=1;t.Data.string='synthetic-token';t.TimeStamp=123457;t.Optype=2;t.Uin=0
w=msg('WCExtInfo')
for field,data in [('wcstf',b'synthetic-wcstf'),('wcste',b'synthetic-wcste'),('ccData',c.SerializeToString(deterministic=True)),('deviceToken',t.SerializeToString(deterministic=True))]:
    buf=getattr(w,field);buf.iLen=len(data);buf.buffer=data
assert t.HasField('Version') and t.HasField('Uin') and c.HasField('unknown6')
target=ROOT/'internal/authwire/testdata';target.mkdir(exist_ok=True)
(target/'extinfo-v8.pb').write_bytes(w.SerializeToString(deterministic=True))
(target/'extinfo-v8.json').write_text(json.dumps({'authority':'synthetic independent Python protobuf fixture; no device token generated or used','descriptor_sha256':hashlib.sha256(raw).hexdigest(),'explicit_zero_and_empty_fields_preserved':True},indent=2),encoding='utf-8')
print('Saved synthetic ExtInfoV8 encoding fixture; no network or original target execution.')
