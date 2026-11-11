# macOS 正式签名与公证

本文面向发布维护者。目标是通过 Developer ID 签名、公证和用户安装路径验收，提供可拖入“应用程序”的 DMG，并保留 ZIP 备用下载。本轮实现使用 Swift/AppKit 原生应用作为入口，由它启动内嵌 Go 后端并打开默认浏览器，提供“打开 OmniShare”“退出 OmniShare”菜单和重新打开处理。

**当前已发布的 `0.1.1` 仍只有 ad-hoc 完整性签名，未经过 Apple 公证。** 本文及主分支的发布管线改进不改变已发布的标签或附件，也不表示新的受信任安装包已经发布。凭据就绪并完成正式验证后，才更新下一版本、发布清单和发布说明。

截至 **2026-11-11 01:57:59 UTC**，[macOS Signing Readiness 第 3 次执行](https://github.com/Azurefly/omnishare/actions/runs/42470861499/attempts/3)的凭据检查仍报告下列五项全部缺失。本轮 Swift 启动器及 ZIP/DMG 扩展运行验收尚待原生 CI；不得据文档或本地脚本测试宣称正式公证、原生安装或 Safari 首次下载已经通过。

## 1. 在自己的 Mac 上准备签名身份

App Store 外的正式分发需要 Apple Developer Program 团队下的 **Developer ID Application** 身份及其私钥。Apple Development、Apple Distribution、Developer ID Installer 或 ad-hoc 身份不能替代它。Developer ID Installer 适用于 PKG 安装器，本项目的 DMG/ZIP 发布不需要这个额外证书。

在自己受信任的 Mac 上完成以下步骤；不要将私钥、P12、导出密码、Apple 账户密码或专用密码发送到聊天、Issue、PR、代码仓库或构建日志。

1. 按 [Apple 的 Developer ID 证书说明](https://developer.apple.com/help/account/certificates/create-developer-id-certificates)，由有权限的团队成员创建 Developer ID Application 证书。在本机生成证书签名请求，保留对应私钥。
2. 将签发的证书安装到本机钥匙串。在“钥匙串访问 → 我的证书”中，确认该 Developer ID Application 证书可以展开，并显示关联的私钥。仅下载 `.cer` 不足以在 CI 签名。
3. 导出这一个签名身份及其私钥为带密码保护的 `.p12`。不要导出整套钥匙串；保留加密备份，记录所设导出密码。
4. 在 Apple 开发者账户中核对该证书所属的 Team ID。公证账号必须有权使用同一个团队。
5. 为该 Apple 账户启用双重认证，并按 [Apple 的专用密码说明](https://support.apple.com/102654)创建供公证使用的 app-specific password。这里使用专用密码，不使用 Apple 账户主密码。

证书创建、私钥生成和 P12 导出都在维护者自己的 Mac 上进行；发布脚本不代为创建 Apple 身份。

## 2. 配置五个 GitHub Actions secrets

在本仓库的 **Settings → Secrets and variables → Actions → New repository secret** 中配置以下五项，名称必须完全一致。操作入口见 [GitHub 官方说明](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)。

| Secret 名称 | 内容 |
| --- | --- |
| `APPLE_CERTIFICATE` | 上一步导出的、包含 Developer ID Application 私钥的 `.p12` 文件的 Base64 内容。 |
| `APPLE_CERTIFICATE_PASSWORD` | 该 `.p12` 的导出密码。 |
| `APPLE_TEAM_ID` | 与签名证书和公证账号对应的 Apple Developer Team ID。 |
| `APPLE_ID` | 有权限为该团队提交公证的 Apple 账户。 |
| `APPLE_APP_SPECIFIC_PASSWORD` | 为公证创建的 app-specific password，不是 Apple 账户主密码。 |

可在自己的 Mac 上将 P12 转为 Base64 后直接复制到系统剪贴板，再粘贴到 `APPLE_CERTIFICATE` 的 Secret 输入框，例如：

```bash
base64 -i "$HOME/Downloads/OmniShare-Developer-ID.p12" | pbcopy
```

将路径改为自己的 P12 文件；该命令不要在共享运行器或聊天环境执行。Base64 是编码，不是加密，其内容同样需要作为私钥材料保管。配置完成后清空剪贴板，并妥善保存或移除本地临时副本。

发布工作流在受信任的 `main` 上使用这些 secrets。PR 验证不导入生产签名身份。临时钥匙串、解码的 P12 和公证凭据由发布作业按需建立，并在结束时清理；它们不属于上传的构建或验证产物。

## 3. 先执行凭据就绪检查

在 Actions 中选择 **macOS Signing Readiness**，手动运行时选择 `main`。其中的凭据检查作业检查上述五个 secret 是否有值及基本格式，并列出缺失的**名称**。它不打印值、不签名、不提交公证，也不创建 Release；本次分支合并后的清理是另一个有独立权限和精确提交约束的作业。

就绪检查通过仅说明配置项存在，不能证明 P12 可解密、身份未过期、Team ID 一致或公证服务接受这些凭据。真正的 Developer ID 身份检查和 Apple 服务认证仍由正式发布门槛执行。缺少凭据或认证失败时，正式发布必须停止，不会退回 ad-hoc 模式发布给用户。

不要为了试验新流程而重新运行已发布 `v0.1.1` 的发布任务。当前版本与发布清单保持历史值；正式发布前再统一更新下一补丁版本，例如 `0.1.2`。

## 4. 正式发布链路

macOS Intel 与 Apple 芯片分别在原生运行器上处理对应架构的 Go 二进制。发布必须满足以下条件：

1. **组装完整原生应用。** 用对应架构的 Swift 编译器构建 `Contents/MacOS/OmniShare`，目标最低 macOS 12，并将 Go 后端置于 `Contents/MacOS/omnishare-server`。`Info.plist` 的 `CFBundleExecutable` 指向 Swift 启动器。写入最终说明和全部资源，核对两份可执行文件的架构、权限与应用元数据；随后再签名实际分发的完整应用。
2. **从内到外使用受信任身份签名。** 先签名并验证内嵌 `omnishare-server`，再签名并验证外层 `OmniShare.app`，以封存原生启动器、已签名后端及资源。两层代码都必须使用与配置团队匹配的 Developer ID Application 身份，开启 Hardened Runtime 并带安全时间戳。不要在外层签名后更改或重新签名内层文件，也不要增加无关的运行时例外。PR 演练同样按从内到外的顺序进行 ad-hoc 签名，但不获得正式分发资格。
3. **提交并核实公证。** 使用 `notarytool` 等待 Apple 返回，明确要求结果为 `Accepted`，并检查公证日志。上传成功或仅得到 submission ID 不算公证成功。
4. **附加并验证票据。** 使用 `stapler` 将票据附到应用和正式分发的 DMG，并执行票据验证。ZIP 本身不能附加票据；ZIP 需在应用完成 stapling 后重新生成。公开校验和必须对应最后生成的文件。
5. **验证实际分发包及完整功能。** 重新解压最终 ZIP；将 DMG 只读挂载，把完整应用复制到含空格的安装目录并推出映像后再运行。分别检查内外完整签名、架构、身份、Hardened Runtime、票据及 Gatekeeper 评估；资源被篡改的独立副本必须校验失败。两个安装路径都必须经 LaunchServices 启动，执行完整核心功能、原生重新打开、正常退出、原数据重启及端口冲突验收。
6. **聚合后发布。** 发布聚合检查必须确认正式签名模式及全部 **33 项**顶层门槛成立，并严格检查 ZIP、DMG 各自 exercise 7 项和 verify 5 项功能结果，将证据绑定到最终两份文件的名称、字节数与 SHA-256。正式模式要求系统策略启用，两份应用的 `syspolicy_check` 和应用、DMG 的 `spctl` 评估通过。发布后再次核对公开附件。旧标签只允许对应原提交，不重写历史附件来冒充新版本。

这些要求依据 [Apple 公证前置条件](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)、[命令行公证与 stapling](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)和[分发包格式说明](https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution)。DMG 提供安装界面和票据容器，不能单独替代 Developer ID 与公证。

### 验证证据

打包阶段生成内部的 `omnishare-macos-<arch>-packaging.json`（schema 1），记录模式、团队、最终 ZIP/DMG 摘要，以及两次公证的 submission ID、状态和票据结果。独立原生运行器重新验证这些产物，生成 `omnishare-macos-<arch>-verification.json`（schema 2）；只有成功的验证报告才进入正式发布资产。

schema 2 报告将版本、架构、`release`/`rehearsal` 模式、最终两份文件的名称/字节数/SHA-256、打包证据摘要、33 项检查和两条实际安装运行路径绑定在一起：

| 顶层检查组 | 数量 | 范围 |
| --- | ---: | --- |
| ZIP 布局、签名与篡改负例 | 4 | 应用布局、完整签名、签名经 ZIP 保存、篡改资源被拒绝。 |
| ZIP 基础运行 | 4 | LaunchServices、健康版本、内嵌页面、正常退出。 |
| DMG 完整性与安装复制 | 5 | 布局、只读挂载、DMG 签名、内含应用与 ZIP 一致、复制后推出映像仍完整。 |
| DMG 安装后基础运行 | 4 | LaunchServices、健康版本、内嵌页面、正常退出。 |
| ZIP 功能与原生生命周期 | 5 | 核心功能、持久化重启、原生退出、运行中重新打开、端口冲突。 |
| DMG 功能与原生生命周期 | 5 | 对从映像安装的应用独立执行相同五项。 |
| 正式分发 | 6 | Developer ID、Hardened Runtime、安全时间戳、App 票据、DMG 票据、隔离状态下的系统策略评估。 |

前六组共 27 项为通用门槛。最后 6 项在正式模式必须全部为 `true`；PR 演练采用 `ad-hoc`，公证状态为 `NotSubmitted`，这些分发检查为 `false`，不能进入正式发布。`production_ready: true` 只表示自动化发布门槛通过，不表示真实 Safari 首装已执行。

两个安装路径的安全功能报告分别保存在 `runtime.zip.features` 和 `runtime.dmg.features`，每组都包含：

| 阶段 | 逐项成功的检查 |
| --- | --- |
| `exercise`，7 项 | `health_and_embedded_ui`、`first_run_authentication`、`notes_and_trash`、`documents_and_conflicts`、`file_upload_download_and_range`、`share_access_and_revocation`、`encrypted_backup_restore`。 |
| `verify`，5 项 | `health_and_embedded_ui`、`configuration_persistence`、`note_and_counter_persistence`、`file_persistence`、`document_persistence`。 |

这些报告必须匹配本次版本、阶段并逐项成功。访问密钥、笔记正文等私有状态只用于本次验证，不上传到公开证据中。测试使用含中文、空格、`#` 和 `%` 的独立数据目录，检查退出与重启后仍使用原有数据库。

原生生命周期验收核对启动器与其拥有的后端身份：运行中调用 LaunchServices 重新打开时，两者不能被重复创建；退出通过 `NSRunningApplication.terminate` 请求 AppKit 的正常退出流程，后端必须经私有 stdin EOF 收尾。验证必须看到退出记录中的 `method: "stdin-eof"`、`normal: true`、退出码 0 和 `fallback_used: false`，同时确认进程已经结束。超时信号终止或失败清理不能使此门槛通过。随后占用上次保存的端口，普通启动必须选择并保存可用端口，保留业务数据并通过 5 项重启功能检查。`--port` 或 `OMNISHARE_PORT` 显式指定的端口仍保持严格语义，不能静默改端口。

原生应用本身提供应用菜单和状态栏菜单的“打开 OmniShare”/“退出 OmniShare”，前台快捷键为 `⌘O`/`⌘Q`。上述自动化原生退出调用与菜单使用同一 AppKit 退出生命周期；它没有宣称已通过鼠标操作菜单或真实 Safari 首次下载。本轮新增验收结果以之后的对应原生 CI 运行记录为准。

## 5. 首次下载验收

自动化签名、`syspolicy_check` 或 `spctl` 检查与真实首次下载验收分别记录。PR 的 ad-hoc 运行测试、CI 下载的构建 artifact，以及人为设置隔离属性的测试，都不能被记为真实 Safari 首装成功。

正式声称完成首次下载验证前，按照 [Apple 的 Testing a Notarised Product](https://developer.apple.com/forums/thread/130560)执行：

1. 使用从未接触过该产品的 Mac，或恢复到干净快照的 macOS 虚拟机。按支持范围分别覆盖 Intel 和 Apple 芯片；重复测试需恢复快照，避免 Gatekeeper 缓存影响结果。
2. 用 Safari 从正式发布地址下载最终 DMG 或 ZIP，核对公开 SHA-256，保留系统的下载隔离状态。
3. 走真实用户路径：打开 DMG，拖入“应用程序”，推出映像，再从“应用程序”启动；ZIP 则由 Finder 解压并复制完整应用。
4. 确认没有“已损坏”“无法验证开发者”等阻断，无需通过“仍要打开”建立例外。正常的“从互联网下载，是否打开”确认属于系统首次启动提示，可以出现。
5. 核对浏览器界面、初始化与核心功能；关闭标签页后使用“打开 OmniShare”，再通过菜单或 `⌘Q` 正常退出并重新打开，检查数据仍在。另在干净快照上断开网络后重复安装与打开，验证随包票据在离线场景可用。
6. 记录实际版本、架构、系统版本、下载 URL、文件 SHA-256、验证日期及结果。原生 CI 证据中的 `first_download_verified` 始终为 `false`；真实 Safari 验收另行记录，不能将 CI 公证、模拟隔离策略检查或运行检查改称真实首装。

公证完成仍不能承诺任意设备无条件放行。组织管理策略、受支持系统版本、证书状态及用户的首次打开选择会影响结果。发布承诺以明确的系统支持范围、最终包证据和真实验收结果为准。

## 6. 发布下一版本前

确认凭据、正式签名与公证门槛、两架构验证及首装验收安排后，再统一更新 `VERSION`、应用元数据、`release/desktop-release.json` 和对应的新发布说明。正式发布清单的 `macos` 字段必须明确为：

```json
{
  "signing_mode": "developer-id",
  "notarized": true,
  "server_zip_signed": true,
  "dmg_published": true,
  "minimum_system_version": "12.0",
  "tauri_published": false
}
```

这些字段声明该发布必须满足的门槛，不能代替实际验证结果。历史 `0.1.1` 清单保留其真实 ad-hoc 状态；它可用于 PR 的内部演练，但会被新的正式发布检查拒绝。

通过 PR 验证并合并到 `main` 后，新的发布清单变更触发正式构建。`0.1.1` 的标签、附件和原发布说明保持不变；用户下载指引仅在新版本实际发布成功后切换。
