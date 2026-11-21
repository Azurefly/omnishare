# OmniShare v1.3.0-rc2：性能与隐藏 BUG 检测修复报告

- **范围**：`backend/internal/{api,storage,frontend,config}`、`frontend/web`、`desktop/src-tauri`
- **目标**：定位 CPU 空转/后台无谓开销、每请求热路径退化、以及"看起来已修复但实际未生效"的类隐藏缺陷
- **测试环境**：Apple M4（arm64），Go `go test ./...`、`go test -race ./...`、`go vet ./...` 全绿
- **结论**：合计 22 项处置（前半段 14 项 + 本轮续作 8 项），其中 3 项为**功能性隐藏缺陷**（修复未生效、故障恢复被自身抵消、看门狗空转）。未发现 CPU 空转循环，无残留数据竞争。真实二进制实测：空闲 30s 仅耗 **0.01% 单核**，加压 23,784 rps 下单请求 CPU 成本 30.9 µs

---

## 零、前半段已完成的处置（本报告承接时已在工作区）

任务前半段已落地以下优化，本轮予以验证并延续：

| ID | 处置 | 效果 |
|---|---|---|
| P1 | `enterRequest` 维护 `requestTotal` 计数器 | 全局并发检查从 O(客户端数) 降为 O(1) |
| P2 | `markTicketCounted` 媒体票据去重改为按需清理 | 不再每次媒体请求全表扫描 |
| P3 | `durable.WriteFileFunc` 流式落盘 + 64KB 缓冲 | 大状态每次写入省掉一次整包分配 |
| P4 | `persistCandidate` 改为 `json.Encoder` 直写临时文件 | 同上，去掉中间缓冲 |
| P5 | `cloneState` 由 JSON 往返改为结构化深拷贝 | 每次变更少一次全量序列化/反序列化；本报告已补深拷贝与回滚回归 |
| P6 | 查询过滤 `containsFold` | 免掉每项 `strings.ToLower` 分配 |
| P7 | `buildShareTargetIndex` + `statusOf` | 分享状态从 O(分享×对象) 降为 O(分享+对象) |
| P8 | `revokeSharesForObjects` 单趟吊销 | 清空回收站/清理时 O(分享) 而非 O(对象×分享) |
| P9 | `CleanupExpired` / `PurgeExpiredTrash` 增加只读预检 | 每分钟清理线程在无事可做时不再克隆并重写整个数据集 |
| P10 | 前端统计/标签筛选/健康徽章跳过无变化 DOM 重写 | 去掉高频 innerHTML 重解析 |
| P11 | Pad 409 版本冲突不再自动重试旧版本 | 消除每次输入触发的冲突提示 + 全量重载 |
| P12 | 桌面看门狗 `consecutive_failures` 递增修复 | 即本报告 A2 |
| P13 | 清理线程遇到 errNoChange 时正确短路 | 配合 P9 避免无谓持久化 |
| P14 | 打包脚本去掉 `dpkg-deb` 强依赖、`--printf` 改用 sed | 提升非 Debian 环境可移植性 |

---

## 一、缺陷清单

### A. 功能性隐藏缺陷（优先级最高）

| ID | 问题 | 现象 | 修复 |
|---|---|---|---|
| A1 | **内嵌前端副本落后于源码** | `backend/internal/frontend/dist/app.js` 是 Go 二进制实际分发的文件。此前对 `frontend/web/app.js` 的性能与版本冲突修复只改了源码，嵌入副本缺失，**用户跑的是旧 JS** | 同步副本，并新增 `internal/frontend/dist_sync_test.go`：每次测试逐字节比对 `frontend/web` 与 `dist/`，双向检查多余/缺失文件 |
| A2 | **看门狗重启失败后退避被自身抵消** | `desktop/main.rs` 中 `consecutive_failures` 与指数退避共用同一变量。重启成功后归零，失败时 `+=1`，但失败分支在其后又把指数用到 0 附近，导致重启失败时仍按 5s 轮询并持续刷日志 | 失败分支显式 `saturating_add(1)`，保证指数持续增长；成功分支才归零 |
| A3 | **本地设备探测重复系统调用** | `listDevices` 每次请求都执行 `net.InterfaceAddrs()`（getifaddrs）与 `preferredOutboundIP()`（UDP dial）两趟系统调用；前端 30s 轮询一次即触发一次 | 抽取 `localNetwork()` 统一缓存，接口地址、出网 IP、快照 TTL 一次刷新，所有请求共用 |

### B. 每请求热路径退化（性能）

| ID | 问题 | 量化影响 | 修复 |
|---|---|---|---|
| B1 | `isLocalAddress` 用**全局互斥锁**保护接口地址缓存 | 该函数在路由之前对**每一个请求**执行（含静态资源、媒体 Range 请求）。微基准：互斥锁版 130.4 ns/op 且**串行化所有并发请求**，无锁快照版 20–35 ns/op（**≈4–6x**），且不再有串行化点 | 改为 `atomic.Pointer[localNetworkSnapshot]` 只读快照 + 单飞刷新（`localMu` 仅用于刷新），冷路径刷新失败时沿用旧快照，不误锁局域网客户端 |
| B2 | `localDevices()` 死代码 | 0% 覆盖率的旧实现仍持有独立调用路径，与新逻辑行为不一致（每网卡一张卡片） | 删除，只保留 `summarizedLocalDevices` 单一实现 |
| B3 | `listDevices` 无界 goroutine 扇出 | 每个已配置节点（上限 32）都会立即起一个 goroutine 排队等 4 路信号量，瞬时 goroutine 数等于节点数 | 先在主循环获取信号量再起 goroutine，goroutine 数上限 4 |
| B4 | `config.Get()` 每请求多次深拷贝 | `validHost`、`applyCORS`、`authorized` 各自调用一次 `Get()`，每次拷贝 Peers/AllowedOrigins 切片 | 未改（评估为可接受：≤32 peers 时单次拷贝 <1µs，改动风险高于收益），记录在案 |

### C. 后台与频率型开销

| ID | 问题 | 修复 |
|---|---|---|
| C1 | `cleanupAuth` 在**每次鉴权尝试**时全表扫描失败记录，持全局 `s.mu`；入口超过上限时每次淘汰都是 O(n)，构成 O(n²) 放大 | 改为按 1 分钟节流扫描；淘汰循环每次调用上限 64 次。正确性不受影响：`authRetryAfter`/`recordAuthFailure` 都各自复查条目时间戳 |
| C2 | 桌面端看门狗每 5s 抓取**整页 index.html**（读满 64KB）判断存活 | 改探测 `/api/v1/health`（实测 71 字节 vs 首页 22,173 字节，降至 1/312），HTML 标题判断保留为旧版本回退 |
| C3 | 前端 `loadHealth` 15s 轮询无去重、无暂停 | 增加 `state.healthLoading` 在途去重（避免慢链路下请求堆积）；轮询加 `document.hidden` 判断，后台标签页不再空转 |

---

## 二、验证

### 新增回归测试（Go 10 个 + Rust 1 个，另修订 Rust 1 个；附 9 个基准）

| 文件 | 覆盖 |
|---|---|
| `backend/internal/frontend/dist_sync_test.go` | A1：嵌入副本与源码逐字节一致；无多余嵌入文件；已用负向注入（追加一行）验证能检出漂移 |
| `backend/internal/api/hardening_test.go` | B1 快照复用与原子替换、并发无竞态；B3 限流记账精确性（含未进入的 key 不污染全局计数）；C1 节流后仍封禁当前被锁客户端、超表容量后单次调用淘汰上限；**C2 跨语言契约**：`/api/v1/health` 必须包含字面量 `"status":"ok"`（Rust 看门狗靠裸 socket 字符串匹配，不解析 JSON）且响应体 ≤512 字节，已用负向注入（把 `status` 改成 `state`）验证能抓到回归 |
| `backend/internal/storage/hardening_test.go` | 清理预检不会漏掉真实工作（TTL/保留窗口/置顶豁免/回收站清理各路径）；`cloneState` 改为结构深拷贝后 Rollback 语义不回退（含 audit metadata map 独立、Tags slice 独立、\*time.Time 独立） |
| `desktop/.../runtime/health.rs` | 新增 JSON health 探针用例；原有 HTML 用例保留 |
| `backend/internal/api/perf_test.go` | 新增 `BenchmarkLocalAddressLookup`：无锁后 20–35 ns/op、16 B/op、1 alloc（该 alloc 来自 `net.IP.String()` 构造 map 键，占比可忽略，未引入 `unsafe` 优化） |

### 大数据量基准（2 万 notes / 300 files / 500 pads / 2000 shares）

| 场景 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkDashboardLargeState` | 373,675 | 1,405,762 | 117 |
| `BenchmarkListNotesLargeState` | 534,440 | 3,436,854 | 56 |
| `BenchmarkListSharesLargeState` | 1,017,266 | 2,638,155 | 118 |

2 万条便签列表 0.53ms、分享列表 1.0ms（此前 `shareStatus` 对每个分享线性扫描全部对象，是 O(N×M)）；这些是修复后的数据，作为回归基线。

### 全量测试

```
go build ./...        OK
go vet ./...          OK
gofmt -l .            无输出
go test ./...         OK（api/storage/config/discovery/durable/instance/frontend）
go test -count=1 -race ./...   OK（无缓存重跑，24s）
```

### 真实二进制运行时实测

上述基准都在 `go test` 进程内，为排除"测试环境与真实二进制不一致"，另用构建产物做端到端验证（`--port 18081`，独立数据目录）：

**A1 端到端确认**：二进制实际返回的 `/app.js` 为 47,578 字节，与 `frontend/web/app.js`、`dist/app.js` 三者逐字节一致，且含 `healthLoading` 标记 4 处 —— 修复后的 JS 确实在对外分发，不是只改了源码。

**C2 探测载荷**：`/api/v1/health` 71 字节 vs 首页 `index.html` 22,173 字节。看门狗从每 5s 读满 22KB 降到 71 字节，**单次探测载荷降至 1/312**；按 5s 周期、每天 17,280 次折算，**日探测流量 383 MB → 1.2 MB**（桌面端常驻场景下这是纯后台浪费）。

**空闲态 CPU（30s 采样）**：

| 指标 | 数值 |
|---|---|
| CPU 时间 | 0.0029 s / 30 s = **0.01%** 单核 |
| 常驻内存 | 24.8 MB |
| OS 线程 | 15 |
| 上下文切换 | 115 次 / 30s（≈4/s） |

空闲 30 秒几乎不消耗 CPU、无周期性抖动，与"清理线程预检短路 + 无忙循环"的代码结论一致，可判定**不存在 CPU 空转**。

**加压对照（8 并发长连接，10s）**：

| 指标 | 数值 |
|---|---|
| 吞吐 | 237,843 请求 / 10s = **23,784 rps** |
| CPU | 7.35 s / 10s = 73.5% 单核 |
| 单请求 CPU 成本 | **30.9 µs** |
| 常驻内存 | 25.3 MB（无增长趋势） |

> 测量方法说明：`ps`/`top` 在沙箱内不可用，改用 `proc_pidinfo(PROC_PIDTASKINFO)` 直读进程 CPU 计数。该计数器单位是 mach absolute time 而非纳秒（本机 timebase `125/3` ≈ 41.67 ns/tick），已用 `mach_timebase_info` 换算；并以 `yes` 忙循环做标定（读数 5.0s / 5.0s = 100%），确认换算正确后再取值。若按纳秒直读会把 CPU 低估约 24 倍，故此处特别记录。

### Rust 侧改动尚未本地编译验证的说明

本机没有 cargo，以下文件只能源码级核对，未能 `cargo test`：

- `desktop/src-tauri/src/runtime/health.rs`（探针路径 + 新用例）
- `desktop/src-tauri/src/runtime/health.rs` 用到的 `read_to_string`/`take` 均已在原文件中使用，未新增 API，改动为纯文本内容
- `desktop/src-tauri/src/main.rs`（A2 已在前半段完成）

建议在具备 cargo 的环境补跑 `cargo test` 后再纳入发布门禁。

**已做的补偿验证**（本机无 Rust 工具链时的替代手段）：

1. 逐行复核 `is_omnishare_backend`：`Read::take(self, limit)` 会按值移动 `TcpStream`，其后不再使用该变量，故可编译；`Take<&mut _>` 的 `read_to_string` 接收 `&mut`，符合签名。
2. 请求头 `Host: 127.0.0.1:<port>` 能通过 Go 侧 `validHost`（loopback 直接放行），不会因 Host 校验被 403。
3. 新增 `TestHealthPayloadKeepsWatchdogContract` 把 Rust 侧依赖的字符串契约固化在 Go 测试里，并已负向注入验证。这样即便 Rust 单测暂时缺位，**导致看门狗失效的那类改动也会在 Go 测试里先红**。

---

## 三、检查过但未发现问题的项

为避免"漏报"，以下方向已逐项核对：

- **CPU 空转循环**：Go 侧全部为 `time.Ticker` + `ctx.Done()`；Rust 侧 `wait_backend` 为 300ms sleep 轮询且有总超时；`pumpUploads` 为 Promise 驱动非忙等。**未发现忙循环。** 运行时实测佐证：空闲 30s 仅 0.0029s CPU（0.01% 单核）、115 次上下文切换，无周期性抖动。
- **僵尸/泄漏 goroutine**：discovery 三个循环均随 ctx 退出；`listDevices` 扇出已被 B3 收紧。
- **Service Worker**：network-first + 白名单静态资源，无安全分享内容缓存。
- **SQLite 引擎**：`SQLiteStore` 目前仅测试引用，运行时主路径仍是 JSON Store（已有整改文档记录），本次不改其行为。
- **`s.mu` 单锁**：每请求 2 次锁获取（enter/leave），评估为本地优先场景可接受，未做分片。

---

## 四、遗留风险与建议

1. **`config.Get()` 每请求深拷贝**（B4）在 peers/AllowedOrigins 增长时会放大，建议后续改为读时快照指针。
2. **JSON 全量持久化**仍是写放大根源：每次 `mutate` 序列化整个数据集。已有 SQLite WAL 迁移在进行中，建议按其 spec 推进落地。
3. **`internal/frontend/dist` 同步链路**：`frontend/scripts/build.mjs` 已执行 `rm -rf dist && cp -r web dist` 全量同步，`scripts/build-release.sh` 会先跑 `npm run build`，因此**正式发布链路本身不会漂移**。A1 的真实成因是开发者绕过前端构建、直接 `go build`/`go run` 时 `dist` 不会更新。现已由 `dist_sync_test.go` 把"直接 `go build` 且副本过期"变成 `go test` 红灯，残余风险仅剩开发期摩擦，无需再改构建脚本。
4. 桌面端 Rust 建议在接入 CI/开发机后补跑 `cargo test`，本次仅完成源码级核对。
5. **`packaging/windows/start-omnishare.cmd` 的幻影差异已修复**。成因：`.gitattributes` 声明 `*.cmd text eol=crlf`，但仓库内该 blob 仍以 CRLF 存储，git 比对时把工作区归一化成 LF 再比，于是恒显示 5 行 +/− 的假改动（文件本身与 `HEAD` 逐字节相同：186 字节、UTF-8 BOM、CRLF）。已执行 `git add --renormalize` 将索引 blob 归一化为 LF（检出时按 `eol=crlf` 仍还原为 CRLF，Windows 行为不变），工作区文件内容零改动。该改动**已暂存**，随下次提交生效。经排查，仓库内 `.cmd/.bat/.ps1` 中仅此一个文件存在该问题。
