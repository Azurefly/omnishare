# OmniShare

OmniShare 是面向个人、家庭、小团队和内网环境的私有化多设备内容中枢。它将随手记、文件与视频、受控分享、轻量协作文档、设备发现、审计、备份恢复和回收站整合为可自托管的产品，并同时提供 Web、单二进制后端和 Tauri 2 桌面端。

## 当前状态

| 组成 | 版本或分支 | 状态 |
|---|---:|---|
| 后端与 Web | `v1.3.0-rc3` | Release Candidate，Windows/Linux/macOS 的 AMD64/ARM64 便携包 |
| Windows/Linux 桌面 | `v1.3.0-rc3` | 同版本预发布，Tauri x64 安装包 |
| macOS Tauri 桌面 | 源码与 CI 验证 | Universal 构建可用；公开签名、公证分发待完成 |
| `main` 主分支 | 统一开发基线 | 已整合安全加固、SQLite、v3 加密备份、恢复与受限阅读修复、桌面初始化及主题功能 |

> **版本边界：** `v1.3.0-rc3` 将后端、Web 和 Windows/Linux 桌面统一为一个候选版本。下载内容以该 Release 的实际附件为准。旧 `v1.0.1-desktop` 不包含下述整合更新，已有用户升级前应先备份并核对数据目录。升级步骤和已知限制见 [RC3 发布说明](docs/RELEASE_NOTES_v1.3.0-rc3.md)。

### RC3 更新

| 范围 | 使用上的变化 |
|---|---|
| SQLite 存储 | 默认使用 SQLite WAL；旧 JSON 数据可自动迁移。已有数据库损坏或无法打开时明确报错，避免误用迁移前的旧数据。 |
| 加密备份 | 新 `.osb` 使用 v3 格式，校验完整包头、数据顺序和结束记录；继续读取旧 v2 `.osb` 与 ZIP。 |
| 恢复与计数 | 失败恢复保留最终切换前已成功完成的写入；修复阅读上限、并发阅后即焚与缓冲计数一致性。 |
| 首次桌面启动 | 桌面端为自己启动的后端自动交接初始化令牌，首次进入后即可配置访问密钥。 |
| 浏览器与文档 | 会话存储不可用时页面仍可使用；保存失败保留最新草稿，备份取消与写入失败会停止下载，恢复过程防止重复提交。 |
| 界面主题 | Web、PWA 和桌面端支持跟随系统、白天和黑夜模式。 |

实现与验证记录见 [分支整合与可靠性优化](docs/INTEGRATION_2026-11-07.md)。

## 下载与版本选择

- [OmniShare v1.3.0-rc3：服务器便携包与 Windows/Linux 桌面安装包](https://github.com/Azurefly/omnishare/releases/tag/v1.3.0-rc3)
- [RC3 更新、升级与备份兼容说明](docs/RELEASE_NOTES_v1.3.0-rc3.md)
- [全部 Releases](https://github.com/Azurefly/omnishare/releases)

Windows x64 桌面用户优先选择：

```text
OmniShare_1.3.0-rc3_x64-setup.exe
```

需要 MSI 部署时选择：

```text
OmniShare_1.3.0-rc3_x64_en-US.msi
```

Linux x64 桌面根据发行版选择 `OmniShare` AppImage、DEB 或 RPM。Windows/Linux/macOS 的 AMD64、ARM64 便携包名称以 `OmniShare-v1.3.0-rc3-` 开头，提供单后端进程和浏览器界面；Linux 小写 `omnishare_1.3.0~rc3_*.deb` 属于服务器包。Windows ARM64 使用对应便携包。

Release 同时提供服务器包 SHA-256 清单、Windows/Linux 桌面校验清单，以及 Windows 安装布局与后端自动恢复测试的 JUnit/JSON 证据。

> **macOS 说明：** 本 Release 的 macOS ZIP 是未签名的后端 `.app` 启动包装，不是 Tauri Universal 桌面安装包。Tauri Universal 构建和双架构检查可用，但公开桌面分发仍需完成 Apple Developer ID 签名、公证和 Gatekeeper 验收。未签名的后端程序也可能受到 Gatekeeper 限制。

## 已包含的桌面可靠性修复

- 后端进程异常退出时由桌面 watchdog 自动恢复，不再持续停留在 `Failed to fetch`。
- 前端对短暂断线进行有限重试、错误节流和恢复后刷新。
- 本机按一台产品设备展示，不再按 loopback、Tailscale、虚拟网卡和 APIPA 地址重复生成设备卡。
- Windows MSI 安装布局测试会强制终止后端并验证新进程恢复，同时校验本机设备条目恰好为 1。
- npm、Cargo 与 Tauri bundle 版本必须一致，否则发布契约失败。

## 界面主题

主题功能已经合并到 `main`，适用于 Web、PWA 和 Tauri 桌面端。

### 快速切换

页面顶部工具栏提供主题按钮：

```text
☀ 白天
☾ 黑夜
```

点击后会在浅色与深色之间立即切换，无需刷新页面或重启桌面程序。

### 系统设置

进入：

```text
系统设置 → 界面主题
```

可选择：

- **跟随系统**：根据 Windows、macOS 或 Linux 桌面环境自动切换。
- **浅色（白天）**：固定使用浅色界面。
- **深色（黑夜）**：固定使用深色界面。

### 保存与同步规则

- 主题选择只保存在当前浏览器或桌面端本机。
- 配置键为 `omnishare.theme`，使用浏览器本地存储保存。
- 主题不会上传到 OmniShare 后端，也不会写入项目数据或备份内容。
- 不同浏览器、不同设备之间不会自动同步主题。
- 清除站点数据或应用 WebView 数据后，主题会恢复为“跟随系统”。

### 加载与系统联动

- 页面在主样式加载前恢复主题，避免启动时先显示深色再切换浅色的闪烁。
- “跟随系统”模式监听 `prefers-color-scheme`，操作系统主题变化后会即时更新。
- 浏览器顶部主题色、表单控件、对话框、卡片、列表、提示框和代码区域会同步变化。
- 启用“减少动态效果”时，主题过渡动画会自动关闭。

### PWA 与离线使用

`theme.js` 和 `theme.css` 已加入 Service Worker 静态资源白名单，因此安装为 PWA 或离线启动时仍可恢复主题。Web 源码和 Go 后端内嵌前端保持同步，桌面安装包不会使用另一套主题实现。

## 桌面端能力

- **Tauri 2 桌面壳**：复用现有 Go 后端和 Web 前端，不另造业务系统。
- **原生后端生命周期**：Rust 负责启动、停止、健康检查、端口选择和错误诊断；首次启动为自有后端交接初始化令牌。
- **后端 watchdog**：托管后端意外退出后自动恢复，并限制重启频率。
- **端口冲突避让**：首选端口被非 OmniShare 服务占用时自动选择备用端口。
- **已有服务接管**：发现已有 OmniShare 后端时直接连接，不重复创建后端。
- **单实例**：重复启动会唤醒已有窗口，保持一个桌面进程。
- **系统托盘**：关闭主窗口后继续驻留，可重新打开、隐藏或完全退出。
- **主题切换**：支持跟随系统、白天和黑夜三种模式。
- **安装包**：Windows MSI/NSIS；Linux AppImage/DEB/RPM；macOS Universal 构建能力。
- **诊断证据**：启动失败时记录后端路径、端口、退出状态、日志路径和最近日志。

## 核心产品能力

- **默认安全**：首次启动仅监听 `127.0.0.1`；开启局域网必须先设置至少 16 字节访问密钥。
- **密钥保护**：管理密钥不接受 URL 参数，不写入浏览器长期存储；服务端只保存 PBKDF2-HMAC-SHA256 派生值。
- **内容票据**：下载、视频与 Raw 内容使用短时签名票据，不传播全局密钥。
- **分享边界**：HEAD 不消耗次数；短时浏览器会话避免 Range 请求重复扣减；HTML/SVG/XML 强制下载。
- **浏览器隔离**：严格 CSP、`no-store`、`no-referrer`；Service Worker 只缓存固定静态资源。
- **数据可靠性**：SQLite WAL 默认存储，支持旧 JSON 数据迁移；配置与 JSON 回退状态保留上一代备份，文件写入执行文件同步和原子替换。
- **后端单实例**：同一数据目录使用跨平台进程锁，拒绝第二个写实例。
- **文件模型**：同内容复用物理 Blob，但每次上传保留独立逻辑文件、文件名与生命周期。
- **完整备份恢复**：备份包含状态、配置、文件及 SHA-256 校验清单；支持 v3 AES-256-GCM 加密包与明文 ZIP，恢复先校验、分阶段替换并支持失败回滚。
- **受限阅读**：阅读次数上限和阅后即焚在并发请求下统一判断、消费并持久化，避免在计数刷盘窗口内被重复读取。
- **发现协议**：局域网公告使用稳定 Ed25519 身份和签名，校验时间、端口、字段长度并确定性去重排序。
- **协作保护**：文档自动保存按文档快照串行执行；切换前保存；冲突时保留本地草稿。

### 备份与恢复

1. 点击“导出备份”，设置口令可生成加密 `.osb`；留空生成明文 ZIP。口令不会写入数据目录，恢复加密包需要相同口令。
2. 支持 File System Access API 的浏览器先选择保存位置，再生成并分块写入备份；取消选择不会开始导出，写入失败会停止下载。
3. 不支持该 API 的浏览器使用内存缓冲下载，上限为 2 GB；即使响应没有提供文件长度，也会按实际接收字节限制。更大的备份需要支持流式保存的浏览器。
4. 恢复时选择备份文件、填写加密口令并确认覆盖。一次恢复完成前不会重复提交；服务端先解压和校验，再切换数据。

导出与恢复共用“备份上限”设置（`backup_max_mb`，默认 16384 MB）。成功恢复会用备份替换当前数据和配置，备份之后的新内容也会被替换；操作前应先导出当前数据。失败恢复会回滚到最终切换之前的状态，保留解压校验期间已经成功完成的写入。

| 备份文件 | main 最新构建的支持情况 | 兼容边界 |
|---|---|---|
| v3 `.osb` | 新加密备份的默认格式，可导出和恢复 | 旧程序无法读取，恢复时需要本次整合更新或更新版本。 |
| v2 `.osb` | 可恢复已有备份 | 保留旧格式的分块校验能力，不具有 v3 的完整包头和结束记录认证。 |
| 未加密 ZIP | 可导出和恢复 | 文件内容未加密，请按其中的数据敏感程度保管。 |

v3 使用 AES-256-GCM 与 PBKDF2-HMAC-SHA256：完整包头、分块顺序与结束记录都参与认证，读取时拒绝截断与尾随数据，并限制不可信包头指定的密钥派生计算量。包头中的版本、算法、创建时间和校验清单仍为可读元数据，备份的数据内容经过加密。

### 会话与文档草稿

访问密钥和初始化令牌使用会话存储，不写入浏览器长期存储。浏览器禁止访问会话存储，或存储空间不足时，当前页面改用内存继续工作；重新加载或关闭页面后，这部分内存数据会丢失，需要重新通过本机初始化链接或访问密钥进入。

文档保存失败或发生版本冲突时，会保存编辑器的最新草稿，避免较早的自动保存请求覆盖之后的输入。草稿保存在当前会话；存储不可用时仅保留在当前页面内存中，重要内容应另行复制保存。文档采用版本冲突检测，不提供多人同时逐字编辑的实时合并。

## Windows 桌面端使用

1. 从 GitHub Release 下载 NSIS 安装器或 MSI。
2. 完成安装并启动 OmniShare。
3. 桌面端自动启动内置后端并打开主界面。
4. 关闭主窗口后应用继续驻留系统托盘。
5. 使用托盘菜单重新打开窗口或完全退出。

正常情况下，任务管理器会看到两个不同职责的进程：

- 一个 Tauri 桌面壳。
- 一个由桌面壳管理的 Go 后端。

重复启动不会创建第二个桌面壳。默认数据目录由桌面端管理；启动失败时，错误页面会显示后端日志路径和最近输出。

旧版便携运行方式仍然可用。解压传统 Windows ZIP 后双击：

```text
start-omnishare.cmd
```

该启动器调用同目录的 `start-omnishare.ps1`，数据默认保存到：

```text
%APPDATA%\OmniShare
```

## Web/后端快速运行

从 `main` 构建需要 **Go 1.26.0 或更新版本**、Node.js 与 npm；当前 CI 使用 Go 1.26.5 和 Node.js 22.23.1。前端构建不需要另装 npm 依赖；首次编译后端需要获取 Go 模块，离线构建需提前准备依赖缓存。

在仓库根目录执行：

```bash
npm --prefix frontend run build
cd backend
go run ./cmd/omnishare
```

默认地址：

```text
http://127.0.0.1:8081
```

常用参数：

```text
--port 8081
--data-dir /path/to/data
--name My-Node
--listen 127.0.0.1
--tls-cert /path/to/cert.pem
--tls-key /path/to/key.pem
--no-browser
```

桌面端使用用户配置和自动避让策略管理端口；直接运行后端时可以通过 `--port` 指定端口。

### 首次配置与初始化令牌

尚未设置访问密钥时，请在运行 OmniShare 的本机完成初始化：

1. **直接运行后端**：使用程序自动打开的页面。如果指定了 `--no-browser`，从启动日志复制带 `init_token` 的完整本机 URL 并打开。
2. **从 main 构建的桌面端**：桌面程序会从自己启动的后端的实际数据目录读取初始化令牌，并交给首次打开的页面；无需手动寻找令牌文件。连接已有的其他后端时，仍需使用该后端的初始化链接或访问密钥。
3. 进入“系统设置”，设置至少 16 字节的访问密钥。成功保存后，初始化令牌失效并从磁盘删除，后续访问使用访问密钥。

令牌文件位于实际数据目录的 `init-token`，路径会写入启动日志；本机 URL 形式为 `http://127.0.0.1:<端口>/?init_token=...`。页面读取令牌后立即从地址栏移除，并保存在会话存储或当前页面内存中。未设置密钥时，管理与数据接口仅接受携带正确令牌的回环请求；直接打开不带令牌的地址不会获得管理权限。

从旧版本升级且原本没有访问密钥时，同样使用下一次启动提供的初始化链接。已经设置访问密钥的用户继续使用原密钥。

### 开启局域网

先在本机设置不少于 16 字节的访问密钥，再启用“允许局域网访问”。没有访问密钥时，程序会拒绝绑定非回环地址。跨不可信网络应使用 HTTPS、Tailscale 或可信 VPN。

## 数据目录

直接运行后端二进制，或由 Tauri 桌面端启动自有后端时，默认使用：

- Windows：`%USERPROFILE%\.omnishare`
- Linux/macOS：`~/.omnishare`

Tauri 桌面端可通过 `OMNISHARE_DATA_DIR` 指定数据目录；直接运行后端可使用 `--data-dir`。切换启动方式前应核对原数据位置。

传统 Windows ZIP 内的 `start-omnishare.cmd` / `install.ps1` 使用：

- 数据：`%APPDATA%\OmniShare`

Linux tar.gz 内的 `install.sh` 用户级安装使用 XDG 目录：

- 程序：`~/.local/lib/omnishare`
- 启动器：`~/.local/bin/omnishare`
- 数据：`${XDG_DATA_HOME:-~/.local/share}/omnishare`
- 状态与日志：`${XDG_STATE_HOME:-~/.local/state}/omnishare`

小写 `omnishare_*.deb` 服务器包直接运行 `/usr/bin/omnishare`，数据默认仍在 `~/.omnishare`；Tauri 桌面 DEB 使用上面的桌面默认目录。

卸载程序默认保留用户数据。

### 存储引擎与升级（SQLite）

生产默认使用 SQLite（`omnishare.db`，WAL 模式），旧版单文件 JSON 实现保留为回退引擎。

- 自动迁移：首次以 SQLite 启动且数据目录存在 `omnishare.json` 时，先把原文件完整复制为 `omnishare.json.<UTC时间戳>.migrated.bak`，再在单个事务中导入并逐项校验 `notes/files/pads/shares/audits/trash` 条数与审计哈希链。任一不一致会回滚事务、保留原 JSON，并自动回退到 JSON 引擎继续运行；启动日志和 `/api/v1/health` 会显示 `"migration": "not_migrated"`。
- 手动迁移：`omnishare --migrate-storage`，打印迁移条目数后退出。
- 强制回退：`omnishare --storage-engine=json`（或环境变量 `OMNISHARE_STORAGE=json`）。迁移成功后原 `omnishare.json` 仍然保留，不会被删除或改名。
- 已有 SQLite 数据库无法打开时，程序会停止并说明原因，不会自动选择迁移前的旧 JSON。旧 JSON 不包含 SQLite 使用期间新增的数据；只有明确决定使用旧数据时才手动选择 JSON 引擎。
- 完整回滚：停止服务，把 `omnishare.db`、`omnishare.db-wal`、`omnishare.db-shm` 移出数据目录，再以 `--storage-engine=json` 启动即可回到旧数据。
- 已知限制：普通笔记阅读数与文件下载数是内存累加、每 5 秒批量落库，进程崩溃会丢失最近几秒的计数；设置最大阅读次数的笔记逐次同步计数，`burn-after-read` / 达到最大阅读次数时同步销毁。备份快照包含尚未刷盘的计数，刷新失败会保留批次供下次重试。审计保留策略（`audit_retention_days` 默认 90、`audit_max_events` 默认 100000）只在 SQLite 引擎执行，裁剪以锚点分段进行；JSON 回退引擎不裁剪审计。

## 构建桌面端

先准备上述 Go、Node.js/npm 环境，以及 Rust 工具链和 Tauri 2 对应平台的编译依赖。Windows 使用 MSVC 构建环境，macOS 使用 Xcode Command Line Tools，Linux 需要相应的 WebKitGTK 开发依赖。推荐使用仓库脚本，按顺序构建前端、内置后端和桌面壳。

### Windows

```powershell
./scripts/build-tauri-desktop.ps1
```

### Linux/macOS

```bash
./scripts/build-tauri-desktop.sh
```

手动构建流程（Linux 本机示例；Windows 可执行文件名和 macOS Universal 双架构构建由上述脚本处理）：

```bash
npm --prefix frontend run build
cd backend && go build -trimpath -o ../desktop/src-tauri/resources/omnishare ./cmd/omnishare
cd ../desktop
npm install
npm run build
```

从 `main` 构建会包含本文列出的 SQLite、v3 加密备份、恢复与阅读修复、桌面首次初始化及主题功能。构建产物位于 `desktop/src-tauri/target/release/bundle`；macOS Universal 使用 `desktop/src-tauri/target/universal-apple-darwin/release/bundle`。代码签名、公证和公开发布需另行完成，签名要求见 [桌面发布与签名](docs/DESKTOP_RELEASE.md)。

## 完整测试过程

桌面 Release 不是以“安装包成功生成”为完成标准。每次发布必须通过以下层级。

### 1. 基础仓库 CI

- 前端语法、静态契约及实际应用函数的 Node 行为回归，覆盖存储拒绝、草稿冲突、下载限制和恢复防重复提交。
- 主题脚本语法、加载顺序和本地持久化契约。
- 浅色主题变量和关键组件覆盖检查。
- Service Worker 主题资源缓存检查。
- Web 源码与 Go 内嵌前端同步检查。
- Windows launcher 契约检查。
- `gofmt`、`go vet`。
- Go 单元和集成测试、Race Detector、覆盖率门禁与 staticcheck。
- 独立后端进程的真实 HTTP 验证，覆盖初始化鉴权、中文内容、上传与 Range、加密备份恢复、错误口令和重启持久化。
- Windows/Linux/macOS 的 amd64/arm64 后端交叉构建。

### 2. 桌面契约与 Rust 门禁

```bash
npm --prefix desktop run test:contracts
cd desktop/src-tauri
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --all-features
```

### 3. Windows MSI 安装布局端到端测试

自动化测试会：

- 管理提取 MSI。
- 从真实安装布局启动桌面端。
- 验证内置后端和 EXE 图标。
- 使用非 OmniShare 服务制造端口冲突。
- 验证自动切换端口且不会误连接其他服务。
- 验证一个桌面进程和一个托管后端进程。
- 验证已有后端接管且不重复启动。
- 验证第二次启动退出并保持单实例。
- 强制终止后端并验证 watchdog 自动恢复。
- 验证本机设备接口只返回一个本机节点。
- 生成 JSON、JUnit、启动日志和 SHA-256 证据。

### 4. Linux 和 macOS

- Linux 完成 contracts、fmt、clippy、unit、bundle 和 SHA-256。
- macOS Universal 验证主程序和内置后端同时包含 `arm64` 与 `x86_64`。
- 正式 macOS 发布还必须通过 Developer ID 签名、公证、`codesign` 和 Gatekeeper `spctl`。

完整规范见 [桌面完整测试流程](docs/DESKTOP_TEST_PLAN.md)。

## 测试发现并修复的 Windows 缺陷

MSI 真实运行测试发现，后端在 Windows 上完成配置文件同步和原子重命名后，又执行 Unix 风格目录级 `fsync`。Windows 返回 `ERROR_ACCESS_DENIED`，导致后端将成功持久化误判为启动失败。

修复后：

- Windows 保留文件级 `fsync`。
- 保留原子重命名和 `.bak`。
- Windows 不再因不支持的目录级同步退出。
- Linux/macOS 继续执行严格目录同步。

同时，已配置环境的后端启动已经前移到 Tauri Rust 原生 `setup`，不再把 WebView JavaScript 作为服务启动的单点依赖。

## 明确限制

- 主存储已默认切到 SQLite（WAL），JSON 引擎用于首次迁移失败时回退或显式选择；JSON 路径在数据规模增大时仍有 O(N) 写放大，且不执行审计裁剪。
- 恢复先在暂存目录解压并校验，只在最后切换目录/状态的短暂窗口内排他；恢复期间仍可上传下载，但切换前完成的并发上传会被目标备份状态覆盖。恢复上传带空闲超时（默认 2 分钟无数据即断开），不是固定总时长。
- 当前只有单管理员密钥，没有只读、上传者、审计员等角色和作用域令牌。
- HTTPS 需要用户提供证书；尚无自动证书签发和可信设备配对。
- 上传是流式单请求，不是分片、断点续传或跨设备端到端加密。
- 未设置口令时备份与恢复使用未加密 ZIP；设置口令后为 AES-256-GCM 加密包 `.osb`。口令不写入数据目录，丢失口令将无法恢复该备份。
- 文档使用乐观锁，不是 CRDT/OT 实时协同。
- 发布物尚未提供商业代码签名、完整 SBOM 和来源证明。
- Windows 托盘物理可见性和点击行为仍需在 Windows 10/11 真机完成最终验收。
- macOS 正式分发仍依赖 Apple Developer ID、公证和真机 Gatekeeper 验收。
- 清除密钥或恢复无密钥配置后的初始化令牌续发、自动清理与恢复的文件协调、审计锚点在备份恢复中的完整保存仍属后续专项，详见[整合记录](docs/INTEGRATION_2026-11-07.md#后续专项)。
- RC3 仍为候选版本，下载能力以对应 Release 的实际附件为准；旧 `v1.0.1-desktop` 不包含本轮整合更新。

## 文档

- [v1.3.0-rc3 发布与升级说明](docs/RELEASE_NOTES_v1.3.0-rc3.md)
- [2026-11-07 分支整合与可靠性优化](docs/INTEGRATION_2026-11-07.md)
- [Desktop v1.0.0 发布说明](docs/RELEASE_NOTES_v1.0.0-desktop.md)
- [桌面完整测试流程](docs/DESKTOP_TEST_PLAN.md)
- [桌面发布与签名](docs/DESKTOP_RELEASE.md)
- [Tauri 迁移方案](docs/TAURI_MIGRATION_PLAN.md)
- [v1.3.0-rc2 发布说明](docs/RELEASE_NOTES_v1.3.0-rc2.md)
- [150 条评审整改映射](docs/REMEDIATION_v1.3.0-rc1.md)
- [五轮测试报告](docs/TEST_REPORT_5_ROUNDS_v1.3.0-rc1.md)
- [架构与安全边界](docs/ARCHITECTURE.md)
- [v1.3 产品需求](docs/PRODUCT_REQUIREMENTS_v1.3.md)
- [安全策略](SECURITY.md)
