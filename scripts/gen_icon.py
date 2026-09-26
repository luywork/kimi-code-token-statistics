# -*- coding: utf-8 -*-
"""手动构造多帧 tray.ico（ICONDIR + PNG 帧，Windows Vista+ 支持 PNG 压缩 ICO）。
绕开 Pillow 12 只写第一帧的 ICO 保存 bug。
"""
import io
import os
import struct
from PIL import Image, ImageDraw

SIZES = [16, 20, 24, 32, 48, 64, 128, 256]


def make_icon(size: int) -> Image.Image:
    S = 256
    base = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    bd = ImageDraw.Draw(base)
    bd.ellipse([8, 8, S - 8, S - 8], fill=(0x1E, 0x88, 0xE5, 255))
    bd.ellipse([40, 150, 216, 246], fill=(0x2F, 0x9B, 0xF3, 90))
    bolt = [(158, 16), (72, 148), (128, 148), (98, 240), (200, 106), (140, 106), (174, 16)]
    bd.polygon(bolt, fill=(255, 255, 255, 255))
    return base.resize((size, size), Image.LANCZOS)


def png_bytes(img: Image.Image) -> bytes:
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


def main() -> None:
    frames = []
    for s in SIZES:
        img = make_icon(s)
        data = png_bytes(img)
        frames.append((s, data))
        print(f"帧 {s}x{s}: {len(data)} bytes")

    out = os.path.join(os.environ["USERPROFILE"], ".kimi-code-hud", "tray.ico")
    os.makedirs(os.path.dirname(out), exist_ok=True)

    # ICONDIR
    header = struct.pack("<HHH", 0, 1, len(frames))
    entries = b""
    images = b""
    offset = 6 + 16 * len(frames)
    for s, data in frames:
        w = 0 if s == 256 else s  # 256 用 0 表示
        h = 0 if s == 256 else s
        entries += struct.pack("<BBBBHHII", w, h, 0, 0, 1, 32, len(data), offset)
        images += data
        offset += len(data)

    with open(out, "wb") as f:
        f.write(header + entries + images)

    # 校验
    img = Image.open(out)
    n = getattr(img, "n_frames", 1)
    print("写回帧数:", n)
    for i in range(n):
        img.seek(i)
        print(f"  {i}: {img.size}")
    print("大小:", os.path.getsize(out), "bytes")
    print("已生成:", out)


if __name__ == "__main__":
    main()
