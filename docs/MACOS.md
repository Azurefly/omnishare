# macOS 安装与诊断

本文适用于 OmniShare `0.1.1` 的 macOS 后端发布包。解压后的 `OmniShare.app` 启动本机 Go 后端并打开浏览器界面；它不是 Tauri 桌面安装器。

## 选择正确的下载

**最低系统要求为 macOS 12 Monterey。** 发布包使用 Go 1.26；Go 官方列出的 1.25/1.26 最低版本是 macOS 12，Go 1.27 则要求 macOS 13。修改 `.app` 的版本声明不能让不支持的系统运行该二进制。见 [Go 最低系统要求](https://go.dev/wiki/MinimumRequirements)。

在 Apple 菜单的“关于本机”中查看芯片或处理器，然后从 [v0.1.1 Release](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1) 下载：

| Mac 类型 | 下载文件 |
| --- | --- |
| Apple 芯片（M 系列） | `OmniShare-v0.1.1-macos-arm64.zip` |
| Intel 处理器 | `OmniShare-v0.1.1-macos-amd64.zip` |

这两个文件分别面向对应架构，不是一个 Universal Tauri 安装包。以 Release 实际附件及同页 SHA-256 清单为准。

## 安装与首次打开

### 1. 验证来源和 ZIP

从项目的 GitHub Release 下载 ZIP 与 `SHA256SUMS.txt`。在终端计算下载文件的 SHA-256，例如 Apple 芯片版本：

```bash
shasum -a 256 "$HOME/Downloads/OmniShare-v0.1.1-macos-arm64.zip"
```

Intel 版本使用对应的 `macos-amd64.zip` 文件名。将结果与清单中**同一文件名**的那一行逐字比较；如果不同，停止使用该副本并重新下载。校验和一致说明拿到的字节与发布清单相符，不代表应用已获得 Apple 身份验证或公证。

### 2. 解压并保留原数据

用 Finder 解压已校验的 ZIP，将完整的 `OmniShare.app` 放入所需应用目录，例如 `/Applications`。更新已有应用时先退出旧后端，再整包替换；不要合并新旧 `.app` 的内部文件，以免破坏签名覆盖的内容。

默认业务数据位于 `~/.omnishare`，不在 `.app` 内。使用过 `OMNISHARE_DATA_DIR` 或 `--data-dir` 的环境应沿用原路径。更新前保留数据备份；替换应用包不需要删除数据目录。

### 3. 按提示决定下一步

`0.1.1` 为整个 `.app` 添加了 **ad-hoc 完整性签名**，但没有 Developer ID 身份签名或 Apple 公证。浏览器下载后仍可能被 Gatekeeper 拦截。

| 提示 | 含义与处理 |
| --- | --- |
| 无法验证开发者，或 Apple 无法检查是否包含恶意软件 | 这属于开发者身份或公证信任问题。只有在来源明确、ZIP 校验一致并愿意信任该应用时，才使用下述单应用允许流程。 |
| “已损坏，无法打开，应移到废纸篓” | 可能涉及文件、包结构或签名完整性，不能直接当成普通未验证开发者提示。重新核对 ZIP 并完整解压；仍出现时收集下一节的诊断。 |
| 会损坏电脑、检测到恶意软件，或系统已将其移入废纸篓 | 停止运行该副本，向项目提供准确提示；不要把这类拦截按普通未公证提示处理。 |

Apple 对这些提示的区别与处理条件见[《在 Mac 上安全地打开 App》](https://support.apple.com/zh-cn/102445)。错误文字可能随 macOS 版本变化，仅凭一张警告不能确定根因。

对于来源和校验均确认的未验证开发者应用：

1. 先在 Finder 尝试打开 `OmniShare.app`。
2. 打开“系统设置 → 隐私与安全性”，在安全性区域查看**针对 OmniShare**的提示。
3. 如果系统提供“仍要打开”（Open Anyway），核对应用名称后选择它，并按提示再次确认。

Apple 将这一流程描述为[为单个应用建立安全例外](https://support.apple.com/guide/mac-help/mh40617/mac)，仍由用户决定是否信任。某些系统或受管理设备可能不提供该选项。从 macOS Sequoia 起，Control-click 不再能直接覆盖这类 Gatekeeper 拦截，Apple 要求到系统设置审阅并决定，见[运行时保护更新](https://developer.apple.com/news/?id=saqachfa)。

## 仍提示“已损坏”时：收集只读诊断

先确认运行的是刚刚校验并重新解压的副本，系统至少为 macOS 12，下载架构与 Mac 对应。然后在终端运行下列命令；如果应用不在 `/Applications`，只修改第一行的路径：

```bash
omnishare_app="/Applications/OmniShare.app"
sw_vers
uname -m
codesign -d -vvv "$omnishare_app"
codesign --verify --deep --strict --verbose=2 "$omnishare_app"
spctl --assess --type execute --verbose=2 "$omnishare_app"
```

这些命令读取系统、签名和策略评估信息，不修改应用内容或全局安全设置。

- `codesign -d` 用于查看签名信息。当前包应显示 ad-hoc 签名，不应被描述为 Developer ID 已认证应用。
- `codesign --verify` 检查代码及资源完整性。如果报告缺失或无效资源、签名无效，应保留原输出并报告，不要自行重新签名掩盖问题。
- `spctl` 评估 Gatekeeper 策略。对于没有 Developer ID 和公证的 ad-hoc 包，出现拒绝结果并不等于下载字节损坏；它与 `codesign` 的完整性检查是不同层面的结果。

Apple 的开发者诊断说明见 [Resolving Gatekeeper Problems](https://developer.apple.com/forums/thread/706379) 和 [Testing a Notarised Product](https://developer.apple.com/forums/thread/130560)。命令行评估不能代替真实首次下载和打开的体验测试。

报告问题时附上：下载文件名、计算得到的 SHA-256、macOS 版本、“关于本机”中的芯片信息、完整警告文字和上述命令输出。分享前遮盖个人路径信息；不需要提供访问密钥、初始化令牌或业务数据。

## 签名、公证与隔离标记分别做什么

- **ad-hoc 签名**记录代码与资源的完整性，不证明开发者身份。Apple 明确说明这类签名不携带有效开发者身份，不能因此通过默认 Gatekeeper 信任检查；Apple 芯片的本地代码签名要求与 Gatekeeper 分发信任是两件事。见 [Apple 的代码签名说明](https://developer.apple.com/documentation/macos-release-notes/macos-big-sur-11_0_1-universal-apps-release-notes)。
- **Developer ID 与公证**用于 App Store 外的受信任分发。公证要求有效 Developer ID 签名、Hardened Runtime 和安全时间戳等条件；ad-hoc 不能替代这一流程。见 [Notarizing macOS software before distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)。
- **下载隔离标记**让 macOS 对互联网下载的内容进行检查。Safari 下载 ZIP、Finder 解压时会传播这类标记；移除标记不能修复失效签名，也不会产生 Developer ID 或公证。见 [Resolving Trusted Execution Problems](https://developer.apple.com/forums/thread/706442)。

本项目的安装步骤不自动清除下载隔离标记，也不要求全局关闭 Gatekeeper。若官方单应用允许入口不可用，或完整性诊断仍失败，应提交诊断信息等待修复包。

## 0.1.1 的验证范围

发布流程在 macOS 上生成并签名完整 `.app`，对最终 ZIP 解压到含空格的路径，核对布局、执行权限、最低系统版本、架构和签名。测试还从实际 `.app` 启动后端，在独立数据目录验证健康接口版本与页面响应；独立副本被修改后，完整性校验必须失败。

上述运行检查使用独立端口和 `--no-browser`，不会把 CI 启动成功当成用户浏览器下载后的 Gatekeeper 放行。发布证据会记录真实 `spctl` 诊断，`notarized` 和 `first_download_verified` 仍为 `false`；当前未完成 Developer ID、公证及完整首次下载验收。实际结果以 Release 随附的两份证据为准：

```text
omnishare-macos-amd64-verification.json
omnishare-macos-arm64-verification.json
```

正式完成受信任分发后，还需要在干净的 Mac 或虚拟机中通过 Safari 下载、按用户方式解压和启动，并验证票据离线可用性。Apple 的[公证工作流](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)说明：ZIP 本身不能 staple，应给其中的 `.app` 附加票据，再重新打包。公证成功仍需实际 Gatekeeper 验收，不能只凭上传公证服务成功就宣布完成。
