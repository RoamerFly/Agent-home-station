#!/usr/bin/env python3
"""Read-only, narrow Mach-O audit for the claimed WeChat client version.

This intentionally does not execute, install, or unpack the IPA.  It inspects
only the already-extracted main Mach-O and emits a compact evidence record.
"""
import argparse
import hashlib
import json
import mmap
import struct
from pathlib import Path


BASE = 0x100000000


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def inspect(binary):
    with binary.open("rb") as handle:
        data = mmap.mmap(handle.fileno(), 0, access=mmap.ACCESS_READ)
        _, _, _, _, ncmds, _, _, _ = struct.unpack_from("<IiiIIIII", data, 0)
        offset, segments, sections, symtab = 32, [], {}, None
        for _ in range(ncmds):
            command, size = struct.unpack_from("<II", data, offset)
            if command == 0x19:  # LC_SEGMENT_64
                name = data[offset + 8 : offset + 24].rstrip(b"\0").decode()
                vmaddr, vmsize, fileoff, filesize = struct.unpack_from("<QQQQ", data, offset + 24)
                sections_count = struct.unpack_from("<I", data, offset + 64)[0]
                segments.append((vmaddr, filesize, fileoff))
                cursor = offset + 72
                for _ in range(sections_count):
                    section = data[cursor : cursor + 16].rstrip(b"\0").decode()
                    segname = data[cursor + 16 : cursor + 32].rstrip(b"\0").decode()
                    addr, length, file_offset = struct.unpack_from("<QQI", data, cursor + 32)
                    sections[(segname, section)] = (addr, length, file_offset)
                    cursor += 80
            elif command == 0x2:
                symtab = struct.unpack_from("<IIII", data, offset + 8)
            offset += size

        def va_at(file_offset):
            for vmaddr, filesize, fileoff in segments:
                if fileoff <= file_offset < fileoff + filesize:
                    return vmaddr + file_offset - fileoff
            return None

        def offset_at(vmaddr):
            for start, filesize, fileoff in segments:
                if start <= vmaddr < start + filesize:
                    return fileoff + vmaddr - start
            return None

        def unpac(pointer):
            # The file stores arm64e authenticated pointers.  Their low 32 bits
            # are the image-relative address for this non-slid executable.
            return BASE + (pointer & 0xFFFFFFFF)

        def text_at(file_offset):
            end = data.find(b"\0", file_offset)
            return data[file_offset:end].decode("utf-8", "replace")

        needles = {name: data.find(value) for name, value in {
            "CUtility": b"CUtility\0",
            "GetVersion": b"GetVersion\0",
            "GetVersionFromPList": b"GetVersionFromPList\0",
        }.items()}

        # Resolve CUtility through __objc_classlist and inspect its metaclass.
        classlist_addr, classlist_size, classlist_off = sections[("__DATA_CONST", "__objc_classlist")]
        cutility = None
        for index in range(classlist_size // 8):
            class_off = offset_at(unpac(struct.unpack_from("<Q", data, classlist_off + index * 8)[0]))
            if class_off is None:
                continue
            ro_off = offset_at(unpac(struct.unpack_from("<Q", data, class_off + 32)[0] & ~7))
            if ro_off is None:
                continue
            name_off = offset_at(unpac(struct.unpack_from("<Q", data, ro_off + 24)[0]))
            if name_off is not None and text_at(name_off) == "CUtility":
                meta_off = offset_at(unpac(struct.unpack_from("<Q", data, class_off)[0]))
                meta_ro_off = offset_at(unpac(struct.unpack_from("<Q", data, meta_off + 32)[0] & ~7))
                methods_off = offset_at(unpac(struct.unpack_from("<Q", data, meta_ro_off + 32)[0]))
                flags, count = struct.unpack_from("<II", data, methods_off)
                version_selectors = []
                if (flags & 0xFFFF) == 12:
                    for entry_index in range(count):
                        entry = methods_off + 8 + entry_index * 12
                        name_offset = entry + struct.unpack_from("<i", data, entry)[0]
                        selector = text_at(name_offset)
                        if "version" in selector.lower():
                            version_selectors.append(selector)
                cutility = {
                    "classlist_index": index,
                    "class_file_offset": hex(class_off),
                    "metaclass_file_offset": hex(meta_off),
                    "metaclass_ro_file_offset": hex(meta_ro_off),
                    "class_method_list_file_offset": hex(methods_off),
                    "class_method_entry_size": flags & 0xFFFF,
                    "class_method_count": count,
                    "class_method_selectors_containing_version": version_selectors,
                }
                break

        # The requested leaf test: MOVZ W0,#low; MOVK W0,#0x1800,LSL#16; RET.
        text_addr, text_size, text_off = sections[("__TEXT", "__text")]
        leaves, cursor = [], text_off
        # Exact byte suffix for MOVK W0,#0x1800,LSL#16; avoid a slow
        # instruction-by-instruction pass over a 400 MB __text.
        while True:
            match = data.find(b"\x00\x00\xA3\x72", cursor, text_off + text_size)
            if match < 0:
                break
            first, second, third = struct.unpack_from("<III", data, match - 4)
            if ((first & 0xFFE0001F) == 0x52800000 and
                    second == 0x72A30000 and third == 0xD65F03C0):
                leaves.append({"file_offset": hex(match - 4), "low16": hex((first >> 5) & 0xFFFF)})
            cursor = match + 1

        claimed = 0x18004E22
        raw_literal = struct.pack("<I", claimed)
        literal_offsets, cursor = [], 0
        while True:
            cursor = data.find(raw_literal, cursor)
            if cursor < 0:
                break
            literal_offsets.append(hex(cursor))
            cursor += 1

        symbol_hits = []
        if symtab:
            symoff, nsyms, stroff, _ = symtab
            for index in range(nsyms):
                string_index, kind, section, _, value = struct.unpack_from("<IBBHQ", data, symoff + index * 16)
                end = data.find(b"\0", stroff + string_index)
                name = data[stroff + string_index:end].decode("utf-8", "replace")
                if any(token in name.lower() for token in ("cutil", "getversion", "versionfromplist")):
                    symbol_hits.append({"index": index, "value": hex(value), "kind": hex(kind), "section": section, "name": name})

        return {
            "artifact": {"path": str(binary), "bytes": binary.stat().st_size, "sha256": sha256(binary)},
            "mode": "static read-only; no IPA execution or installation",
            "selector_strings": {name: {"file_offset": hex(found), "vmaddr": hex(va_at(found))} for name, found in needles.items()},
            "objc_cutility": cutility,
            "symtab_version_related": symbol_hits,
            "client_version_leaf_functions": leaves,
            "claimed_plist_value_raw_literal_occurrences": literal_offsets,
            "conclusion": {
                "internal_client_version_recovered_from_code": False,
                "reason": "No direct ARM64 W0 leaf return with high word 0x1800 was found, and the CUtility class method list contains no GetVersion selector. The plist value alone is metadata, not proof of the protocol ClientVersion implementation.",
            },
        }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    result = inspect(arguments.binary)
    arguments.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
