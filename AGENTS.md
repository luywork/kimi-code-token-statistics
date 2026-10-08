# AGENTS.md

This file provides guidance to Aidex when working with code in this repository.

## 项目概述

kimi-hud：Go 编写的 Windows 系统托盘常驻服务，统计 Kimi Code 的 token 用量。托盘菜单显示生成速度（TPS/TTFT/舰队）、跨回合累计 Cache 命中率、今日用量、订阅额度柱条（5h/7d + 重置倒计时），托盘图标按配额等级变色（绿/黄/红）。无主窗口、低内存、单二进制、无 CGO（纯 Win32 LazyDLL）。"查看详细用量"按需打开 WebView2 详情窗口，关窗回托盘。

## 常用命令

```bash
go build ./...                                          # 编译全部

# 正式版：GUI 子系统（不弹黑窗）；demo/echotest 保持控制台
go build -ldflags "-s -w -H windowsgui" -o build/kimi-hud.exe ./cmd/kimi-hud
python scripts/check_pe_subsystem.py build/kimi-hud.exe  # 校验 Subsystem=WINDOWS_GUI

go test ./...                                            # 全量单测
go test -race ./...                                      # 竞态检测（并发模型关键，务必跑）
go test -run <TestName> ./cmd/kimi-hud/                  # 单个测试（internal 包同理）
go vet ./...                                             # 见下方告警说明
gofmt -l .
```

- **构建正式版必须带 `-H windowsgui`**：缺了会编译成控制台子系统，启动弹黑窗、且关窗即杀进程（2026-09-02 实测踩坑）。改完验证后重跑 `check_pe_subsystem.py` 确认。
- **前端改动在 `internal/webview/frontend.html`（go:embed）**：改 HTML/JS 后必须重新编译 exe 才生效；JS 语法校验可用 `node --check`（先抽 `<script>` 内容）。

- 图标/版本资源（`cmd/kimi-hud/winres/*.png` + `winres.json`）已编译为 `.syso` 并随仓库提交，普通构建无需 go-winres；改 winres.json 或图标后需重跑 go-winres 再提交 .syso。
- `go vet` 对 `internal/tray/owndraw.go` 报 unsafe.Pointer 告警（202, 220 行）：owner-draw 自绘菜单的有意用法，非回归，可忽略。

## 架构

### 三条相互独立的数据管线，只在展示层合并

1. **实时指标（离线）**：wire 事件日志增量读取 → `internal/session.Manager`（定位最近活跃会话 + 驱动指标状态机）→ `internal/metrics.State`（TPS/TTFT/Cache 状态机 + 舰队汇总）。数据源：`~/.kimi-code/sessions/<wd>/session_*/agents/{main,agent-N}/wire.jsonl`。
2. **订阅额度（联网）**：`internal/quota.Client` → `GET https://api.kimi.com/coding/v1/usages`，双凭据路由：配置了长期 API key（`~/.kimi-code-hud/config.toml` 的 `[quota].api_key`，`sk-kimi-...`，热加载）时 Bearer 用 key、与 CLI 运行态解耦；未配置回退 credentials 的 access_token（15min 短期，靠 CLI 懒刷新）。TTL 5min 缓存 + 原子写。
3. **今日用量（离线全量扫描）**：`internal/scan.ScanToday`（today-only 轻量模式）→ `internal/today.Manager`（5min 缓存，跨天 date key 自动重置）。缓存是顶部实时卡"今日总 token"的唯一来源；详情窗"今日"档扫描完成后经 `tm.UpdateFromScan(report, startMs)` **回填缓存**（用户诉求：两处更新时间同步，详见下节）。

三条管线的数据经 `internal/display`（托盘菜单文本 + 图标颜色，`cmd/kimi-hud` 与 `cmd/demo` 共用，避免复制漂移）与 `cmd/kimi-hud/live.go`（WebView2 实时推送 JSON）合并展示。`internal/pricing` 提供官方单价表 + 费用计算（纳元整数运算）与用户配置覆盖。

### 并发模型（读多文件才能看清，务必遵守）

- `cmd/kimi-hud/main.go` 首行 `runtime.LockOSThread()`：UI 线程（托盘消息循环 + WebView2 COM apartment）绑定主 OS 线程，所有 COM/窗口调用必须在其上发生。
- 三个后台 goroutine：`pollLoop`（500ms wire 轮询 + 5s 配额刷新）、`today.Manager.Run`（5min）、`heartbeat`（500ms 图标变色 + 详情窗口打开期间推送实时数据，关窗零开销）。
- **`metrics.State` 无内部锁**：所有访问（pollLoop 写、`display.BuildMenu`/`buildLive` 读）都必须在 main.go 的 `stMu` 锁内。`quota.Client` 与 `today.Manager` 自带锁，可独立访问。锁外读 `metrics.State` 会触发 concurrent map read and write fatal。
- 退出：`CmdExit` 只调 `t.Stop()`；`close(stopCh)` 统一在 `t.Run()` 返回后执行一次，避免二次 close panic。
- **session 重定位（R2-1 评审固化）**：全目录遍历必须走锁外 `sess.RelocateScan()`（节流 30s，只读，结果存 pending）+ 锁内 `sess.ApplyPending()`（切换 `state` 指针），绝不能在 `stMu` 锁内做 `Relocate` 全目录扫描（历史会话多/慢盘会阻塞 BuildMenu/heartbeat）。`Relocate()` 仅作组合便捷方法（测试/一次性场景）。

### 详情窗口前端 RPC 协议（cmd/kimi-hud/live.go ↔ internal/webview/frontend.html）

前端是 `go:embed` 的静态 HTML（`internal/webview/frontend.html`，改前端要重新编译 exe），经 WebView2 WebMessage 双向通道与 Go 通信：

- **前端→Go RPC**（`handleWebMessage`，按 `id` 回投）：
  - `request_live`：立即返回一次实时快照（含 today 缓存/quota）。
  - `scan_usage`：`range=today` 走 `ScanToday` 轻量扫描并回填 today 缓存；其他档（24h/7d/30d/all）走全量 `scan.Scan`。全量扫描用 `scanGate`（并发门控，最多 1 个）+ `scanSeq`（丢弃过期回复，前端快速切档防竞态）。
  - `refresh_today`：非"今日"档时同步刷新今日缓存（异步 `ScanToday` + 写缓存），使顶部实时卡保持最新。
  - `default_home`：返回 Kimi 主目录。
- **Go→前端**：`heartbeat` 每 500ms 组装快照，仅 `liveChanged`（TPS/TTFT/Cache/today total/quota used）变化时才 `pushLive` 推送（R5 防抖）。
- **Go→前端投递通道（R1-2 评审固化）**：`webview.Window.Push` 是**可丢的 live 心跳**（有界队列 cap 8，满队只丢最旧 live）；`PushReply` 是**不可丢的 RPC 回复**（无条件入队）。**RPC 回复一律走 `PushReply`**——用 `Push` 发回复在 UI 线程繁忙/导航缓冲期可能被 live 挤出导致前端"扫描中"超时（曾踩坑）。

**今日同步设计（用户 2026-09-02 明确诉求）**：顶部"今日总 token"实时卡（读 `today.Manager` 缓存）与下方"今日"档（`scan_usage` 实时扫描）曾因更新时间不同而数值不一致。现"今日"档扫描成功后 `tm.UpdateFromScan(report, startMs)` 回填缓存 → 顶部卡下一跳心跳推送（≤500ms）同步为新值；`UpdateFromScan` 仅接受窗口起点为本日 00:00 的扫描（防跨天污染）。**订阅额度（quota）走独立数据源，不参与此同步，保持 5min 刷新节奏不变**。

**详情窗口刷新 UI**（frontend.html 工具栏）：「刷新」按钮=手动立即刷新（静默，不清空界面/保留分页）；「自动刷新」单按钮=默认关闭，点击按 关闭→5s→10s→15s→30s→60s→关闭 循环切换。

### 关键数据源事实（代码中不直接可见）

- wire 事件 `context.append_loop_event` 的 `event.type=step.end` 含 usage（inputOther/inputCacheRead/inputCacheCreation/output）、llmFirstTokenLatencyMs、llmStreamDurationMs → TPS/TTFT/Cache 唯一来源。
- `usage.record` 计入 Cache 会双计，实时指标必须忽略（但全量扫描 `internal/scan` 以 usage.record 为准——两套口径并存）。
- 不存在 `turn.ended` 事件；回合结束靠 step.end 的 `finishReason=end_turn` 标记。
- access_token 15 分钟过期，刷新端点是私有实现未公开；401 时保留旧缓存等待 Kimi CLI 懒刷新（`~/.kimi-code/bin/kimi.exe`），程序不自行刷新。长期 key 路径（`[quota].api_key`）401 = key 失效：同样只记错误不删缓存（key 与本地 credentials 文件无关，热加载修复后自动恢复）。
- `/usages` 端点同时接受 access_token 与长期 API key（2026-10-08 实测，对齐 cc-switch `query_kimi`）；响应顶层 `usages.limit_5h/limit_7d.used_ratio` 是服务端精确比率，展示层（托盘柱条/图标分级/详情窗/前端）一律走 `Window.Ratio()`：`UsedRatio > 0` 用服务端值，`<= 0` 回退 `Used/Limit` 推导并 clamp（-1 哨兵语义见 quota.go `UnmarshalJSON`——旧磁盘缓存缺该字段时为 -1，不能用 0，0 是合法比率）。
- 托管 provider 判定：`~/.kimi-code/config.toml` 的 `[models."<alias>"]` 的 `provider = "managed:kimi-code"`；非托管自动隐藏额度段。
- 用户单价/月费配置在 `~/.kimi-code-hud/config.toml`（`[pricing]`，热加载）；绝不写入 Kimi 的 `~/.kimi-code/config.toml`（其更新会覆盖我们的段）。**配置文件缺失时启动自动生成全注释模板**（`cmd/kimi-hud/main.go` 的 `ensureConfigTemplate`，含 `[quota].api_key` 与 `[pricing]` 示例；所有行含表头都以 # 开头保证零配置——非注释空表会被解析器当成配置，pricing 会以零值覆盖内置价格表）；已存在绝不覆盖。
- 参考实现（行为对拍/常量核对用）：`D:\Project\kimi-code-hud-main\src\*.mjs`（Node.js 原版，零依赖）；本项目多数包注释标注了"对齐 xxx.mjs"，改逻辑时对照原版可避免口径漂移。

### 已知陷阱（已修复，改相关代码前先读这些点）

- `internal/tray`：TrackPopupMenu 用 TPM_NONOTIFY + SetForegroundWindow 会前台锁定卡死线程；SetWindowPos 带 SWP_SHOWWINDOW 会把隐藏消息窗显示到屏幕左上角。弹菜单解前台锁严禁显示窗口，用 Alt 键模拟（keybd_event）。
- `internal/session`：Relocate 替换 State 指针会让外部读到空态 → 改为 session 自管、外部经 `State()` 访问。
- `internal/webview`：本机 Chromium 沙箱初始化失败致白屏（NavigationCompleted CONNECTION_ABORTED）→ 注入 `--no-sandbox`（DefaultAdditionalArgs）；DPI 保持系统默认 unaware（PER_MONITOR_AWARE_V2 会白屏）。
- `internal/scan`：今日档与全量档共享 `scanSeq` 丢弃过期回复（前端快速切档竞态）；今日档收紧上限（256K 行/5k 文件）。
- `internal/webview` 的 `webview.log` 中 `flush PostWebMessageAsJson` 对 >120 字符的 payload 截断（webview.go `json[:120]`），实时卡 today 字段常被截掉——排查推送内容时需用 Python 解析原始 JSON 行，或临时放宽截断。

## 设计、编码规则

### 复用优先，性能优先于复用

**避免重复造轮子；性能与复用冲突时，性能优先。**

- 开发设计及编码时，先盘点项目内已有的可复用能力（工具函数、middleware、IPC 通道、store 工厂、权限/确认链路等），能复用的一定不要重复建设。
- 每个功能设计尽可能优雅，提高代码的可用性（单一职责、接口清晰、与既有模式对齐）。
- 但当性能与复用做取舍时，**性能优先考虑**——为复用引入的热路径开销（多余抽象层、同步等待、重复计算）不可接受时，允许为性能破例，但必须在代码注释中说明取舍理由。

## 本项目落地示例

- 实时数据通道（`doc/02-kimi-usage-tracker功能整合方案.md` §5.5 D3c）：复用 `metrics.State`/`quota.Client`/`display` 与 `heartbeat`/`pollLoop` 的节奏推送实时数据，不另建 HTTP 服务或文件中间层；仅在 WebView2 窗口打开期间推送，关窗零开销。

## 文档与验证

- 设计/评审文档在 `doc/`：项目方案（含常量速查对照表）、功能整合方案（M2 WebView2 详情窗口）、WebView2 SPIKE 预研结果、开发过程工具问题记录。
- 桌面/托盘 GUI 自动化测试用 win-desktop-test skill（枚举窗口/弹托盘菜单/读日志断言）；WebView2 白屏变体诊断用 `cmd/echotest` + `scripts/run_webview_test.bat`；配额 API 校验用 `scripts/verify_quota.py`/`test_quota.py`。
- 运行时日志：`%USERPROFILE%\.kimi-code-hud\debug.log`（托盘）、同目录 `webview.log`（WebView2 通道）。

## Tool cache directory

`.cache/` is the Aidex tool cache directory (web-fetch page snapshots + web_search results; regenerable, safe to delete at any time). It is git-ignored — exclude it from git add/commit.
