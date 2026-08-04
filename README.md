# OmniShare

OmniShare 是面向个人、家庭、小团队和内网环境的私有化多设备内容中枢。它将随手记、文件与视频、受控分享、轻量协作文档、设备发现、审计、备份恢复和回收站整合为可离线构建的产品，并同时提供 Web、单二进制后端和 Tauri 2 桌面端。

## 当前发布状态

| 组成 | 版本 | 状态 |
|---|---:|---|
| 后端与 Web | `v1.3.0-rc2` | Release Candidate |
| Tauri 桌面端 | `v1.0.1-desktop` | Prerelease |

### 桌面端下载

- [OmniShare Desktop v1.0.1 GitHub Release](https://github.com/Azurefly/omnishare/releases/tag/v1.0.1-desktop)
- [全部 Releases](https://github.com/Azurefly/omnishare/releases)

Windows 普通用户优先选择：

```text
OmniShare_1.0.1_x64-setup.exe
```

需要 MSI 部署时选择：

```text
OmniShare_1.0.1_x64_en-US.msi
```

Linux 根据发行版选择 AppImage、DEB 或 RPM。Release 同时提供 Windows/Linux SHA-256 清单和 Windows 安装布局运行测试 JUnit 报告。

> **macOS 说明：** macOS Universal 构建和双架构检查已经通过，但当前仓库尚未配置 Apple Developer ID 签名和公证凭据。为了避免用户下载后被 Gatekeeper 判定为“不受信任”或“应用已损坏”，本次预发布不提供未签名的 macOS 正式下载包。

### v1.0.1 桌面热修复

- 后端进程异常退出时由桌面 watchdog 自动恢复，不再持续停留在 `Failed to fetch`。
- 前端对短暂断线进行有限重试、错误节流和恢复后刷新。
- 本机按一台产品设备展示，不再按 loopback、Tailscale、虚拟网卡和 APIPA 地址重复生成设备卡。
- Windows MSI 安装布局测试会强制终止后端并验证新进程恢复，同时校验本机设备条目恰好为 1。
- npm、Cargo 与 Tauri bundle 版本必须一致，否则发布契约失败。

## 桌面端能力

- **Tauri 2 桌面壳**：复用现有 Go 后端和 Web 前端，不另造业务系统。
- **原生后端生命周期**：Rust 负责启动、停止、健康检查、端口选择和错误诊断。
- **端口冲突避让**：首选端口被非 OmniShare 服务占用时自动选择备用端口。
- **已有服务接管**：发现已有 OmniShare 后端时直接连接，不重复创建后端。
- **单实例**：重复启动会唤醒已有窗口，保持一个桌面进程。
- **系统托盘**：关闭主窗口后继续驻留，可重新打开、隐藏或完全退出。
- **安装包**：Windows MSI/NSIS；Linux AppImage/DEB/RPM；macOS Universal 构建能力。
- **诊断证据**：启动失败时记录后端路径、端口、退出状态、日志路径和最近日志。

## 核心产品能力

- **默认安全**：首次启动仅监听 `127.0.0.1`；开启局域网必须先设置至少 16 字节访问密钥。
- **密钥保护**：管理密钥不接受 URL 参数，不写入浏览器长期存储；服务端只保存 PBKDF2-HMAC-SHA256 派生值。
- **内容票据**：下载、视频与 Raw 内容使用短时签名票据，不传播全局密钥。
- **分享边界**：HEAD 不消耗次数；短时浏览器会话避免 Range 请求重复扣减；HTML/SVG/XML 强制下载。
- **浏览器隔离**：严格 CSP、`no-store`、`no-referrer`；Service Worker 只缓存固定静态资源。
- **数据可靠性**：写入失败不污染内存；主状态与配置保留上一代备份；文件写入执行文件同步和原子替换。
- **后端单实例**：同一数据目录使用跨平台进程锁，拒绝第二个写实例。
- **文件模型**：同内容复用物理 Blob，但每次上传保留独立逻辑文件、文件名与生命周期。
- **完整备份恢复**：备份包含状态、配置、文件清单和 SHA-256；恢复先校验、分阶段替换并支持回滚。
- **发现协议**：局域网公告使用稳定 Ed25519 身份和签名，校验时间、端口、字段长度并确定性去重排序。
- **协作保护**：文档自动保存按文档快照串行执行；切换前保存；冲突时保留本地草稿。

## Windows 桌面端使用

1. 从 GitHub Release 下载 NSIS 安装器或 MSI。
2. 完成安装并启动 OmniShare。
3. 桌面端会自动启动内置后端并打开主界面。
4. 关闭主窗口后应用继续驻留系统托盘。
5. 使用托盘菜单重新打开窗口或完全退出。

默认数据目录由桌面端管理。启动失败时可在错误页面中查看后端日志路径和最近输出。

旧版便携运行方式仍然可用：解压传统 Windows ZIP 后双击：

```text
start-omnishare.cmd
```

该启动器调用同目录的 `start-omnishare.ps1`，数据默认保存到：

```text
%APPDATA%\OmniShare
```

## Web/后端快速运行

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

### 开启局域网

先在本机设置不少于 16 字节的访问密钥，再启用“允许局域网访问”。没有访问密钥时，程序会拒绝绑定非回环地址。跨不可信网络应使用 HTTPS、Tailscale 或可信 VPN。

## 数据目录

直接运行后端二进制时默认使用：

- Windows：`%USERPROFILE%\.omnishare`
- Linux/macOS：`~/.omnishare`

传统 Windows 启动脚本和用户级安装包使用：

- 数据：`%APPDATA%\OmniShare`

Linux 用户级安装包使用 XDG 目录：

- 程序：`~/.local/lib/omnishare`
- 启动器：`~/.local/bin/omnishare`
- 数据：`${XDG_DATA_HOME:-~/.local/share}/omnishare`
- 状态与日志：`${XDG_STATE_HOME:-~/.local/state}/omnishare`

卸载程序默认保留用户数据。

## 构建桌面端

### Windows

```powershell
./scripts/build-tauri-desktop.ps1
```

### Linux/macOS

```bash
./scripts/build-tauri-desktop.sh
```

手动构建流程：

```bash
npm --prefix frontend run build
cd backend && go build -trimpath -o ../desktop/src-tauri/resources/omnishare ./cmd/omnishare
cd ../desktop
npm install
npm run build
```

## 完整测试过程

桌面 Release 不是以“安装包成功生成”为完成标准。每次发布必须通过以下层级：

### 1. 基础仓库 CI

- 前端语法和契约检查。
- Windows launcher 契约检查。
- `gofmt`、`go vet`。
- Go 单元和集成测试。
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

- 主存储仍是单 JSON 快照；数据规模、写并发和审计量增大时存在 O(N) 写放大，后续应迁移 SQLite WAL。
- 当前只有单管理员密钥，没有只读、上传者、审计员等角色和作用域令牌。
- HTTPS 需要用户提供证书；尚无自动证书签发和可信设备配对。
- 上传是流式单请求，不是分片、断点续传或跨设备端到端加密。
- 文档使用乐观锁，不是 CRDT/OT 实时协同。
- 发布物尚未提供商业代码签名、完整 SBOM 和来源证明。
- Windows 托盘物理可见性/点击行为仍需在 Windows 10/11 真机完成最终验收。
- macOS 正式分发仍依赖 Apple Developer ID、公证和真机 Gatekeeper 验收。

## 文档

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
