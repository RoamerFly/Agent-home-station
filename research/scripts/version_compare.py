"""Bounded, offline comparison of two ELF profiles. Never executes either ELF.

The expected Client.Init address comes from the separately recovered Go
function tables. RIP-relative loads are checked before following their targets;
file offsets are resolved separately for each ELF's PT_LOAD segments.
"""
import hashlib
import json
import pathlib
import struct

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = ROOT / 'research/evidence/version-update'


class ELF:
    def __init__(self, path):
        self.path = path
        self.data = path.read_bytes()
        assert self.data[:6] == b'\x7fELF\x02\x01'
        phoff = struct.unpack_from('<Q', self.data, 32)[0]
        size, count = struct.unpack_from('<HH', self.data, 54)
        self.segments = []
        for i in range(count):
            kind, flags, pos, va, pa, fs, ms, align = struct.unpack_from('<II6Q', self.data, phoff + i * size)
            if kind == 1:
                self.segments.append((va, va + fs, pos))

    def read(self, address, length):
        for start, end, offset in self.segments:
            if start <= address and address + length <= end:
                return self.data[offset + address - start:offset + address - start + length]
        raise ValueError(hex(address))

    def rip(self, address, opcode):
        insn = self.read(address, len(opcode) + 4)
        assert insn.startswith(opcode), 'revisit changed instruction at ' + hex(address)
        return address + len(insn) + struct.unpack('<i', insn[-4:])[0]

    def profile(self):
        version_address = self.rip(0x7d16ca, b'\x8b\x0d')
        device_address = self.rip(0x7d16da, b'\x48\x8b\x35')
        key_address = self.rip(0x7d1702, b'\x48\x8d\x05')
        # The adjacent instruction gives the literal's exact byte length.
        assert self.read(0x7d1709, 5) == b'\xbb\x82\x00\x00\x00'
        ptr, length = struct.unpack('<QQ', self.read(device_address, 16))
        assert length < 256
        version = struct.unpack('<I', self.read(version_address, 4))[0]
        key = self.read(key_address, 130).decode('ascii')
        assert len(bytes.fromhex(key)) == 65
        return {'client_init': '0x7d1520..0x7d1f40',
                'version_load': '0x7d16ca', 'version_address': hex(version_address),
                'client_version': version, 'client_version_hex': hex(version),
                'device_type_address': hex(device_address),
                'device_type': self.read(ptr, length).decode('utf-8'),
                'bootstrap_address': hex(key_address), 'bootstrap_public_key_hex': key,
                'client_init_sha256': hashlib.sha256(self.read(0x7d1520, 0xa20)).hexdigest()}

    def section_hashes(self):
        off = struct.unpack_from('<Q', self.data, 40)[0]
        size, count, strings = struct.unpack_from('<HHH', self.data, 58)
        headers = [struct.unpack_from('<II4QII2Q', self.data, off + i * size) for i in range(count)]
        names = headers[strings]
        names = self.data[names[4]:names[4] + names[5]]
        result = {}
        for h in headers:
            name = names[h[0]:].split(b'\0', 1)[0].decode('ascii')
            if name in ('.text', '.rodata', '.data', '.noptrdata', '.gopclntab'):
                result[name] = {'bytes': h[5], 'va': hex(h[3]), 'file_offset': hex(h[4]),
                                'sha256': hashlib.sha256(self.data[h[4]:h[4] + h[5]]).hexdigest()}
        return result


old = ELF(ROOT / 'research/samples/main.elf.bin')
new = ELF(OUT / 'main.elf.bin')
report = {'method': 'per-artifact PT_LOAD mapping and checked Client.Init RIP-relative instructions; no execution',
          'old_sha256': hashlib.sha256(old.data).hexdigest(),
          'new_sha256': hashlib.sha256(new.data).hexdigest(),
          'old_profile': old.profile(), 'new_profile': new.profile(),
          'old_sections': old.section_hashes(), 'new_sections': new.section_hashes()}
tuple_fields = ('client_version', 'device_type', 'bootstrap_public_key_hex')
report['compared_profile_fields'] = list(tuple_fields)
report['static_profile_equal'] = all(report['old_profile'][k] == report['new_profile'][k] for k in tuple_fields)
report['matching_sections'] = [k for k, v in report['old_sections'].items()
                               if v['sha256'] == report['new_sections'][k]['sha256']]
(OUT / 'profile-comparison.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
print(json.dumps({'static_profile_equal': report['static_profile_equal'],
                  'version': report['new_profile']['client_version_hex'],
                  'matching_sections': report['matching_sections']}, indent=2))
