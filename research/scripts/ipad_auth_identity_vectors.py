"""Independent static-formula examples for baseinfo.IOSUuid, not an oracle."""
import hashlib
import json
from pathlib import Path

ROOT=Path(__file__).resolve().parents[2]

def auth_identity_pair(device_id):
    def encode(salt):
        text=hashlib.md5((device_id+salt).encode()).hexdigest()
        parts=(text[:8],text[2:6],text[3:7],text[1:5],text[20:32])
        return '-'.join(part.encode().hex() for part in parts)
    return encode('SM202003220432'),encode('BM202003220432')

examples=[]
for device_id in ['490102030405060708090a0b0c0d0e0f','49000000000000000000000000000000']:
    one,two=auth_identity_pair(device_id)
    assert len(one)==len(two)==68
    soft=('<softtype><k3>18.4.1</k3><k9>iPad</k9><k10>6</k10><k19>'+one+
          '</k19><k20>'+two+'</k20><k22>(null)</k22><k33>微信</k33><k47>1</k47>'+
          '<k50>1</k50><k51>com.tencent.xin</k51><k54>iPad15,6</k54><k61>2</k61></softtype>')
    examples.append({'synthetic_login_device_id':device_id,'auth_identity_one':one,'auth_identity_two':two,
                     'soft_type':soft,'soft_type_bytes':len(soft.encode()),'ad_source':one})
record={'authority':'Python model of static baseinfo.IOSUuid@7c7860 and SoftType construction; not observed original output',
        'distinct_from':'baseutils.IOSUuid@7c0a00 used by fingerprint/spam; different salts and format verbs',
        'examples':examples}
(ROOT/'research/evidence/ipad-auth-identity-vectors.json').write_text(json.dumps(record,indent=2,ensure_ascii=False)+'\n',encoding='utf-8')
print(json.dumps(record,indent=2,ensure_ascii=False))
