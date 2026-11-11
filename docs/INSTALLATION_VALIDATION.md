# 安装验证与支持边界

## 发布状态

当前公开版本仍是 [v0.1.1](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1)。本文描述当前源码新增的安装修复和发布门禁；这些改动尚未作为新的安装包发布。新增检查已接入工作流，必须取得对应提交、对应安装包的原生成功报告，才能宣称该候选版本通过验收。

macOS 正式发布仍缺所需签名凭据。现有 `v0.1.1` 的 ad-hoc ZIP 不会因新增代码变成已公证包；安装与签名要求见 [MACOS.md](MACOS.md) 和 [MACOS_SIGNING.md](MACOS_SIGNING.md)。

## 平台范围

| 平台 | 新增验收执行的路径 | 需要独立确认的范围 |
| --- | --- | --- |
| Windows x64 MSI | 真正执行 `/i` 安装到 `%ProgramFiles%\OmniShare`；验证机器级注册与快捷方式、原生窗口和 WebView2 页面；正常退出、重启、`/x` 卸载。 | MSI 需要管理员权限；当前 CI 不能代表所有 Windows 10/11 实体机器。 |
| Windows x64 NSIS | 真正执行当前用户安装到 `%LOCALAPPDATA%\OmniShare`；完成同样的界面、核心功能、退出、重启和卸载检查。 | 配置为当前用户安装不等于已经在非管理员账户完成全流程。 |
| Ubuntu 22.04、24.04 x64 DEB | 使用同一次 Ubuntu 22.04 构建的包，通过 APT 安装；验证桌面入口，读取真实 GTK/WebKit 内容，正常退出后重启，卸载并核对业务数据保留。 | Xvfb 与 D-Bus 提供自动化图形会话；不同实体桌面、Wayland 和托盘行为仍需测试。 |
| Ubuntu 22.04、24.04 x64 AppImage | 从含空格、中文和特殊字符的路径运行 `--appimage-extract-and-run`，检查内置程序、WebKit、功能和重启数据。 | 此路径不使用 FUSE；FUSE 挂载启动和其他发行版未由该检查覆盖。 |
| macOS Intel、Apple 芯片 | 最终 ZIP/DMG 有独立签名、结构、安装复制、启动、数据与退出验收；正式模式另有 Developer ID、公证、票据和策略门禁。 | PR ad-hoc 演练不通过正式签名门禁；真实 Safari 首次下载和用户安装需独立记录。 |

Linux 桌面构建固定在 Ubuntu 22.04，DEB 声明 `libc6 (>= 2.35)`，测试读取已安装桌面程序和后端的符号要求。这个基线适用于新的构建流程。RPM 继续构建和校验摘要，当前没有等价的 RPM 原生安装验收；旧 `v0.1.1` 二进制也不能从新基线推导兼容性。

## 真实界面与核心功能

Windows 验收要求应用自己拥有可见原生窗口，再通过限定回环地址、核对进程归属的 CDP 连接读取实际 WebView2。Ubuntu 通过图形可访问性接口读取实际 WebKit。在首次配置和重启后，都检查界面显示的内容；HTTP 健康检查单独记录。

共享探针 [`scripts/test-installed-features.py`](../scripts/test-installed-features.py) 使用已经安装并运行的后端，执行两个阶段：

| 阶段 | 检查 |
| --- | --- |
| 使用：7 项 | 健康与内嵌页面；首次初始化和鉴权；中文笔记与回收站恢复；文档与版本冲突；文件上传、下载与 Range；分享访问与撤销；加密备份的错误口令保护和恢复。 |
| 重启：5 项 | 健康与内嵌页面；配置持久化；笔记和阅读计数；文件内容摘要；文档内容与版本。 |

Windows MSI 使用真实默认业务目录 `%USERPROFILE%\.omnishare`，NSIS 使用独立的含中文、空格、`#`、`%` 路径，覆盖 SQLite 文件 URI 的路径修复。两种安装器分别完成使用、正常退出、重启验证和卸载，验证卸载不改变原有业务数据。

原生菜单退出和托盘退出共用生命周期。Windows/Linux 使用 `Ctrl+Q`，macOS Tauri 使用 `Cmd+Q`。自己启动的后端通过私有标准输入关闭请求正常刷盘，退出期间禁止 watchdog 拉起新进程；连接已有后端时保留该外部服务。应急强杀只用于失败后清理，不能满足正常退出检查。

## WebView2 的离线依赖

当前源码将两种 Windows 安装器设置为完整 `offlineInstaller`，构建阶段仍需要获取微软的运行时。构建后、执行安装器前，检查 MSI 和 NSIS 各自缓存中的完整 EXE：大小在 64–512 MiB、Authenticode 状态为 `Valid`、签名者为 `Microsoft Corporation`，同时记录产品版本和 SHA-256。缺失、歧义、链接文件或验证失败会阻止继续安装。

这份证据覆盖固定 Tauri CLI 所用的缓存文件及对应安装包摘要，没有独立从安装器内反解运行时。它也不能代替没有 WebView2 的冷机、断网和非管理员安装验证。因此总报告保留 `cold_runtime_install_verified: false`、`offline_runtime_install_verified: false`、`non_admin_install_verified: false`；某个测试环境安装前后运行时清单的变化另行记录。微软运行时的有效签名不表示 OmniShare 安装包已经获得商业代码签名或 SmartScreen 信誉。

## 失败清理与证据

Windows 在每次安装前拒绝既存 OmniShare 注册项和目标安装目录，并在真正启动安装命令前记录本次尝试。即使命令超时，也重新读取注册项：MSI 仅允许包内 ProductCode 对应的机器级产品；NSIS 仅允许当前用户、固定产品键、安装路径与完整卸载程序一致的产品。未知或多重注册项会拒绝清理。测试只删除带本次所有权标记的数据目录，避免将失败清理变成对已有用户数据的卸载或删除。

访问密钥、初始化令牌、私有功能状态、数据库和 WebView 用户资料留在临时目录，不上传。公开报告包含安装包摘要、检查结果和安全的功能阶段报告。截图来自测试数据；与凭据有关的地址参数在截图前移除。

新发布门禁要求以下三份安装报告与同次构建的实际包匹配：

- `omnishare-windows-installation.json`：MSI/NSIS、WebView2 签名摘要和两阶段功能报告。
- `omnishare-ubuntu-22.04-installation.json`：Ubuntu 22.04 的 DEB/AppImage 与两阶段报告。
- `omnishare-ubuntu-24.04-installation.json`：Ubuntu 24.04 的 DEB/AppImage 与两阶段报告。

[`scripts/verify-installation-evidence.cjs`](../scripts/verify-installation-evidence.cjs) 检查版本、成功状态、必需检查、安装包摘要以及私有字段泄漏；缺失或失败的证据会阻止发布。新汇总门禁共有 **29 个附件**，`SHA256SUMS.txt` 校验除自身外的 **28 个文件**。现有 `v0.1.1` 附件维持原样，新的报告数量不追溯修改旧版本。

维护者运行方式见[桌面发布指南](DESKTOP_RELEASE.md)。正式下载后的首次安装、实体设备交互和上述未覆盖范围，应作为独立结果记录，保留具体系统、安装包 SHA-256、步骤和结论。
