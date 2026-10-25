"""Independent Python full-body static model with public synthetic inputs.

Uses google.protobuf for serialization. String wire fields are modeled as bytes
to retain the selected client's possibly non-UTF8 EncodeString output exactly.
Only rqtx uses a previously retained isolated native vector; no target execution.
"""
import datetime,hashlib,json,pathlib,struct,zlib
from google.protobuf import descriptor_pb2,descriptor_pool,message_factory
ROOT=pathlib.Path(__file__).resolve().parents[2]
schema=json.loads((ROOT/'research/evidence/spam-schema.json').read_text())
fd=descriptor_pb2.FileDescriptorProto(name='spam_bytes.proto',package='golden',syntax='proto2')
scalar={'int32':5,'int64':3,'uint32':13,'uint64':4,'string':12}
for msg in schema:
    m=fd.message_type.add(name=msg['name'])
    for v in msg['fields']:
        f=m.field.add(name=v['name'],number=v['number'],label={'req':2,'opt':1,'rep':3}[v['presence']]);typ=v['type'].lstrip('[]*')
        if typ in scalar:f.type=scalar[typ]
        else:f.type=11;f.type_name='.golden.'+typ
pool=descriptor_pool.DescriptorPool();pool.Add(fd)
def message(name,values):
    m=message_factory.GetMessageClass(pool.FindMessageTypeByName('golden.'+name))()
    for key,value in values.items():
        f=m.DESCRIPTOR.fields_by_name[key]
        if f.is_repeated:
            if f.message_type:
                for x in value:getattr(m,key).add().CopyFrom(message(f.message_type.name,x))
            else:getattr(m,key).extend(value)
        elif f.message_type:getattr(m,key).CopyFrom(message(f.message_type.name,value))
        else:setattr(m,key,value)
    return m
mask64=(1<<64)-1
def dilate(x):return sum(((x>>i)&1)<<(2*i) for i in range(32))
def rol(x,n,bits):
    n%=bits;return ((x<<n)|(x>>(bits-n)))&((1<<bits)-1)
def encode64(x,key,sec):
    words=[x&0xffffffff,(x>>32)&0xffffffff]
    return sum(rol(rol(word,i+1,32)^rol(sec,-i,32)^rol(key,i,32),i+1,32)<<(32*i) for i,word in enumerate(words))
def encode_str(s,key,sec):
    s=s.encode();out=bytearray()
    n=len(s)//4
    for i in range(n):out.extend(struct.pack('<I',rol(rol(int.from_bytes(s[i*4:i*4+4],'little'),i+1,32)^rol(sec,-i,32)^rol(key,i,32),i+1,32)))
    for i,x in enumerate(s[4*n:]):out.append(rol(rol(x,i+1,8)^rol(sec&255,-i,8)^rol(key&255,i,8),i+1,8))
    return bytes(out)
def ident(salt):
    s=hashlib.md5(('public-offline-login'+salt).encode()).hexdigest();return s[:8]+'-'+s[2:6]+'-'+s[3:7]+'-'+s[1:5]+'-'+s[20:]
def stat(sec):
    return {'Inode':11,'Access':{'Seconds':sec,'Nanos':11},'Modified':{'Seconds':sec+1,'Nanos':29},'Changed':{'Seconds':sec+2,'Nanos':123},'Birth':{'Seconds':sec-100,'Nanos':((1<<63)+91)}}
p={'Schema':'wechat-ipad-v8-profile-1','DeviceID':'abcdef0123456789abcdef0123456789','LoginDeviceID':'public-offline-login','IDFV':ident('SM2020032204320'),'IDFA':ident('BM2020032204321'),'SDI':'0'*32,'GUID1':'01234567-89AB-CDEF-0123-456789ABCDEF','GUID2':'FEDCBA98-7654-3210-FEDC-BA9876543210','DeviceName':'iPad Air (第7代)','Model':'iPad15,6','OSVersion':'18.4.1','DeviceType':'iPad Air iPadOS18.4.1','CarrierName':'中国电信','ClientVersion':402668320,'CoreCount':3,'InstallTime':1747000000,'BootTime':1792000000,'SystemVersion':stat(1700000000),'DyldCache':stat(1700000100),'Var':stat(1700000200),'EtcGroup':stat(1700000300),'EtcHosts':stat(1700000400),'Filesystem':{'Type':26,'Flags':1417728009,'FSID':19081375130,'TypeName':'apfs','MountOn':'/','MountFrom':'com.apple.os.update-{PUBLIC-OFFLINE}'}}
build={'HeadMD5':'d05a80a94b6c2e3c31424403437b6e18','IOSBuildVersion':'19H386','KernelType':'Darwin','KernelVersion':'21.6.0','KernelRelease':'Darwin Kernel Version 21.6.0: Sun Oct 15 00:17:39 PDT 2023; root:xnu-8020.241.42~8/RELEASE_ARM64_T7000'}
input={'Profile':p,'LoginDeviceID':p['LoginDeviceID'],'DeviceInfoName':'explicit-info-name','RomModel':'explicit-rom-model','StartTime':'2026-10-24T02:00:00.123456789Z','NowTime':'2026-10-24T02:00:01.123457789Z','ServerDeviceToken':'PUBLIC-OFFLINE-PLACEHOLDER-NOT-SERVER-ISSUED','Build':build}
sec=int(datetime.datetime(2026,10,24,2,tzinfo=datetime.timezone.utc).timestamp());xor=(sec*-19+7)&255
def x(s):return ''.join(chr(v^xor) for v in s.encode()).encode()
def u(v,k):return encode64(v&mask64,k,sec)
def s(v,k):return encode_str(v,k,sec)
files=json.loads((ROOT/'research/evidence/spam-fileinfo-literals.json').read_text())['items']
fileobjects=[];crcdata=b''
for i in range(0,len(files),2):
    path=files[i]['prefix']+p['GUID2']+files[i]['suffix'];uuid=files[i+1]['literal']
    fileobjects.append({'Filepath':x(path),'Fileuuid':x(uuid)});crcdata+=('/private'+path+uuid).encode()
rqt=dilate(next(v['output'] for v in json.loads((ROOT/'internal/rqtx/testdata/oracle-vectors.json').read_text())['vectors'] if v['input']==p['SDI']))
body={'UnKnown1':1,'TimeStamp':sec,'KeyHash':dilate(xor),'CoreCount':dilate(p['CoreCount']),'BatteryInfo':1,'NetworkName':[x('en0'),x('utun3')],'NetType':1,'GetVersion':dilate(p['ClientVersion']),'GetVersionFromPList':dilate(p['ClientVersion']),'Unknown5':0,'Unknown6':4,'Unknown7':4,'Unknown8':0,'Unknown9':0,'AppFileInfo':fileobjects,'IsModify':0,'RqtHash':rqt,'InstallTime':p['InstallTime'],'KernBootTime':p['BootTime'],'Unknown55':0,'RqtHash56':rqt,'Unknown57':0,'DeviceIdCrc':dilate(zlib.crc32(p['DeviceID'].encode())),'AppFileInfoCrc':str(dilate(zlib.crc32(crcdata))).encode(),'Unknown62':0x1051151051115115,'Si':b'','NowUsec':123456,'NowUsecScale':(123456*0x68323+17)&0xffffffff,'Now':(((sec+1)<<((123457)&31)|((sec+1)>>(32-((123457)&31))))^123457)&0xfffffc3f|0x40,'EmptyString':b'','IsCaptured':0}
texts={'Yes1':'yes','Yes2':'yes','IosVersion':p['OSVersion'],'DeviceType':input['DeviceInfoName'],'IdentifierForVendor':p['IDFV'],'AdvertisingIdentifier':p['IDFA'],'Carrier':p['CarrierName'],'AppBundleId':'com.tencent.xin','DeviceName':p['DeviceName'],'UserName':input['RomModel'],'Lang':'zh','Country':'CN','DocumentDir':'/var/mobile/Containers/Data/Application/'+p['GUID1']+'/Documents','HeadMD5':build['HeadMD5'],'AppUUID':p['IDFV'],'AppName':'微信','SshPath':'/usr/bin/ssh','TempTest':'/tmp/test.txt','Sdi':p['SDI'],'DeviceId':p['DeviceID'],'Unknown61':'2FFC7F6DFEEFFF2B3FFCA029','DeviceToken':input['ServerDeviceToken']}
body.update({k:x(v) for k,v in texts.items()})
for k in ['SyslogUUID','Unknown10','Unknown11','DevMD5','DevUser','DevPrefix','Unknown12']:body[k]=b''
for field,statname,key in [('SystemVersionCtime','SystemVersion',0xd91ca739),('DyldSharedCacheArm64Ctime','DyldCache',0xa3071842),('PrivateVarDirCTime','Var',0x5a5f1852)]:body[field]=u(p[statname]['Changed']['Seconds']*10**9,key)
f=p['Filesystem'];body['StatfsInfo']=[{'FType':u(f['Type'],0x99687b8a),'FFstypename':s(f['TypeName'],0xe2868e99),'FFlags':u(f['Flags'],0xc36fa9a2),'FMntonname':s(f['MountOn'],0xb62136b0),'FMntfromname':s(f['MountFrom'],0x47ee23c4),'FFsid':u(f['FSID'],0xa19a7ed5)}]
crcdata=struct.pack('<QQQ',f['Flags'],f['Type'],f['FSID'])+(f['TypeName']+f['MountOn']+f['MountFrom']).encode();body['StatfsCrc']=rol(rol(zlib.crc32(crcdata),1,32)^sec^0xc5b26fe3,1,32)
body['FixedOne']=u(1,0x75a9f3fa)
lists=json.loads((ROOT/'research/evidence/spam-later-literals.json').read_text());body['AppexList']=[s(v['value'],0x540cf80d) for v in lists if v['mask']=='0x540cf80d'];body['FrameworkList']=[s(v['value'],0x8c36ee2a) for v in lists if v['mask']=='0x8c36ee2a']
body['PrivateVarDir']={field:u(p['Var'][kind]['Seconds']*10**9+p['Var'][kind]['Nanos'],0x2d6aca52) for field,kind in [('StBirthtime','Birth'),('StCtime','Changed'),('StMtime','Modified')]}
body['WechatDocBirth']=u(p['InstallTime']*10**9+0x12241352,0x30c0d228)
for field,value,key in [('LAPolicyDeviceOwnerAuthentication',1,0xa0dac236),('LAPolicyDeviceOwnerAuthenticationWithBiometrics',1,0x6c964322),('ICloudLogin',1,0x92a52bce),('SandboxReceiptExist',0,0x236de3ff),('NSClassFromString',0,0x3aa9a3ce)]:body[field]=u(value,key)
for field,value,key in [('AppStoreReceiptURL','/private/var/mobile/Containers/Data/Application/'+p['GUID1']+'/StoreKit/Receipt',0x985b22aa),('IosBuildVersion',build['IOSBuildVersion'],0x554738e7),('KernelType',build['KernelType'],0xec1b9627),('DeviceModel',input['RomModel'],0x562e8bdc),('KernelVersion',build['KernelVersion'],0x318bc072),('KernelRelease',build['KernelRelease'],0x3b696b8d),('DevciceType',p['Model'],0x554738e7)]:body[field]=s(value,key)
wire=message('SpamBody',body).SerializeToString()
result={'authority':'Independent Python static full-body reconstruction plus google.protobuf encoder; rqtx value reused from retained isolated native fixture; not whole-function native parity or live acceptance','input':input,'expected_hex':wire.hex(),'sha256':hashlib.sha256(wire).hexdigest()}
(ROOT/'internal/deviceproof/testdata/spam_build.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
print('Full body reference',len(wire),'bytes',result['sha256'])
