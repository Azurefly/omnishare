# OmniShare v1.3.0-rc3 发布说明

发布日期：2026-11-07。版本定位：**Release Candidate / 预发布**。

本次将安全加固、SQLite 存储、加密备份和最新可靠性修复统一到 `main`，后端、Web 与 Windows/Linux 桌面使用同一源码版本发布。旧分支的提交历史完整保留。

## 主要更新

- **默认 SQLite WAL 存储**：支持从旧 JSON 自动迁移并保留原文件副本；已有 SQLite 无法打开时明确停止，避免误用迁移前的旧数据。
- **v3 加密备份**：使用 AES-256-GCM，认证完整包头、数据分块顺序和结束记录，拒绝截断和尾随数据；PBKDF2 使用 Go 标准库并限制不可信参数的计算量。
- **恢复可靠性**：恢复失败时保留最终切换前已成功写入的数据；使用真实 HTTP 连接的空闲读超时，覆盖上传停发和请求收尾的阻塞场景。
- **阅读限制正确性**：修复 SQLite 阅读上限和阅后即焚在并发下被重复消费的问题；备份包含未刷盘计数，失败刷盘可重试，恢复后清理旧批次。
- **桌面首次初始化**：为桌面自行启动的后端传入实际数据目录并交接初始化令牌，使首次安装后可完成访问密钥配置。
- **前端与草稿**：会话存储不可用时保留当前页面内存后备；保存冲突保留最新编辑内容；备份先选择文件再传输，失败时中止写入；恢复防重复提交。
- **主题及既有桌面修复**：包含白天、黑夜、跟随系统主题，以及后端 watchdog、端口冲突避让、已有后端连接和单实例行为。

## 下载选择

全部附件见 [本版本 Release](https://github.com/Azurefly/omnishare/releases/tag/v1.3.0-rc3)。请按操作系统、CPU 架构及使用方式选择，桌面安装包和服务器便携包提供不同的启动方式。

| 使用方式 | 附件 |
| --- | --- |
| Windows x64 桌面 | `OmniShare_1.3.0-rc3_x64-setup.exe`；MSI 部署使用 `OmniShare_1.3.0-rc3_x64_en-US.msi` |
| Linux x64 桌面 | 名称以 `OmniShare` 开头的 AppImage、DEB 或 RPM；RPM 包版本按系统要求映射为 `1.3.0`、release 为 `0.rc3` |
| Windows 便携后端 + Web | `OmniShare-v1.3.0-rc3-windows-amd64.zip` / `OmniShare-v1.3.0-rc3-windows-arm64.zip` |
| Linux 后端 + Web | `OmniShare-v1.3.0-rc3-linux-amd64.tar.gz` / `OmniShare-v1.3.0-rc3-linux-arm64.tar.gz`，以及小写 `omnishare_1.3.0.rc3_*.deb` |
| macOS 后端 + Web | `OmniShare-v1.3.0-rc3-macos-amd64.zip` / `OmniShare-v1.3.0-rc3-macos-arm64.zip`，内部为单后端进程的 `.app` 启动包装 |
| 完整性与验证 | SHA-256 清单、Windows 运行及 watchdog 的 JUnit / JSON 证据 |

**macOS 边界：** 本版本的 macOS ZIP 是未签名的后端启动包装，并非 Tauri Universal 桌面安装包。macOS Tauri 桌面签名与公证尚未完成，因此不在本次公开桌面附件范围内；Gatekeeper 可能限制未签名程序的运行。Windows 安装包也尚未提供商业代码签名。

Windows 便携包解压后运行 `start-omnishare.cmd`，并保留同目录的 `start-omnishare.ps1` 和 `omnishare.exe`。ARM64 Windows 使用便携包；本次 Tauri Windows 安装器为 x64。

## RC3 公开校验清单更正

发布后确认 GitHub 将两个服务器 DEB 文件名中的 `~` 规范化为 `.`，但原附件 `SHA256SUMS.txt` 仍保留 `~rc3`。包内容与哈希没有变化；按公开名字下载后，原清单会提示找不到这两个文件。

需要校验 RC3 的用户可使用仓库中的 [修正版 SHA256SUMS.txt](../release/checksums/v1.3.0-rc3/SHA256SUMS.txt)。修正版只调整这两个名称，全部 11 项哈希与原清单一致，并已对照公开 Release 的资产 digest 验证。原 RC3 tag 和二进制没有改写。RC4 起从打包阶段采用不会被 GitHub 改写的文件名，并增加公开附件校验。

## 升级与数据

1. 在旧程序中完成一次备份，记录正在使用的数据目录，然后退出程序及后台服务。
2. 安装新版本或替换程序文件，继续使用原数据目录。不要把备份、配置或用户上传目录替换成空目录。
3. 首次使用 SQLite 时会迁移原 JSON，并在数据目录保留带时间戳的迁移前副本；已有数据库打开失败时按日志处理，不会自动退回陈旧 JSON。
4. 原先没有访问密钥时，使用本机启动 URL 中的一次性初始化令牌完成设置；桌面自己启动的后端会自动交接令牌。已有访问密钥继续有效。

从便携后端切换到桌面端时，应先核对数据目录。Windows 便携启动脚本默认使用 `%APPDATA%\OmniShare`，Tauri 桌面及直接运行的后端默认使用 `%USERPROFILE%\.omnishare`；Linux Tauri 默认为 `~/.omnishare`，tar.gz 的用户级安装脚本使用 XDG 数据目录。Tauri 支持以 `OMNISHARE_DATA_DIR` 指定原目录，直接运行后端可用 `--data-dir`。改变启动方式并不等于自动搬迁数据。

### 备份兼容性

| 备份格式 | RC3 恢复能力 | 使用说明 |
| --- | --- | --- |
| 新生成的 v3 `.osb` | 支持 | 恢复时须使用 RC3 或更新版本，旧程序不认识该格式 |
| 旧版 v2 `.osb` | 支持 | 保留旧容器原有认证能力，不会自动变成 v3 |
| 未加密 ZIP | 支持 | 未设置导出口令时仍使用 ZIP；内容未加密 |

成功恢复会用备份替换当前状态，包括备份之后产生的新数据。本次改进保证失败恢复回滚到最终切换前的状态。加密备份口令不会保存在数据目录中，恢复时必须提供原口令。

## 发布验证

发布流程先验证清单版本与 `VERSION` 一致，再构建和测试服务器包、Windows/Linux 桌面安装包；全部任务成功后才创建本预发布。

- 仓库 CI：Go 全仓测试、Race Detector、格式、`go vet`、Staticcheck、覆盖率门槛，以及前端语法、静态契约和 12 项行为测试。
- 服务器发布：版本一致性、六目标交叉构建、Windows ZIP 启动器验证和包 SHA-256 校验。
- Linux 桌面：Rust 格式、Clippy、单元测试、安装包构建和校验清单。
- Windows 桌面：MSI 安装布局启动、端口冲突回退、已有后端连接、单实例、后端自动恢复与本机设备摘要检查。
- 发布附件包含运行测试证据；具体结果可在 [GitHub Actions](https://github.com/Azurefly/omnishare/actions) 查看。

## 保留的限制与后续工作

- RC3 仍为候选版本；完整真实浏览器交互验收、Windows 托盘真机操作和 macOS 签名公证尚未完成。
- 普通阅读/下载计数仍按周期批量落库，进程突然崩溃可能损失最近几秒的普通计数；受限阅读同步提交。
- 清除访问密钥或恢复无密钥配置后的初始化令牌续发、自动清理与恢复的文件协调，以及审计锚点的备份恢复仍是后续专项。
- 当前为单管理员访问密钥模型；角色权限、自动 TLS 和分片断点续传不在本版本范围内。

完整问题与验证记录见 [分支整合与可靠性优化](https://github.com/Azurefly/omnishare/blob/v1.3.0-rc3/docs/INTEGRATION_2026-11-07.md)。
