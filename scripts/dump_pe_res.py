# -*- coding: utf-8 -*-
"""完整 dump PE 的 RT_GROUP_ICON(14)/RT_ICON(3) 资源树与数据，验证 LoadImage 可识别性。
"""
import struct
import sys

def build_rsrc(data, pe_off):
    size_opt = struct.unpack_from('<H', data, pe_off + 20)[0]
    opt_off = pe_off + 24
    dd_off = opt_off + size_opt - 16 * 8
    res_rva, res_sz = struct.unpack_from('<II', data, dd_off + 2 * 8)
    if res_rva == 0:
        return None, None
    sect_off = opt_off + size_opt
    nsec = struct.unpack_from('<H', data, pe_off + 6)[0]
    sects = []
    for i in range(nsec):
        sec = sect_off + i * 40
        vs, va, rawsz, raw = struct.unpack_from('<IIII', data, sec + 8)
        sects.append((va, vs, rawsz, raw))
    def rva_to_off(rva):
        for va, vs, rawsz, raw in sects:
            if va <= rva < va + max(rawsz, vs):
                return raw + (rva - va)
        return None
    return rva_to_off, res_rva

def parse_dir(data, base_rva, rva_to_off, off, depth=0):
    """解析资源目录，返回 [(name_or_id, is_string, 'dir'|'data', sub_off_or_data_off)]"""
    n_named = struct.unpack_from('<H', data, off + 12)[0]
    n_id = struct.unpack_from('<H', data, off + 14)[0]
    print('  [dir] off=%x named=%d id=%d head=%s' % (off, n_named, n_id, data[off:off+16].hex()))
    entries = []
    for i in range(n_named + n_id):
        e = off + 16 + i * 8
        name_id, off2 = struct.unpack_from('<II', data, e)
        print('  [entry] name_id=%08x off2=%08x' % (name_id, off2))
        is_string = bool(name_id & 0x80000000)
        name = None
        if is_string:
            noff = base_rva + (name_id & 0x7FFFFFFF)
            noff = rva_to_off(noff)
            ln = struct.unpack_from('<H', data, noff)[0]
            name = data[noff + 2:noff + 2 + ln * 2].decode('utf-16le', 'replace')
        is_data = bool(off2 & 0x80000000)
        sub = base_rva + (off2 & 0x7FFFFFFF)
        sub = rva_to_off(sub)
        if is_data:
            entries.append((name, name_id, 'data', sub))
        else:
            entries.append((name, name_id, 'dir', sub))
    return entries

def main():
    path = sys.argv[1]
    with open(path, 'rb') as f:
        data = f.read()
    pe_off = struct.unpack_from('<I', data, 0x3C)[0]
    rva_to_off, res_rva = build_rsrc(data, pe_off)
    root = rva_to_off(res_rva)
    print('root dir off =', hex(root))
    types = parse_dir(data, res_rva, rva_to_off, root)
    for tname, tid, tkind, toff in types:
        tname_s = tname if tname else ('ID=%d' % (tid & 0xFFFF))
        print(f'类型 {tname_s}: {tkind} off={hex(toff)}')
        if tkind != 'dir':
            continue
        for nm, nid, nkind, noff in parse_dir(data, res_rva, rva_to_off, toff):
            nm_s = nm if nm else ('ID=%d' % (nid & 0xFFFF))
            print(f'  名称 {nm_s}: {nkind} off={hex(noff)}')
            if nkind != 'dir':
                continue
            for lm, lid, lkind, loff in parse_dir(data, res_rva, rva_to_off, noff):
                lm_s = lm if lm else ('ID=%d' % (lid & 0xFFFF))
                print(f'    语言 {lm_s}: {lkind} off={hex(loff)}')
                if lkind == 'data':
                    # dump 数据头
                    size, rva = struct.unpack_from('<II', data, loff)
                    doff = rva_to_off(rva)
                    print(f'      数据 size={size} rva={hex(rva)} doff={hex(doff)} head={data[doff:doff+16].hex()}')

if __name__ == '__main__':
    main()
