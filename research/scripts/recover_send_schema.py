"""Read the mm.proto descriptor from the pinned ELF. Never executes target code."""
import hashlib, json, pathlib, struct
from google.protobuf import descriptor_pb2, json_format

ROOT = pathlib.Path(__file__).resolve().parents[1]
SAMPLE = ROOT / 'samples/main.elf.bin'
b = SAMPLE.read_bytes()
phoff = struct.unpack_from('<Q', b, 32)[0]
entsz, count = struct.unpack_from('<HH', b, 54)
segments = []
for i in range(count):
    kind, flags, off, va, pa, fs, ms, al = struct.unpack_from('<II6Q', b, phoff+i*entsz)
    if kind == 1:
        segments.append((va, va+fs, off))
def read(va, size):
    for start, end, off in segments:
        if start <= va and va+size <= end:
            return b[off+va-start:off+va-start+size], off+va-start
    raise ValueError(f'unmapped file range: {va:x}+{size:x}')

# Address established by file_mm_proto_init, loads at 0x71f763 and 0x71f76a.
header, _ = read(0x1a89810, 24)
ptr, length, capacity = struct.unpack('<QQQ', header)
assert 0 < length <= capacity < 4_000_000
raw, offset = read(ptr, length)
fd = descriptor_pb2.FileDescriptorProto()
fd.ParseFromString(raw)
assert fd.name == 'mm.proto'
assert any(m.name == 'NewSendMsgRequest' for m in fd.message_type)
evidence = ROOT/'evidence'
(evidence/'send-mm-descriptor.bin').write_bytes(raw)
(evidence/'send-mm-descriptor.json').write_text(json_format.MessageToJson(fd), encoding='utf8')
by_name = {'.'+fd.package+'.'+m.name: m for m in fd.message_type}
wanted = {'.'+fd.package+'.'+n for n in ('NewSendMsgRequest','NewSendMsgRespone')}
todo = list(wanted)
while todo:
    message = by_name[todo.pop()]
    for field in message.field:
        if field.type == field.TYPE_MESSAGE and field.type_name not in wanted:
            assert field.type_name in by_name, field.type_name
            wanted.add(field.type_name)
            todo.append(field.type_name)
summary = {
    'authority':'ELF-embedded protobuf descriptor, offline extraction; no runtime equivalence claim',
    'artifact_sha256':hashlib.sha256(b).hexdigest(),
    'descriptor_slice_header_address':'0x1a89810','descriptor_address':hex(ptr),
    'descriptor_file_offset':offset,'descriptor_bytes':length,
    'descriptor_sha256':hashlib.sha256(raw).hexdigest(),
    'file':fd.name,'package':fd.package,'syntax':fd.syntax or 'proto2',
    'messages':{name:json_format.MessageToDict(by_name[name]) for name in sorted(wanted)},
}
(evidence/'send-schema.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(summary,ensure_ascii=False,indent=2))
