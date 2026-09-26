import struct
import sys

def pe_subsystem(path):
    with open(path, 'rb') as f:
        data = f.read()
    # DOS header: e_lfanew at 0x3C
    pe_off = struct.unpack_from('<I', data, 0x3C)[0]
    if data[pe_off:pe_off+4] != b'PE\x00\x00':
        return None
    # COFF header at pe_off+4, optional header starts at pe_off+24
    opt_off = pe_off + 24
    magic = struct.unpack_from('<H', data, opt_off)[0]  # 0x10b PE32, 0x20b PE32+
    # Subsystem 字段在可选头偏移 68（PE32 与 PE32+ 相同）。
    subsystem = struct.unpack_from('<H', data, opt_off + 68)[0]
    names = {1: 'NATIVE', 2: 'WINDOWS_GUI', 3: 'WINDOWS_CUI'}
    return names.get(subsystem, f'UNKNOWN({subsystem})')

if __name__ == '__main__':
    for p in sys.argv[1:]:
        print(p, '->', pe_subsystem(p))
