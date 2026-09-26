# -*- coding: utf-8 -*-
"""测试能否通过本地凭据访问 Kimi 订阅配额 API。
只做只读 GET，不打印完整 token（掩码显示）。
"""
import json
import sys
import urllib.request
import urllib.error
import os
import datetime

USAGES_URL = "https://api.kimi.com/coding/v1/usages"
CRED_PATH = os.path.join(os.environ.get("USERPROFILE", ""), ".kimi-code", "credentials", "kimi-code.json")


def mask(v):
    if isinstance(v, str) and len(v) > 8:
        return v[:4] + "..." + v[-4:]
    return v


def main():
    # 1. 读取凭据
    print("== 1. 读取本地凭据 ==")
    if not os.path.isfile(CRED_PATH):
        print(f"凭据文件不存在: {CRED_PATH}")
        sys.exit(1)
    with open(CRED_PATH, encoding="utf-8") as f:
        cred = json.load(f)
    print(f"凭据文件: {CRED_PATH}")
    for k in ("access_token", "refresh_token", "token_type", "expires_at", "account_id", "id", "expires_in"):
        if k in cred:
            print(f"  {k}: {mask(cred[k]) if isinstance(cred[k], str) else cred[k]}")
    access_token = cred.get("access_token") or cred.get("token") or cred.get("id")
    if not access_token:
        print("未找到 access_token，无法继续。")
        sys.exit(1)

    # 2. 请求配额 API
    print("\n== 2. 请求配额 API ==")
    req = urllib.request.Request(USAGES_URL, headers={
        "Authorization": f"Bearer {access_token}",
        "Accept": "application/json",
        "User-Agent": "kimi-code-token-statistics-test",
    })
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status = resp.status
            body = resp.read().decode("utf-8")
        print(f"HTTP {status}")
    except urllib.error.HTTPError as e:
        print(f"HTTP 错误: {e.code} {e.reason}")
        print("响应体:")
        print(e.read().decode("utf-8", errors="replace")[:2000])
        sys.exit(2)
    except Exception as e:
        print(f"请求失败: {type(e).__name__}: {e}")
        sys.exit(2)

    # 3. 解析响应
    print("\n== 3. 解析响应 ==")
    try:
        data = json.loads(body)
    except json.JSONDecodeError as e:
        print(f"JSON 解析失败: {e}")
        print(body[:2000])
        sys.exit(3)
    print(f"顶层字段: {list(data.keys())}")

    usage = data.get("usage")
    if usage:
        print("\n--- 顶层 usage (一般对应 weekly/7d) ---")
        for k in ("used", "limit", "remaining", "resetTime", "reset_time", "window", "unit", "timeUnit", "duration"):
            if k in usage:
                v = usage[k]
                if k in ("resetTime", "reset_time") and isinstance(v, str):
                    v = v + "  (" + str(datetime.datetime.fromisoformat(v.replace("Z", "+00:00"))) + ")"
                print(f"  {k}: {v}")
        pct = None
        if usage.get("limit"):
            used = float(usage.get("used") or 0)
            limit = float(usage.get("limit"))
            pct = used / limit * 100
            print(f"  => 使用率: {pct:.1f}%  (剩余 {limit - used:.0f})")
    else:
        print("顶层无 usage 字段")

    limits = data.get("limits")
    if limits:
        print(f"\n--- limits 窗口数: {len(limits)} ---")
        for idx, w in enumerate(limits):
            detail = w.get("detail") or w
            d = w.get("duration") or detail.get("duration")
            tu = w.get("timeUnit") or detail.get("timeUnit")
            used = detail.get("used")
            limit = detail.get("limit")
            remaining = detail.get("remaining")
            reset = detail.get("resetTime") or detail.get("reset_time")
            print(f"  [{idx}] duration={d} timeUnit={tu} used={used} limit={limit} remaining={remaining} resetTime={reset}")
            try:
                f_used, f_limit = float(used), float(limit)
                print(f"       => 使用率 {f_used / f_limit * 100:.1f}%")
            except (TypeError, ValueError):
                pass
    else:
        print("无 limits 字段")

    # 4. 保存原始响应到本地（供调试）
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "quota_response.json")
    with open(out, "w", encoding="utf-8") as f:
        f.write(body)
    print(f"\n原始响应已保存: {out}")
    print("== 测试完成 ==")


if __name__ == "__main__":
    main()
