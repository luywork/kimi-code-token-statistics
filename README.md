# kimi-hud

**English** | [简体中文](README.zh-CN.md)

kimi-hud is a Windows system-tray resident service, written in Go, that tracks your **Kimi Code** token usage. It sits quietly in the tray and shows generation speed, cache hit rate, today's usage and subscription quota at a glance — no main window, low memory, a single binary with no CGO.

## Features

- **Realtime metrics** — streaming TPS (median), TTFT, and a `gen Ns` timer while a turn is in flight; multiple parallel agents are aggregated into a fleet speed (e.g. `⚡ 156 t/s (3 agents @52)`).
- **Cache hit rate** — token-weighted, accumulated across turns; stays steady between turns instead of flickering.
- **Today's usage** — total tokens (all models) with a per-model breakdown, cached and refreshed automatically.
- **Subscription quota** — 5h / 7d usage bars with percentage and reset countdown; tray icon turns green / yellow / red by usage level (thresholds at 60% and 85%). Third-party providers hide this section automatically (it never represents API balance).
- **Cost estimation** — local estimate from input / output / cache-read / cache-write token counts, using a built-in Kimi price table; custom per-model prices and a subscription monthly fee can be set in `config.toml` (hot-reloaded, no restart needed).
- **Detail window** — "View detailed usage…" in the tray menu opens an on-demand WebView2 window with time ranges (today / 24h / 7d / 30d / all), per-model usage and cost, manual refresh and a cycling auto-refresh (5s–60s). Close the window and you're back to the tray; zero overhead while it's closed.

## Requirements

- Windows 10 / 11 (x64)
- **Kimi Code CLI** installed and logged in (data is read from `~/.kimi-code/`)
- **WebView2 Runtime** — only needed for the detail window; preinstalled on up-to-date Win10/11, otherwise get it from [Microsoft](https://developer.microsoft.com/en-us/microsoft-edge/webview2/)
- Go 1.25+ and Git — only for building from source
- Python 3 — optional, only for the helper scripts (`scripts/`)

## Installation

### 1. Clone the code

```bash
git clone <repository-url> kimi-hud
cd kimi-hud
```

### 2. Install dependencies

Install [Go 1.25+](https://go.dev/dl/), then let the Go module system pull everything else:

```bash
go mod download
```

There is nothing else to install: no CGO toolchain, no Node.js — Win32 and WebView2 are loaded at runtime via `syscall` / `LazyDLL`, and the module dependencies (`go-webview2`, `go-win32api`, `go-com`, `x/sys`) are resolved automatically on the first build. Icon/version resources are pre-compiled into committed `.syso` files, so a plain `go build` works out of the box.

### 3. Development & debugging

```bash
# Quick run from source (console subsystem: prints startup logs to stdout)
go run ./cmd/kimi-hud

# Debug build without the GUI subsystem flag — convenient for development
go build -o build/kimi-hud-debug.exe ./cmd/kimi-hud
```

Debugging aids:

| What | Where |
|---|---|
| Tray service log | `%USERPROFILE%\.kimi-code-hud\debug.log` |
| WebView2 channel log | `%USERPROFILE%\.kimi-code-hud\webview.log` |
| WebView2 white-screen / channel diagnostics | `cmd/echotest` + `scripts/run_webview_test.bat` |
| Tray menu rendering preview (console) | `cmd/demo` |
| Quota API verification | `scripts/verify_quota.py` / `scripts/test_quota.py` |
| Tests | `go test ./...`, race check: `go test -race ./...` |

Notes:

- The detail-window frontend is embedded via `go:embed` (`internal/webview/frontend.html`) — after editing the HTML/JS you **must rebuild** the exe; `node --check` (on the extracted `<script>`) can lint the JS.
- A debug (console-subsystem) build pops a black console window and dies with it — expected; that's why release builds need `-H windowsgui` (see below).
- `go vet` reports two `unsafe.Pointer` warnings in `internal/tray/owndraw.go` (lines 202, 220) — intentional owner-draw menu usage, not a regression.

### 4. Build for release

```bash
# Release build: GUI subsystem (no console window), stripped
go build -ldflags "-s -w -H windowsgui" -o build/kimi-hud.exe ./cmd/kimi-hud

# Verify the PE subsystem is WINDOWS_GUI
python scripts/check_pe_subsystem.py build/kimi-hud.exe
```

> The `-H windowsgui` flag is **required**. Without it the binary is built as a console-subsystem executable: a black console window appears on startup and closing it kills the process.

The result is one self-contained `build/kimi-hud.exe` — that single file is the entire deliverable to distribute (WebView2 Runtime is the only external dependency, already present on updated Windows 10/11). Re-run [go-winres](https://github.com/tc-hib/go-winres) only if you change `cmd/kimi-hud/winres.json` or the icons, then commit the regenerated `.syso`.

## Usage

Start `kimi-hud.exe` — it lives in the system tray (single instance enforced; launching it again does nothing). The icon color reflects the current state:

| Icon color | Meaning |
|---|---|
| 🟢 Green | Quota OK (5h/7d usage below 60%) |
| 🟡 Yellow | Quota warning (60% – 85%) |
| 🔴 Red | Quota critical (≥ 85%) |
| 🔵 Blue | Generating (no quota data available) |
| ⚪ Gray | Idle (no quota data available) |

### Tray menu

Left-click or right-click the tray icon to open the menu:

![Tray menu](doc/images/tray-menu.png)

From top to bottom:

- **Realtime metrics** — generation speed (`TPS` / `TTFT`, plus a `gen Ns` timer while generating) and the cross-turn cumulative cache hit rate.
- **Today's usage (all models)** — total tokens of today, an input / output / cache-read / cache-write breakdown, and the local cost estimate. Refreshed from cache automatically.
- **Subscription quota** (`managed:kimi-code` only) — 5h and 7d usage bars with reset countdowns, refreshed every 5 minutes. Hidden entirely when the current model is not a Kimi-managed subscription.
- **Actions** — `View detailed usage…` opens the detail window (grayed out if WebView2 Runtime is missing), `Refresh quota` forces a quota refresh now, `Exit` quits the app.

### Detail window

Click **View detailed usage…** to open the WebView2 detail window:

![Detail window](doc/images/detail-window.png)

- **Top realtime cards** — TPS / TTFT, cache hit rate, today's total tokens and subscription quota; pushed live (≤ 0.5 s) while the window is open.
- **Time ranges** — `Today / 24 hours / 7 days / 30 days / All`; the "Today" range scans live and back-fills the tray's today card so both always agree.
- **Filters** — narrow the tables by vendor / model.
- **Stat cards & tables** — request count, token totals, cache read/write, estimated cost, a per-model summary table, and a recent-requests list.
- **Toolbar** — `刷新` refreshes immediately (silent; keeps your current range and pagination); `自动刷新: 关闭` cycles through Off → 5s → 10s → 15s → 30s → 60s → Off.

Close the window and you're back to the tray — no background cost while it's closed.

### Exit

- Menu → `Exit`, or
- run `kimi-hud.exe --quit` to ask the running instance to quit.

## Configuration

kimi-hud keeps its own config at **`~/.kimi-code-hud/config.toml`** (fully isolated from Kimi Code's `~/.kimi-code/config.toml`, which is only ever read, never written). Create the file if it doesn't exist — changes hot-reload on the next refresh cycle.

### Custom model pricing

Prices are in **CNY per million tokens**. The built-in table covers official Kimi models (k3, k2.7-code, k2.6, k2.5, …); subscription models such as `kimi-for-coding` have no built-in price, so add one to make cost stats meaningful:

```toml
[pricing."kimi-for-coding"]   # model name as it appears in usage stats
input       = 6.5             # CNY / 1M tokens
output      = 26.0
cache_read  = 1.3
cache_write = 6.5

[pricing.subscription]
monthly_cny = 60.0            # subscription monthly fee (display only)
```

- All four fields are optional (missing = 0).
- Full names (`kimi-code/kimi-for-coding`) or short names (`kimi-for-coding`) both match; case-insensitive.
- User overrides take precedence over the built-in table.
- For the full price-table reference see `doc/成本设置说明.md` (kept locally, not part of the repository).

## How it works

Three independent data pipelines, merged only at the presentation layer:

1. **Realtime metrics (offline)** — incremental reads of Kimi Code wire event logs (`~/.kimi-code/sessions/<workdir>/session_*/agents/*/wire.jsonl`) feed a metrics state machine that derives TPS / TTFT / cache hit rate from `step.end` usage events.
2. **Subscription quota (online)** — calls `GET https://api.kimi.com/coding/v1/usages` with the CLI's access token, cached for 5 minutes. On 401 the old cache is kept until Kimi CLI refreshes the token itself.
3. **Today's usage (offline full scan)** — a lightweight today-only scan feeds the tray's "today total" card (5-minute cache, auto-reset at midnight); the detail window's "today" range scans live and back-fills the same cache so both views always agree.

## Project layout

```
cmd/kimi-hud/       Main binary (tray service + WebView2 detail window)
cmd/demo/           Console demo that prints the tray menu rendering
cmd/echotest/       WebView2 diagnostics harness
internal/display/   Tray menu text + icon color (shared by kimi-hud and demo)
internal/metrics/   TPS / TTFT / cache / fleet state machine
internal/quota/     Quota API client (5h / 7d windows, 5-min cache)
internal/today/     Today-usage cache manager
internal/scan/      Offline usage scans (today / 24h / 7d / 30d / all)
internal/session/   Latest-session discovery + relocation
internal/pricing/   Built-in price table + user overrides + cost math
internal/modelcfg/  Kimi config.toml reader (managed-provider detection)
internal/tray/      Win32 tray: owner-draw menu, icon, message loop
internal/webview/   WebView2 host + embedded frontend (frontend.html)
internal/wire/      Wire JSONL event types / parsing
doc/                Design & review documents (Chinese)
scripts/            PE/icon checkers, quota API test scripts
```

## Development notes

- The UI thread (tray message loop + WebView2 COM apartment) is pinned to the main OS thread via `runtime.LockOSThread()`; all COM/window calls must happen there. `metrics.State` has no internal lock and must be accessed under the `stMu` mutex in `main.go`.
- Behavioral reference: the original zero-dependency Node.js implementation at `D:\Project\kimi-code-hud-main\src\*.mjs` (used to cross-check constants and semantics).
