# -*- coding: utf-8 -*-
"""生成 go-winres 嵌入 exe 用的图标 PNG（任务管理器/资源管理器显示）。
从运行时 tray.ico 提取 256 帧并另存为 PNG；go-winres 不接受 PNG 压缩帧 ICO。
"""
import os
import sys
from PIL import Image

def main() -> None:
    src = os.path.join(os.environ["USERPROFILE"], ".kimi-code-hud", "tray.ico")
    out_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "cmd", "kimi-hud", "winres")
    out_dir = os.path.normpath(out_dir)
    os.makedirs(out_dir, exist_ok=True)

    img = Image.open(src)
    # 找最大帧（256）作为高质量主图，另生成 32/48 小尺寸（系统常见显示尺寸）。
    frames = []
    for i in range(getattr(img, "n_frames", 1)):
        img.seek(i)
        frames.append((img.size[0], img.convert("RGBA").copy()))
    frames.sort(key=lambda x: x[0])
    largest = frames[-1][1]

    for size in (256, 48, 32, 16):
        frame = largest.resize((size, size), Image.LANCZOS) if size != largest.width else largest
        path = os.path.join(out_dir, f"icon_{size}.png")
        frame.save(path, format="PNG")
        print("已生成:", path, frame.size)

if __name__ == "__main__":
    main()
