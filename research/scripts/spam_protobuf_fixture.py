"""Generate independent Python protobuf fixtures using recovered tag metadata."""
import json,pathlib
from google.protobuf import descriptor_pb2,descriptor_pool,message_factory,json_format
ROOT=pathlib.Path(__file__).resolve().parents[2]
schemas=json.loads((ROOT/'research/evidence/spam-schema.json').read_text())
fd=descriptor_pb2.FileDescriptorProto(name='recovered_spam.proto',package='recovered',syntax='proto2')
nums={'int32':5,'int64':3,'uint32':13,'uint64':4,'string':9}
for item in schemas:
    msg=fd.message_type.add(name=item['name'])
    for v in item['fields']:
        f=msg.field.add(name=v['name'],number=v['number'],label={'req':2,'opt':1,'rep':3}[v['presence']])
        typ=v['type'].removeprefix('[]').removeprefix('*')
        if typ in nums:f.type=nums[typ]
        else:f.type=11;f.type_name='.recovered.'+typ
msg=fd.message_type.add(name='SpamCheckData')
for name,n,typ in [('CRC',1,3),('Timestamp',2,3),('Body',3,12)]:msg.field.add(name=name,number=n,type=typ,label=1)
pool=descriptor_pool.DescriptorPool();pool.Add(fd)
classes={x.name:message_factory.GetMessageClass(pool.FindMessageTypeByName('recovered.'+x.name)) for x in fd.message_type}
def fill(name):
    msg=classes[name]();obj={}
    for f in msg.DESCRIPTOR.fields:
        if f.is_repeated:
            if f.message_type:
                nested,plain=fill(f.message_type.name);getattr(msg,f.name).add().CopyFrom(nested);obj[f.name]=[plain]
            else:getattr(msg,f.name).extend(['','重复项']);obj[f.name]=['','重复项']
        elif f.message_type:
            nested,plain=fill(f.message_type.name);getattr(msg,f.name).CopyFrom(nested);obj[f.name]=plain
        elif f.type==9:
            value='' if f.number%3==0 else 'field-'+str(f.number)+'-中文';setattr(msg,f.name,value);obj[f.name]=value
        else:
            value=0 if f.number%3==0 else ((-1 if f.number%2 else 1)*(f.number+1) if f.type in (3,5) else f.number+1)
            setattr(msg,f.name,value);obj[f.name]=value
    return msg,obj
vectors=[]
for name in [x['name'] for x in schemas]:
    msg,obj=fill(name);vectors.append({'type':name,'input':obj,'expected_hex':msg.SerializeToString().hex()})
target=ROOT/'internal/deviceproof/testdata';target.mkdir(exist_ok=True)
(target/'spam_protobuf.json').write_text(json.dumps({'authority':'Python google.protobuf dynamic proto2 encoder using frozen ABI tags; public synthetic inputs only','vectors':vectors},ensure_ascii=False,indent=2)+'\n',encoding='utf8')
print('Wrote',len(vectors),'independent dynamic protobuf fixtures.')
