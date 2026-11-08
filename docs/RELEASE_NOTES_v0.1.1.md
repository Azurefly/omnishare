# OmniShare v0.1.1 发布说明

发布日期：2026-11-08。

`0.1.1` 是 macOS 发布包装与兼容性修正版本，同时统一后端、Web 与 Windows/Linux 桌面版本号。随手记、文件、分享、文档、SQLite 存储和备份格式保持现有能力。

## macOS 更新

此前的 macOS 包将 Go 后端直接放入 `.app` 后压缩，没有在打包阶段为完整应用签名及验证。`0.1.1` 改为在 macOS 原生环境完成应用包的 **ad-hoc 完整性签名**，并对最终 ZIP 的解压结果、签名、实际应用启动和篡改副本进行检查。

最低系统要求明确为 **macOS 12 Monterey**，与发布使用的 Go 1.26 一致；旧的 macOS 11 声明不能保证二进制在该系统运行。Go 官方要求见[最低系统版本说明](https://go.dev/wiki/MinimumRequirements)。

**ad-hoc 签名不等于 Developer ID 或 Apple 公证。** 本次改动加强应用包完整性与运行验证，首次浏览器下载后仍可能被 Gatekeeper 拦截，不能承诺无提示启动。当前 macOS 交付形式仍是 Go 后端加浏览器界面的 `.app`，不是 Tauri 桌面安装器。

## 下载

下载与 SHA-256 清单见 [v0.1.1 Release](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1)，以实际附件为准。

| 使用方式 | 文件 |
| --- | --- |
| Apple 芯片 Mac，macOS 12 或更新版本 | `OmniShare-v0.1.1-macos-arm64.zip` |
| Intel Mac，macOS 12 或更新版本 | `OmniShare-v0.1.1-macos-amd64.zip` |
| Windows x64 桌面 | `OmniShare_0.1.1_x64-setup.exe` 或 `OmniShare_0.1.1_x64_en-US.msi` |
| Linux x64 桌面 | `OmniShare_0.1.1_amd64.AppImage`、`OmniShare_0.1.1_amd64.deb` 或 `OmniShare-0.1.1-1.x86_64.rpm` |
| Windows 后端与 Web | `OmniShare-v0.1.1-windows-amd64.zip` / `OmniShare-v0.1.1-windows-arm64.zip` |
| Linux 后端与 Web | `OmniShare-v0.1.1-linux-amd64.tar.gz` / `OmniShare-v0.1.1-linux-arm64.tar.gz`，或 `omnishare-server_0.1.1_{amd64,arm64}.deb` |

### Mac 首次打开

1. 在“关于本机”中确认芯片，下载对应 ZIP 并与同一 Release 的 SHA-256 清单核对。
2. 完整解压后放入应用目录。更新时先退出旧后端，再整包替换 `.app`，保留业务数据目录。
3. 对于“无法验证开发者”或未公证提示，只有在确认来源和文件校验后，才按 Apple 的“系统设置 → 隐私与安全性 → 仍要打开”流程建立单应用例外。
4. 若仍提示“已损坏”，不要将其直接当成普通开发者警告。重新核对文件，再按 [macOS 安装与诊断](https://github.com/Azurefly/omnishare/blob/main/docs/MACOS.md)收集系统、架构、`codesign` 与 `spctl` 的只读结果。

Apple 对警告的解释见[在 Mac 上安全地打开 App](https://support.apple.com/zh-cn/102445)。从 macOS Sequoia 起，Control-click 不能直接覆盖相关 Gatekeeper 检查，应到[系统设置审阅安全信息](https://developer.apple.com/news/?id=saqachfa)。

## 更新与数据兼容

### 已使用 0.1.0

这是常规补丁更新。先导出备份并退出应用及后端，再按原安装方式更新到 `0.1.1`，继续使用原数据目录；不要求所有用户先卸载旧应用。macOS 更换整个 `.app`，不要合并内部文件，也不要删除 `~/.omnishare`。

本次不更改 SQLite 数据格式或备份容器版本。默认仍使用 SQLite WAL；新加密备份继续写 v3 `.osb`，可恢复 v3、v2 和明文 ZIP。应用版本号与备份格式版本相互独立。

### 仍使用此前的 1.x

`0.1.0` 开始的版本号重置仍会影响迁入：当前 `0.1.1` 在安装器排序中低于 `1.x`。先备份并停止旧进程，只有在安装器拒绝覆盖时，才卸载旧应用但保留数据目录，然后安装当前版本并沿用原路径。

| 原启动方式 | 默认业务数据目录 |
| --- | --- |
| Windows Tauri 桌面或直接运行后端 | `%USERPROFILE%\.omnishare` |
| Windows ZIP 启动/安装脚本 | `%APPDATA%\OmniShare` |
| Linux Tauri 桌面、直接运行后端或服务器 DEB | `~/.omnishare` |
| Linux tar.gz 用户级安装脚本 | `${XDG_DATA_HOME:-$HOME/.local/share}/omnishare` |
| macOS 后端 `.app` 或直接运行后端 | `~/.omnishare` |

自定义过路径的环境以实际启动参数为准，可通过 `OMNISHARE_DATA_DIR` 或后端 `--data-dir` 指定原目录。旧 `omnishare.json` 可迁移到当前 SQLite；早期 `backend/data` SQLite 原型没有自动转换工具，应保留原程序与完整数据目录，在副本上转换验证。

恢复备份仍会替换当前内容与配置，包括备份之后新增的数据。恢复前先备份并暂停相关写入；加密口令不会保存在数据目录，丢失后无法恢复对应包。

## 验证与边界

macOS 验证针对最终发布 ZIP，覆盖：

- 含空格路径下解压后的布局、执行权限、最低系统版本、架构与完整签名。
- 通过实际 `.app` 启动独立后端，核对健康接口版本与页面响应。
- 修改独立副本后签名校验必须失败，避免只验证未修改样本。
- 记录真实 Gatekeeper 评估输出，明确 `notarized:false`、`first_download_verified:false`。

发布附件提供 `omnishare-macos-amd64-verification.json` 与 `omnishare-macos-arm64-verification.json`，实际结果以附件为准。CI 使用独立数据目录和 `--no-browser`，没有将自动化启动等同于 Safari 下载后的首次 Gatekeeper 放行，也没有删除隔离标记或关闭全局安全策略。

原有 Go/HTTP、前端、Windows 安装布局和 Linux 打包验证继续执行。Windows 商业代码签名、macOS Developer ID 与公证、Tauri macOS 正式安装包及相关真机验收仍未完成。已知共享、权限与数据边界见[安全共享说明](https://github.com/Azurefly/omnishare/blob/main/docs/SECURE_SHARING.md)。
