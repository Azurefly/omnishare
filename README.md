# OmniShare

OmniShare 是面向个人、家庭、小团队和内网环境的自托管内容工作区。随手记、文件与视频、受控分享、轻量协作文档、设备发现、审计、备份恢复和回收站集中在同一个界面中，可通过浏览器、单二进制后端或 Tauri 2 桌面端使用。

## 当前版本：0.1.1

`0.1.1` 更新 macOS 发布包装：为完整 `.app` 添加 ad-hoc 完整性签名，验证最终 ZIP 的解压、启动及签名，并将最低系统要求明确为 macOS 12。后端、Web 与 Windows/Linux 桌面使用同一版本号，现有存储和共享功能继续保留。下载以 [v0.1.1 Release](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1) 的实际附件为准。

从 `0.1.0` 更新到 `0.1.1` 是常规补丁更新，先备份并退出旧进程，再按原安装方式更新即可。**如果仍使用此前的 `1.x`，需要处理从 `0.1.0` 开始的版本号重置：** `0.1.1` 仍低于 `1.x`，安装器可能拒绝直接覆盖，必要时卸载旧应用但保留数据目录。新加密备份仍使用 v3，继续读取 v2 和 ZIP。详见[安装与迁移](#安装与迁移)及 [0.1.1 发布说明](docs/RELEASE_NOTES_v0.1.1.md)。

## 主要功能

| 功能 | 使用方式 |
| --- | --- |
| 内容工作区 | 桌面左侧集中导航、设备与存储，右侧显示当前模块；窄屏保留连接状态和可滚动导航。 |
| 随手记与剪贴板 | 保存纯文本，设置保留时长、阅读次数或阅后即焚；剪贴板内容先预览，再确认发送到当前节点。 |
| 文件与视频 | 多文件排队上传、取消和手动重试；下载与视频播放支持 HTTP Range。 |
| 受控分享 | 为内容创建带有效期、访问限制等约束的分享，管理内容生命周期与回收站。 |
| 轻量协作文档 | 自动保存、版本冲突检测与会话草稿保留，适合跨设备继续编辑。 |
| 设备与连接 | 按当前节点、在线和离线分组；打开其他节点后使用该节点自己的访问凭据。 |
| 数据与备份 | 默认 SQLite WAL，支持旧 JSON 迁移、完整备份、v3 加密备份及校验后恢复。 |
| 外观与设置 | 浅色、深色和跟随系统主题，设置按用途分组，提供键盘焦点和减少动态效果支持。 |

剪贴板、设备和传输的具体行为见[安全共享与使用边界](docs/SECURE_SHARING.md)。

## 下载

| 平台与使用方式 | v0.1.1 附件 |
| --- | --- |
| Windows x64 桌面 | `OmniShare_0.1.1_x64-setup.exe` 或 `OmniShare_0.1.1_x64_en-US.msi` |
| Linux x64 桌面 | `OmniShare_0.1.1_amd64.AppImage`、`OmniShare_0.1.1_amd64.deb` 或 `OmniShare-0.1.1-1.x86_64.rpm` |
| Windows 后端与 Web | `OmniShare-v0.1.1-windows-amd64.zip` / `OmniShare-v0.1.1-windows-arm64.zip` |
| Linux 后端与 Web | `OmniShare-v0.1.1-linux-amd64.tar.gz` / `OmniShare-v0.1.1-linux-arm64.tar.gz`，或 `omnishare-server_0.1.1_{amd64,arm64}.deb` |
| macOS 后端与 Web | `OmniShare-v0.1.1-macos-amd64.zip` / `OmniShare-v0.1.1-macos-arm64.zip` |

在 [v0.1.1 Release](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1) 下载对应架构的文件，并用同页的 SHA-256 清单核对。Windows ARM64 使用后端便携包。Linux 的 `omnishare-server_` 文件是服务器包，`OmniShare_` 文件是桌面包，请按所需运行方式选择。服务器 DEB 的内部软件包名为 `omnishare`。

**macOS 发布包需要 macOS 12 Monterey 或更新版本。** Apple 芯片选择 `macos-arm64.zip`，Intel Mac 选择 `macos-amd64.zip`。解压后的 `.app` 启动 Go 后端并通过浏览器使用，当前不提供 Tauri macOS 桌面安装器。

**已发布的 macOS `0.1.1` 只有 ad-hoc 完整性签名，没有 Developer ID 身份签名或 Apple 公证**，首次下载后仍可能被 Gatekeeper 拦截；若提示“已损坏”，先核对文件并按 [macOS 安装与诊断](docs/MACOS.md)收集信息。

主分支正在完善正式签名、公证和 DMG 拖入“应用程序”的发布流程，并保留 ZIP 备用包。**新的公证安装包尚未发布，当前下载仍为上述 `0.1.1` 文件。** 配置签名身份、通过 Apple 公证及实际安装验收后，才会发布下一版本；维护者步骤见 [macOS 正式签名与公证](docs/MACOS_SIGNING.md)。正式包首次打开可能仍有系统正常的“是否打开”确认，目标是不需要“仍要打开”安全例外；组织管理策略可能另有限制。Windows 安装包尚未提供商业代码签名。

## 安装与迁移

### 首次安装

Windows x64 桌面用户下载 NSIS 安装器或 MSI，安装后启动 OmniShare；桌面端会启动内置后端并打开界面。关闭主窗口后应用继续驻留系统托盘，使用托盘菜单可重新打开窗口或完全退出。

Linux x64 桌面用户按发行版选择 AppImage、DEB 或 RPM。后端便携包通过浏览器使用；Windows ZIP 解压后运行 `start-omnishare.cmd`，Linux tar.gz 可直接运行后端或使用包内的用户级 `install.sh`。

首次启动默认仅监听本机。请先完成[首次配置](#首次配置与网络访问)，再按需要开放局域网。

### 从 0.1.0 更新到 0.1.1

先导出备份、退出应用及其后端，再安装新版本并沿用原数据目录。macOS 用户重新解压对应芯片的 ZIP，替换整个旧 `.app`，不要将新旧应用的内部文件合并；默认业务数据在 `~/.omnishare`，不在 `.app` 内。此次补丁不更改 SQLite 数据格式或备份容器版本，也不要求先卸载所有旧版应用。

### 从旧 1.x 迁移到 0.1.x

1. **确认并备份原数据。** 在旧程序中导出备份并保存口令，记录实际数据目录；需要目录副本时，先完全停止服务，再复制整个目录，包括数据库、文件和配置。
2. **退出旧程序及后端。** 桌面窗口关闭后可能仍在托盘运行，应选择完全退出；确认没有其他进程继续使用该数据目录。
3. **按版本重置安装。** 从 `0.1.0` 开始的版本号重新起算，当前 `0.1.1` 仍低于旧 `1.x`。仅当安装器或包管理器拒绝覆盖时，先卸载旧应用，保留数据目录与独立备份，再安装当前版本；不要使用会清除用户数据的选项。
4. **沿用原目录。** 同一平台的桌面安装包、便携脚本和直接运行后端可能使用不同默认目录。可通过 `OMNISHARE_DATA_DIR` 或后端的 `--data-dir` 指定原目录的绝对路径，不要让两个实例同时操作同一目录。
5. **核对内容后继续使用。** 检查随手记、文件、文档、分享和访问配置。已设置的访问密钥继续使用；没有密钥的环境通过本次启动提供的初始化入口配置。

版本号重置不降低 SQLite 数据格式或备份容器版本。不要仅凭应用版本号判断备份兼容性。恢复备份会替换当前数据和配置，详见[备份与恢复](#备份与恢复)。

旧 `omnishare.json` 可导入当前 SQLite 格式。若数据来自早期写入 `backend/data` 的 SQLite 原型，当前没有自动转换工具；请保留原程序和完整目录，在数据副本上另行转换与验证，不要直接覆盖唯一副本。

## Web 与后端快速运行

源码构建需要 **Go 1.26.0 或更新版本**、Node.js 和 npm；发布构建使用 Go 1.26 与 Node.js 22。macOS 发布包的最低版本为 12；自行换用更高版本 Go 时应核对其[系统要求](https://go.dev/wiki/MinimumRequirements)，Go 1.27 起要求 macOS 13。前端构建无需安装 npm 依赖，首次后端构建需要获取 Go 模块，离线构建应提前准备依赖缓存。

在仓库根目录执行：

```bash
npm --prefix frontend run build
cd backend
go run ./cmd/omnishare
```

默认地址为 `http://127.0.0.1:8081`。常用参数：

```text
--port 8081
--data-dir /path/to/data
--name My-Node
--listen 127.0.0.1
--tls-cert /path/to/cert.pem
--tls-key /path/to/key.pem
--no-browser
```

### 首次配置与网络访问

未设置访问密钥时，在运行 OmniShare 的本机完成初始化：

1. 直接运行后端时，使用程序自动打开的页面；指定 `--no-browser` 时，从启动日志复制带 `init_token` 的完整本机 URL。
2. 桌面端会从自己启动的后端的实际数据目录读取初始化令牌并交给页面。连接已有后端时，仍需该后端自己的初始化链接或访问密钥。
3. 在“设置 → 访问与网络”设置至少 16 字节的访问密钥。保存成功后，初始化令牌失效并从磁盘删除，后续访问使用访问密钥。

令牌文件位于实际数据目录的 `init-token`。页面读取 URL 中的令牌后立即从地址栏移除，并保存在会话存储或当前页面内存中。未设置密钥时，管理与数据接口只接受携带正确令牌的回环请求，普通本机 URL 不会自动获得管理权限。

开启局域网前必须设置访问密钥；没有密钥时程序拒绝绑定非回环地址。跨不可信网络应使用 HTTPS、Tailscale 或可信 VPN。HTTPS 证书由用户提供，当前没有自动证书签发。

## 数据目录与存储

| 启动方式 | 默认数据目录 |
| --- | --- |
| Windows Tauri 桌面或直接运行后端 | `%USERPROFILE%\.omnishare` |
| Linux/macOS Tauri 桌面或直接运行后端 | `~/.omnishare` |
| Windows ZIP 的启动或安装脚本 | `%APPDATA%\OmniShare` |
| Linux tar.gz 的 `install.sh` | `${XDG_DATA_HOME:-$HOME/.local/share}/omnishare` |
| Linux 服务器 DEB，直接运行 `/usr/bin/omnishare` | `~/.omnishare` |
| macOS 后端 `.app` 包装 | `~/.omnishare` |

桌面端和直接运行的后端支持 `OMNISHARE_DATA_DIR`，后端也可使用 `--data-dir`。安装脚本会显式传入自身的数据路径，切换启动方式时应核对实际启动参数。Linux 用户级安装的程序位于 `~/.local/lib/omnishare`，启动器位于 `~/.local/bin/omnishare`，状态与日志位于 `${XDG_STATE_HOME:-$HOME/.local/state}/omnishare`。Windows 和 Linux 包内卸载脚本保留用户数据；更换安装器或手动卸载前仍应独立备份。

### SQLite 与旧 JSON 数据

默认存储为 `omnishare.db`，使用 SQLite WAL。同一数据目录有跨平台进程锁，拒绝第二个写实例。

- 首次以 SQLite 启动且存在旧 `omnishare.json` 时，先复制原文件为带 UTC 时间戳的 `.migrated.bak`，再在单个事务中导入，核对各类条目数和审计哈希链。导入失败会回滚，并保留原 JSON 继续使用回退引擎；启动日志与健康接口显示迁移状态。
- 可用 `omnishare --migrate-storage` 手动执行迁移。成功后原 JSON 仍然保留。
- 已有 SQLite 数据库无法打开时，程序停止并报告原因，不会静默退回迁移前的旧 JSON。
- `--storage-engine=json` 或 `OMNISHARE_STORAGE=json` 可显式选择 JSON 引擎。旧 JSON 不包含使用 SQLite 期间新增的内容，切换前须停止服务、备份并确认所需数据来源。
- 受最大阅读次数限制和阅后即焚的笔记同步消费次数与持久化。普通阅读、下载计数每 5 秒批量落库，进程崩溃仍可能丢失最近几秒的普通计数；备份快照包含尚未刷盘的计数。

同内容文件可复用物理 Blob，每次上传仍保留独立逻辑文件、名称和生命周期。SQLite 支持按配置裁剪审计，JSON 回退引擎不执行审计裁剪。审计哈希链用于校验链内一致性，不构成外部可信的防篡改证明。

## 备份与恢复

在“设置 → 存储与保留”导出备份。设置口令时生成加密 `.osb`，留空则生成明文 ZIP。备份包含状态、配置、文件与 SHA-256 校验清单；口令不写入数据目录，丢失口令将无法恢复对应加密包。

| 格式 | 0.1.1 的支持情况 | 使用边界 |
| --- | --- | --- |
| v3 `.osb` | 新加密备份的默认格式，可导出和恢复 | 恢复程序必须支持 v3，不能仅按应用版本号判断。 |
| v2 `.osb` | 可读取并恢复已有备份 | 保留旧格式的分块校验能力，没有 v3 的完整包头与结束记录认证。 |
| 未加密 ZIP | 可导出和恢复 | 内容没有加密，应按数据敏感程度保管。 |

v3 使用 AES-256-GCM 和 PBKDF2-HMAC-SHA256，认证完整包头、分块顺序与结束记录，拒绝截断和尾随数据，并限制不可信包头指定的密钥派生计算量。版本、算法、创建时间和校验清单等包头元数据仍可读。

支持 File System Access API 的浏览器先选择保存位置，再分块写入；取消文件选择不会开始导出，写入失败会停止下载。其他浏览器使用内存缓冲，按实际接收量限制为 2 GB；更大备份需要支持流式保存的浏览器。

导出和恢复共用 `backup_max_mb` 上限，默认 16384 MB。恢复时选择文件、填写原口令并确认覆盖；服务端先在暂存目录解压校验，再短暂锁定并切换数据，失败时回滚到切换前的状态。

**成功恢复会替换当前内容和配置，包括备份之后新增的数据。** 解压阶段仍可读写，但在最终切换前完成的新写入也可能被备份状态覆盖，应先暂停相关操作并导出当前备份。恢复请求防止重复提交，上传有默认 2 分钟的空闲超时。

## 剪贴板、传输与会话

点击“从剪贴板发送”才会请求读取权限。确认框展示纯文本或 PNG、目标节点和保留时长；默认保留 24 小时，可选择 1 小时或 7 天。确认后文本保存为随手记，PNG 进入文件队列。取消会清空预览，不上传内容，不在后台监控或自动同步剪贴板。

文本最多 256 KB，PNG 最多 16 MB，图片同时受节点上传上限与解码尺寸限制。HTML、SVG 等富内容不作为活动页面导入。剪贴板读取通常需要 HTTPS 或本机地址；浏览器不支持时可手动粘贴或选择文件。

传输队列显示排队、上传、服务端处理、完成、失败和取消，所有上传尝试共享最多 3 个并发名额。取消排队项不会发出请求；取消上传会终止当前连接，但不能撤回服务器已保存的内容。中断后先检查文件列表，再决定是否手动重试。上传当前不支持分片续传，下载和视频播放支持 HTTP Range。

访问密钥和初始化令牌只保存在当前会话；会话存储被禁用或写满时，页面改用内存继续工作，重载后需要重新验证。文档保存失败或发生冲突时保留当前编辑草稿；草稿也仅属于当前会话，存储不可用时只在当前页面内存中，重要内容应另行复制保存。文档使用版本冲突检测，没有逐字实时协同合并。

主题选择保存在当前浏览器的本地存储中，不上传至后端，也不随数据备份或跨设备同步；清除站点数据后恢复“跟随系统”。PWA 只缓存固定静态资源，离线启动不代表数据接口可以离线使用。

## 桌面运行与构建

桌面端复用同一 Go 后端和 Web 界面。Rust 管理后端启动、健康检查、端口避让和错误诊断；后端意外退出时 watchdog 会尝试恢复并限制重启频率。发现已有 OmniShare 后端时可连接已有服务，重复启动桌面应用会唤醒已有窗口。

除上述 Go、Node.js/npm 环境外，还需 Rust 工具链与 Tauri 2 平台编译依赖：Windows 使用 MSVC，macOS 使用 Xcode Command Line Tools，Linux 需要 WebKitGTK 开发依赖。

Windows：

```powershell
./scripts/build-tauri-desktop.ps1
```

Linux/macOS：

```bash
./scripts/build-tauri-desktop.sh
```

脚本按顺序构建前端、内置后端与桌面壳。普通构建产物位于 `desktop/src-tauri/target/release/bundle`，macOS Universal 产物位于 `desktop/src-tauri/target/universal-apple-darwin/release/bundle`。当前 macOS 后端包装说明见[macOS 安装与诊断](docs/MACOS.md)，签名凭据和正式分发门槛见[macOS 正式签名与公证](docs/MACOS_SIGNING.md)，Tauri 验证与统一发布入口见[桌面发布指南](docs/DESKTOP_RELEASE.md)。

## 验证

常用本地检查：

```bash
npm --prefix frontend run check
npm --prefix frontend test
npm --prefix frontend run build
npm --prefix desktop run test:contracts
(cd backend && go test ./... && go vet ./...)
```

真实 Chromium 界面检查需要安装锁定的开发测试依赖和浏览器：

```bash
npm --prefix frontend ci --ignore-scripts
(cd frontend && npx --no-install playwright install --with-deps chromium)
npm --prefix frontend run test:browser
```

CI 还执行 Go Race Detector、Staticcheck、独立 HTTP 进程验证、桌面 Rust 检查、六目标后端构建，以及 Windows 安装布局与 watchdog 验证。macOS 新发布流程对最终 ZIP 和 DMG 分别检查结构、架构、签名、实际 `.app` 启动、正常退出及篡改拒绝；正式模式另要求 Developer ID、公证、票据和 Gatekeeper 门槛通过。自动化检查与真实 Safari 首次下载验收分别记录，当前 `0.1.1` 不因此变为已公证版本。浏览器测试使用合成 API 数据，后端鉴权和持久化由 Go/HTTP 测试覆盖。发布前检查包名大小写冲突，发布后核对附件和 SHA-256。详细流程见[桌面完整测试流程](docs/DESKTOP_TEST_PLAN.md)。

## 当前限制

- 访问控制为单管理员密钥，没有只读、上传者、审计员等角色，也没有逐设备权限或可信配对。发现设备、网络在线和访问授权是不同状态。
- 文件上传不提供分片续传或跨设备端到端加密，取消不能召回已保存的数据；协作文档不提供 CRDT/OT 实时合并。
- JSON 回退引擎仍有随数据规模增长的整体写入开销，不执行 SQLite 的审计裁剪。
- 清除访问密钥或恢复无密钥配置后的初始化令牌续发尚待完善；相关操作可能需要重启后使用新的本机初始化链接。自动清理与恢复之间的文件协调、审计锚点的完整备份恢复仍需后续改进。
- Windows 托盘可见性和点击行为、macOS 权限与 Gatekeeper，以及 Safari 和各种触屏设备仍需对应真机验收。自动化检查不替代这些平台验收。
- 已发布的 `0.1.1` macOS 后端包使用 ad-hoc 完整性签名，尚未提供 Developer ID 签名和 Apple 公证；新的正式分发链路仍待凭据配置与实际发布验证。Windows 商业代码签名、完整 SBOM 和来源证明仍待补齐。macOS Tauri 桌面尚未公开分发。

## 文档

- [0.1.1 发布与更新说明](docs/RELEASE_NOTES_v0.1.1.md)
- [macOS 安装、签名边界与诊断](docs/MACOS.md)
- [安全共享与使用边界](docs/SECURE_SHARING.md)
- [架构与安全边界](docs/ARCHITECTURE.md)
- [桌面完整测试流程](docs/DESKTOP_TEST_PLAN.md)
- [桌面发布与签名](docs/DESKTOP_RELEASE.md)
- [安全策略](SECURITY.md)
- [参与开发](CONTRIBUTING.md)
