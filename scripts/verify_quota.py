# -*- coding: utf-8 -*-
"""
配额获取连通性 + 解析演示脚本（对齐项目方案中的显示逻辑）。

- 读取本地凭据 access_token -> 请求 https://api.kimi.com/coding/v1/usages
- 解析 5h / 7d 两个窗口（对齐 kimi-code-hud 的 quotaValues / deriveWindowLabel）
- 输出: 柱条 + 百分比 + 倒计时 + 绿/黄/红分级
敏感 token 一律掩码，不落盘。
"""
import json
import math
import os
import sys
import time
import urllib.request
import urllib.error
from datetime import datetime, timezone

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")

CRED_PATH = os.path.join(
    os.environ.get("USERPROFILE", ""), ".kimi-code", "credentials", "kimi-code.json"
)
USAGES_URL = "https://api.kimi.com/coding/v1/usages"
BAR_WIDTH = 10


def mask(v):
    if isinstance(v, str) and len(v) > 8:
        return v[:4] + "..." + v[-4:]
    return v


def to_num(v):
    try:
        n = float(v)
        return n if math.isfinite(n) else None
    except (TypeError, ValueError):
        return None


def window_label(duration, time_unit):
    """deriveWindowLabel: 300min->5h, 1440min->1d, TIME_UNIT_HOUR->{n}h, TIME_UNIT_DAY->{n}d"""
    d = to_num(duration)
    if d is None or d <= 0:
        return None
    t = time_unit or ""
    if "MINUTE" in t:
        if d % 1440 == 0:
            return f"{int(d // 1440)}d"
        if d % 60 == 0:
            return f"{int(d // 60)}h"
        return f"{int(d)}m"
    if "HOUR" in t:
        if d % 24 == 0:
            return f"{int(d // 24)}d"
        return f"{int(d)}h"
    if "DAY" in t:
        return f"{int(d)}d"
    return None


def countdown(reset_iso, now_ts):
    """~2h18m / ~3d2h / 已过期为 ~reset"""
    try:
        reset = datetime.fromisoformat(reset_iso.replace("Z", "+00:00"))
        remain = int(reset.timestamp() - now_ts)
    except Exception:
        return "~?"
    if remain <= 0:
        return "~reset"
    if remain >= 86400:
        return f"~{remain // 86400}d{(remain % 86400) // 3600}h"
    if remain >= 3600:
        return f"~{remain // 3600}h{remain % 3600 // 60}m"
    return f"~{max(1, remain // 60)}m"


def level(frac):
    """<0.6 绿 / <0.85 黄 / >=0.85 红"""
    if frac >= 0.85:
        return "红"
    if frac >= 0.6:
        return "黄"
    return "绿"


def bar(frac):
    full = round(BAR_WIDTH * frac)
    return "█" * full + "░" * (BAR_WIDTH - full)


def main():
    # ---- 1. 读凭据（掩码打印） ----
    print("== 1. 读取本地凭据 ==")
    if not os.path.isfile(CRED_PATH):
        print(f"凭据文件不存在: {CRED_PATH}")
        raise SystemExit(1)
    with open(CRED_PATH, encoding="utf-8") as f:
        cred = json.load(f)
    token = cred.get("access_token")
    print(f"  凭据文件: {CRED_PATH}")
    print(f"  access_token: {mask(token)} (len={len(token)})")
    print(f"  expires_at: {cred.get('expires_at')} -> 剩余 {cred.get('expires_at', 0) - int(time.time())}s")
    print(f"  refresh_token: 存在={bool(cred.get('refresh_token'))}")

    # ---- 2. 请求 API ----
    print("\n== 2. 请求配额 API ==")
    req = urllib.request.Request(
        USAGES_URL,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=8) as resp:
            raw = json.loads(resp.read().decode("utf-8"))
            print(f"  HTTP {resp.status} OK")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        print(f"  HTTP 错误: {e.code}")
        print(f"  响应体: {body[:500]}")
        raise SystemExit(2)
    except Exception as e:
        print(f"  请求异常: {type(e).__name__}: {e}")
        raise SystemExit(2)

    # ---- 3. 解析窗口 ----
    now_ts = int(time.time())
    print("\n== 3. 解析窗口 ==")

    # 顶层 usage -> 7d weekly
    usage = raw.get("usage") or {}
    w7_used = to_num(usage.get("used"))
    w7_limit = to_num(usage.get("limit"))
    if w7_used is None and usage.get("remaining") is not None:
        w7_used = w7_limit - to_num(usage.get("remaining"))
    if w7_used is not None and w7_limit:
        w7_used = max(0.0, min(w7_used, w7_limit))

    windows = []  # (label, used, limit, frac, resetTime)
    if w7_used is not None and w7_limit:
        windows.append(("7d", w7_used, w7_limit, w7_used / w7_limit, usage.get("resetTime")))

    for item in raw.get("limits") or []:
        w = item.get("window") or {}
        det = item.get("detail") or {}
        label = window_label(w.get("duration"), w.get("timeUnit"))
        limit = to_num(det.get("limit"))
        used = to_num(det.get("used"))
        if used is None and det.get("remaining") is not None:
            used = limit - to_num(det.get("remaining")) if limit is not None else None
        if used is not None and limit:
            used = max(0.0, min(used, limit))
            windows.append((label or "?", used, limit, used / limit, det.get("resetTime")))

    if not windows:
        print("  未解析到任何配额窗口，原始响应: ", json.dumps(raw)[:500])
        raise SystemExit(3)

    print(f"  解析到 {len(windows)} 个窗口: {[w[0] for w in windows]}")
    print("\n== 4. 配额展示（对齐托盘菜单） ==")
    for label, used, limit, frac, reset in windows:
        print(
            f"  {label:<4} {bar(frac)} {frac * 100:5.1f}%  "
            f"({used:.0f}/{limit:.0f})  {countdown(reset, now_ts)}  [{level(frac)}]"
        )
    print("\n== 测试完成: 配额 API 连接正常，数据可解析 ==")


if __name__ == "__main__":
    main()
