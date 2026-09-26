# -*- coding: utf-8 -*-
"""验证手动构造的 tray.ico：解析 ICONDIR 条目。"""
import os
import struct

p = os.path.join(os.environ["USERPROFILE"], ".kimi-code-hud", "tray.ico")
with open(p, "rb") as f:
    data = f.read()

reserved, ico_type, count = struct.unpack("<HHH", data[:6])
print(f"reserved={reserved} type={ico_type} count={count}")
for i in range(count):
    w, h, colors, rsv, planes, bitcount, size, offset = struct.unpack(
        "<BBBBHHII", data[6 + 16 * i : 22 + 16 * i]
    )
    w = 256 if w == 0 else w
    h = 256 if h == 0 else h
    print(f"  条目{i}: {w}x{h} planes={planes} bitcount={bitcount} size={size} offset={offset}")
