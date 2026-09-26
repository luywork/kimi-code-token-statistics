# -*- coding: utf-8 -*-
"""检查 PE 文件是否嵌入图标资源（RT_GROUP_ICON=14）与版本资源（RT_VERSION=16）。
解析 PE 资源目录树，纯标准库实现。
"""
import struct
import sys

# IMAGE_RESOURCE_DIRECTORY_ENTRY 名称/ID 判定
def rsrc_types(data, pe_off):
    size_opt = struct.unpack_from('<H', data, pe_off + 20)[0]
    opt_off = pe_off + 24
    magic = struct.unpack_from('<H', data, opt_off)[0]
    # 资源目录 RVA 位于数据目录表（可选头末尾的 16 项）第 2 项（index=2）
    dd_off = opt_off + size_opt - 16 * 8
    res_rva, res_sz = struct.unpack_from('<II', data, dd_off + 2 * 8)
    if res_rva == 0:
        return []
    # 节表
    sect_off = opt_off + size_opt
    nsec = struct.unpack_from('<H', data, pe_off + 6)[0]
    def rva_to_off(rva):
        for i in range(nsec):
            sec = sect_off + i * 40
            # IMAGE_SECTION_HEADER: VirtualSize@8, VirtualAddress@12,
            # SizeOfRawData@16, PointerToRawData@20
            vs, va, rawsz, raw = struct.unpack_from('<IIII', data, sec + 8)
            if va <= rva < va + max(rawsz, vs):
                return raw + (rva - va)
        return None

    off = rva_to_off(res_rva)
    if off is None:
        return []
    # 根目录：类型层
    n_entries = struct.unpack_from('<H', data, off + 12)[0] + struct.unpack_from('<H', data, off + 14)[0]
    types = []
    for i in range(n_entries):
        e_off = off + 16 + i * 8
        name_id, off2 = struct.unpack_from('<II', data, e_off)
        if name_id & 0x80000000:
            # 命名资源，跳过
            continue
        types.append(name_id)
    return types

def main():
    path = sys.argv[1]
    with open(path, 'rb') as f:
        data = f.read()
    pe_off = struct.unpack_from('<I', data, 0x3C)[0]
    if data[pe_off:pe_off+4] != b'PE\x00\x00':
        print('不是有效的 PE 文件')
        return 1
    types = rsrc_types(data, pe_off)
    names = {1: 'RT_CURSOR', 2: 'RT_BITMAP', 3: 'RT_ICON', 5: 'RT_DIALOG',
             6: 'RT_STRING', 9: 'RT_ACCELERATOR', 10: 'RT_RCDATA', 11: 'RT_MESSAGETABLE',
             14: 'RT_GROUP_ICON', 16: 'RT_VERSION', 24: 'RT_MANIFEST'}
    print('资源类型:', ', '.join(names.get(t, str(t)) for t in types))
    has_icon = 14 in types
    print('已嵌入图标(RT_GROUP_ICON):', '是' if has_icon else '否')
    print('已嵌入版本信息(RT_VERSION):', '是' if 16 in types else '否')
    return 0 if has_icon else 1

if __name__ == '__main__':
    sys.exit(main())
