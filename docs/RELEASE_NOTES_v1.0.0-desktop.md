# OmniShare Desktop v1.0.0

这是 OmniShare 首个基于 Tauri 2 的桌面端预发布版本，为 Windows 和 Linux 提供经过自动化构建、质量检查和安装布局运行验证的桌面安装包。

> 本版本标记为 **Prerelease**。Windows 已完成 MSI 安装布局端到端自动化测试；Linux 已完成构建、Rust 静态分析和单元测试。macOS Universal 构建能力已经验证，但由于当前仓库尚未配置 Apple Developer ID 签名和公证凭据，本次 GitHub Release 不提供容易被 Gatekeeper 判定为“不受信任/已损坏”的 macOS 正式下载包。

## 主要变化

- 新增 Tauri 2 桌面壳，保留现有 Go 后端和 Web 前端。
- 桌面端原生管理后端启动、停止、健康检查和端口选择。
- 支持系统托盘、关闭到托盘、恢复窗口和退出应用。
- 支持单实例：重复启动会激活已有窗口，不重复创建桌面进程。
- 已有 OmniShare 后端运行时，桌面端会连接已有后端，不重复启动服务。
- 后端端口被其他程序占用时自动选择可用端口。
- 后端启动不再依赖 WebView JavaScript 完成，已配置环境由 Rust 原生初始化。
- Windows 安装包包含 MSI 和 NSIS 两种格式。
- Linux 发布包含 Tauri 生成的 AppImage、DEB 或 RPM（以构建平台实际产物为准）。

## 完整测试过程

本版本必须同时通过仓库 CI 和桌面发布测试流程。

### 基础质量门禁

- 前端语法与契约测试。
- Windows launcher 契约测试。
- `gofmt` 和 `go vet`。
- Go 单元/集成测试。
- Windows、Linux、macOS 的 amd64/arm64 后端交叉构建。

### Rust 桌面质量门禁

- `cargo fmt --check`
- `cargo clippy --all-targets --all-features -- -D warnings`
- `cargo test --all-features`
- Tauri 发布契约测试。

### Windows 安装布局端到端测试

测试脚本不是只运行开发目录中的 EXE，而是：

1. 构建 MSI 和 NSIS。
2. 对 MSI 执行管理安装提取。
3. 从提取后的真实安装布局启动桌面程序。
4. 验证内置 Go 后端存在并可启动。
5. 使用一个非 OmniShare HTTP 服务占用首选端口。
6. 验证 OmniShare 自动选择备用端口，而不会误连接其他服务。
7. 验证桌面进程数为 1，托管后端进程数为 1。
8. 独立启动已有 OmniShare 后端，验证桌面连接已有服务且不重复创建后端。
9. 再次启动桌面程序，验证第二个进程退出并保持单实例。
10. 提取 Windows 关联图标，验证安装程序中的 EXE 图标有效。

发布资产同时包含：

- Windows 安装包 SHA-256 清单。
- Linux 安装包 SHA-256 清单。
- Windows 运行测试 JUnit 报告。

## 测试过程中发现并修复的问题

新增的 MSI 运行测试发现了仅靠编译和打包无法发现的 Windows 后端启动缺陷：

- 配置文件已经完成文件级 `fsync` 和原子重命名。
- 后端随后对目录句柄执行 Unix 风格目录同步。
- Windows 返回 `ERROR_ACCESS_DENIED`，导致后端把一次成功持久化误判为启动失败。

现在 Windows 继续保留文件级同步、原子重命名和 `.bak` 备份，但不再因为不支持的目录级同步而退出；Linux/macOS 仍执行严格目录同步。

## 下载建议

### Windows

普通用户优先下载：

```text
OmniShare_1.0.0_x64-setup.exe
```

需要 MSI 部署时下载：

```text
OmniShare_1.0.0_x64_en-US.msi
```

安装后直接启动 OmniShare。关闭主窗口时程序会继续驻留系统托盘；使用托盘菜单可以重新打开窗口或完全退出。

### Linux

根据发行版选择 AppImage、DEB 或 RPM。首次运行后应确认 WebKit 运行时、托盘/AppIndicator 和文件传输功能正常。

### macOS

本次 Release 不发布未签名的正式 macOS 下载包。正式 macOS 版本需要：

- Apple Developer ID Application 证书。
- Hardened Runtime。
- Apple 公证和 stapling。
- Gatekeeper `spctl` 验证。
- Apple Silicon（包括 M4）和 Intel 真机首次启动验收。

## 已知限制

- 当前主存储仍是单 JSON 快照，不适合高并发和超大规模数据。
- 当前只有单管理员密钥，没有完整 RBAC 和作用域令牌。
- 上传仍是单请求流式上传，不支持分片和断点续传。
- HTTPS 证书需要用户自行提供。
- Windows 托盘的物理可见性和点击行为仍应在 Windows 10/11 真机完成最终验收。

## 相关文档

- `docs/DESKTOP_TEST_PLAN.md`
- `docs/DESKTOP_RELEASE.md`
- `docs/TAURI_MIGRATION_PLAN.md`
- `SECURITY.md`
