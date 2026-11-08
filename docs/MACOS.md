# macOS 安装与诊断

**当前已发布的 `0.1.1` 只有 ad-hoc 完整性签名，没有 Developer ID 身份签名或 Apple 公证。** 它仍可能被下载后的 Gatekeeper 检查阻止。仓库正在完善正式签名、公证及 DMG 安装流程；这些改进尚不代表新的安装包已经发布或完成首次下载验收。

macOS 包中的 `OmniShare.app` 启动本机 Go 后端并打开浏览器界面，不是 Tauri macOS 桌面安装器。后续正式分发目标是已签名、公证并附加票据的 DMG，以及同一应用的 ZIP 备用包。维护者配置和发布门槛见 [macOS 正式签名与公证](MACOS_SIGNING.md)。

## 系统要求与当前下载

**最低系统要求为 macOS 12 Monterey。** 当前发布使用 Go 1.26，其最低 macOS 版本为 12；更改 `Info.plist` 不能让不受支持的系统运行二进制。见 [Go 最低系统要求](https://go.dev/wiki/MinimumRequirements)。

在 Apple 菜单的“关于本机”查看芯片或处理器，再选择 [v0.1.1 Release](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1) 中的文件：

| Mac 类型 | 当前已发布文件 |
| --- | --- |
| Apple 芯片（M 系列） | `OmniShare-v0.1.1-macos-arm64.zip` |
| Intel 处理器 | `OmniShare-v0.1.1-macos-amd64.zip` |

两份 ZIP 分别面向对应架构；`0.1.1` 没有公证 DMG。以 Release 的实际附件和同页 SHA-256 清单为准，不要将 CI 演练 artifact 当成正式安装包。

## 验证来源和下载文件

从项目的 GitHub Release 下载包及 `SHA256SUMS.txt`。例如，Apple 芯片的 `0.1.1` 包可在终端计算：

```bash
shasum -a 256 "$HOME/Downloads/OmniShare-v0.1.1-macos-arm64.zip"
```

将结果与清单中**同一文件名**的那一行逐字比较；Intel 包使用对应的 `macos-amd64.zip` 文件名。结果不一致时停止使用该副本并重新下载。SHA-256 一致只表示字节符合发布清单，不等于已获得 Apple 身份信任或公证。

## 正式签名包的安装方式

以下步骤用于后续**实际发布并标明 Developer ID、公证和验证结果**的版本。当前 `0.1.1` 不满足这个前提，不能据此承诺它首次下载后直接打开。

1. 下载与 Mac 架构匹配的 DMG，并核对 SHA-256。
2. 双击 DMG，将完整的 `OmniShare.app` 拖入“应用程序”（`Applications`）。更新时先退出旧后端，再替换整个应用；不要合并新旧 `.app` 内的文件。
3. 推出 DMG，从“应用程序”打开 OmniShare。应用启动本机后端并打开默认浏览器。
4. 如果使用 ZIP 备用包，用 Finder 解压、将完整应用复制到“应用程序”，再打开。ZIP 中的应用也必须携带相同的正式签名与公证票据。

首次下载可能出现 macOS 正常的“从互联网下载，是否打开”确认。正式分发的验收目标是正常确认后即可使用，无需“仍要打开”（Open Anyway）安全例外；不承诺任意受管理设备或组织策略都允许运行。Apple 对系统提示的说明见[《在 Mac 上安全地打开 App》](https://support.apple.com/zh-cn/102445)。

默认业务数据位于 `~/.omnishare`，不在 `.app` 内。使用过 `OMNISHARE_DATA_DIR` 或 `--data-dir` 时应沿用原目录，更新前保留备份。更换应用包不需要删除数据目录。

## 当前 0.1.1 或其他包被阻止时

| 提示 | 含义与下一步 |
| --- | --- |
| 无法验证开发者，或 Apple 无法检查是否包含恶意软件 | 属于开发者身份或公证信任问题。`0.1.1` 没有完成受信任分发，不能用 ad-hoc 验签结果宣称这一问题已解决。核对版本并保留提示，等待完成正式发布门槛的新包。 |
| “已损坏，无法打开，应移到废纸篓” | 可能涉及包结构、签名、文件完整性或系统策略。先核对 ZIP 摘要并重新完整解压，仍失败时收集下述诊断，不能单凭该文字确定根因。 |
| 会损坏电脑、检测到恶意软件，或系统已将其移入废纸篓 | 停止运行该副本，保留准确警告并报告。 |

## 收集只读诊断

确认下载架构与 Mac 对应，系统至少为 macOS 12。若应用不在 `/Applications`，只修改下列第一行路径：

```bash
omnishare_app="/Applications/OmniShare.app"
sw_vers
uname -m
codesign -d -vvv "$omnishare_app"
codesign --verify --deep --strict --verbose=2 "$omnishare_app"
spctl --assess --type execute --verbose=2 "$omnishare_app"
```

macOS 14 或更新版本还可运行：

```bash
syspolicy_check distribution "$omnishare_app"
```

这些命令读取信息，不修改应用或系统安全设置：

- `codesign -d` 显示签名身份。`0.1.1` 应显示 ad-hoc；未来正式包应显示与发布团队一致的 Developer ID Application 身份和时间戳。
- `codesign --verify` 检查代码及资源完整性。出现签名无效、缺失资源或资源被更改时，保留原输出，不要重新签名掩盖问题。
- `spctl` 和 `syspolicy_check` 检查系统分发策略。ad-hoc 包可能完整性正确但策略仍拒绝；公证成功也不能代替实际安装验证。

报告问题时附上下载文件名、SHA-256、macOS 版本、芯片信息、准确警告与命令输出。遮盖个人路径，不要提供访问密钥、初始化令牌或业务数据。Apple 的[公证产品测试说明](https://developer.apple.com/forums/thread/130560)解释了命令行评估与真实首次下载测试的区别。

## 已验证内容与尚未完成的内容

`0.1.1` 在原生 Intel 和 Apple 芯片运行器上组装完整 `.app` 并做 ad-hoc 签名；测试将最终 ZIP 解压到含空格的路径，核对布局、权限、架构和签名，通过独立副本篡改确认签名校验能拒绝变化，并验证实际应用的启动、健康接口版本、页面响应与正常退出。

这些运行检查使用独立数据目录、端口及 `--no-browser`。它们不等于用户通过浏览器下载后的信任验证。`0.1.1` 的证据中，`notarized` 与 `first_download_verified` 均为 `false`，Gatekeeper 评估拒绝；详见 Release 中的：

```text
omnishare-macos-amd64-verification.json
omnishare-macos-arm64-verification.json
```

后续正式发布要求 Developer ID、Hardened Runtime、安全时间戳、Apple 公证 `Accepted`、票据验证及 Gatekeeper 门槛全部通过。最终 DMG/ZIP 的 SHA-256 必须与发布证据和公开附件一致。

**真实首次下载验收仍是独立步骤：** 在从未见过该产品的 Mac 或干净虚拟机快照中，通过 Safari 下载正式文件，保留下载隔离状态，按用户路径安装和启动，并另行验证离线票据。原生 CI 报告的 `first_download_verified` 始终为 `false`；人工首装另行记录，不能由自动化构建、签名、公证或模拟隔离检查代替。

签名、公证和票据的技术要求见 [Apple 公证前置条件](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)及[自定义公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)。
