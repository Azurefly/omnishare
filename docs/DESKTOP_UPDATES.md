# 桌面应用检查与升级

## 用户使用

v1.0.0 提供源码归档。从源码构建的桌面应用保留检查更新入口，应用内安装需要目标版本提供对应平台的安装包。

安装包含本功能的版本后，从“设置 → 桌面软件更新”、顶部状态栏版本徽标或桌面菜单/托盘的“检查更新…”进入（浏览器与便携运行模式亦在“设置 → 桌面软件更新”提供版本检查与对应平台直接下载入口）。
更新窗口打开时检查 GitHub Releases；稳定版默认只查稳定版，候选版默认允许候选版，可自行切换通道。
按真实语义化版本比较，`rc.10` 比 `rc.9` 新，不自动降级。网络失败不能显示成“已是最新版”。

关闭更新窗口会取消仍在进行的检查/下载并释放已下载缓存；重新打开可再次检查。已经确认并开始的安装不会因窗口关闭而中断。

“下载并验证更新”不停止应用、不安装文件；在配置有签名公钥且官方发布包含 `latest.json` 时优先走签名验证通道，否则自动回退核对官方 Release 附件 `SHA256SUMS.txt` 的文件大小与 SHA-256 哈希，且严格校验下载地址属于 `https://github.com/Azurefly/omnishare/releases/download/<tag>/`。只有下载完整、校验通过，才出现安装确认。
勾选已保存内容后，点击“退出并安装更新”。只更新当前电脑，保留数据目录、端口、访问密钥配置和设备绑定；其他绑定设备不会被关闭或升级（Windows 支持 NSIS/MSI 自动替换与失败回滚，macOS 支持挂载 DMG 并经 `codesign --verify --deep --strict` 校验后覆盖 `.app`，Linux 支持 AppImage 原子替换与 DEB 安装）。
安装前阻止看门狗拉起后端，正常关闭自有后端并等待数据刷盘。遇到超时会取消升级而不是强杀。
独立启动的外部后端不会被杀掉；需要用户先退出它并重新启动桌面应用。
安装器启动前失败，或本机安装调用返回错误/异常时，会尝试恢复原服务的运行意图；恢复失败会明确报错。
Windows 成功交给系统安装器后，后续结果由安装器处理，不能承诺所有安装失败都会自动回滚并重新打开旧版。
成功后 Windows 由原安装类型的安装器重启，macOS/Linux 重新启动桌面应用。升级前仍应保存内容并备份重要数据。
系统权限提升、文件锁、安装失败和 Apple Gatekeeper 提示仍遵循系统规则，不关闭安全保护。

Windows 使用 EXE/MSI 桌面版；服务器/浏览器 portable 包在设置面板提供新版本匹配下载链接。

## 发布者首次启用签名通道（可选/增强）

当仓库暂未配置 `desktop/src-tauri/updater-public-key.txt` 时，更新器使用官方 Release 附件 `SHA256SUMS.txt` 进行多平台安装包大小与 SHA-256 校验安装；若发布者希望额外启用 Tauri updater 非对称密钥签名验证，可在自己可信电脑上执行一次下列配置（Tauri CLI 已锁定为 2.12.4）：

```bash
npm --prefix desktop ci
gh auth login --hostname github.com
python3 scripts/configure-desktop-updates.py --generate --upload-secrets
```

Windows 使用 `python` 替代 `python3`。这是**维护者的一次性发布配置**，普通使用者不需要执行这些命令。
脚本会交互式生成持久密钥，对随机测试文件试签名并核对版本与公钥标识，将公钥写入
`desktop/src-tauri/updater-public-key.txt`；私钥和密码仅通过已登录的 GitHub CLI 标准输入加密上传到固定仓库的
`TAURI_SIGNING_PRIVATE_KEY`、`TAURI_SIGNING_PRIVATE_KEY_PASSWORD` Secrets，不进入命令参数、仓库或输出。
默认私钥存放在仓库外的 `~/.config/omnishare/updater.key`，必须自行备份原文件和密码。

已有密钥时不要加 `--generate`，用 `--key-file` 指向原私钥；没有 `--upload-secrets` 时只准备本地公钥，不改变远端。
脚本拒绝在 CI/非交互环境运行，拒绝覆盖已有密钥、替换已固定的不同公钥，或覆盖已经存在的更新 Secrets。
发生部分上传失败时不会删除既有 Secrets，需维护者检查仓库设置，不要重新生成一套密钥。

完成后仅提交**公钥文件**，合并并构建一次新桌面安装包，供客户端使用内置签名更新。
私钥不要提交到仓库、Issue、日志或聊天，不要每次发布重新生成；旧客户端必须继续信任同一公钥。
这与 Apple Developer ID 及公证是两套独立配置：更新签名保护下载包来源，不能替代 Apple 公证。

## 发布流程

当目标 Release 已提供最终桌面包时，手动运行 `.github/workflows/signed-desktop-updates.yml` 并选择该 tag。工作流会核对 `build-manifest.json` 的源码、版本及摘要，并为该批安装包生成更新索引。
普通 CI 不需要签名私钥；签名只在官方仓库的发布工作流执行。缺公钥/私钥时严格失败，不发布未验证的更新索引。

- Windows 分别提供 `windows-x86_64-nsis` 与 `windows-x86_64-msi`，不把已安装 MSI 偷换成 EXE 安装类型。
- Ubuntu DEB 与 Linux AppImage 分别匹配；DEB 可需要系统授权，不执行用户提供的 sudo/shell 命令。
- macOS 使用最终应用包的 `.app.tar.gz` 更新档，Universal 包匹配两种 CPU。
- `prepare-desktop-updates.py` 使用 `tauri signer sign --app-version` 对最终包签名，签名绑定版本。
- `publish-desktop-updates.py` 比对所有包的源码提交、版本、公钥摘要、大小和 SHA-256；先上传包，最后才上传同次 Release 的 `latest.json`。
- 已存在的附件只有摘要完全一致才允许幂等重跑，不覆盖原包、tag 或旧签名。

客户端只访问官方 GitHub HTTPS 地址，跨域重定向限定官方附件主机，禁止跳过 TLS 检查。
更新命令仅允许 bundled 原生更新窗口调用；局域网网页、对端服务和任意远程 URL 不能触发本机安装。
下载包最大 512 MiB，失败/截断/版本不一致/错误签名均不可进入安装步骤。

## 验证边界

前端状态机测试覆盖检查不安装、签名失败、下载后确认、重复点击和失败清空旧状态；Rust 测试覆盖版本通道、来源 URL、窗口权限与停止/恢复守护逻辑。
另有真实原生升级回归：`.github/workflows/direct-upgrade.yml` 构建独立测试身份的完整 OmniShare 旧版与新版，
实际安装旧版，调用与生产相同的本机更新命令完成下载、验签、确认、安装和重启，再读取原来的 SQLite 随手记、协同文档、
API 上传文件及索引、端口和设备绑定。永久矩阵包含 Windows MSI/NSIS、Ubuntu 22.04 AppImage/DEB、Ubuntu 24.04 DEB 和 macOS app。

这些测试使用临时测试密钥、隔离 HTTPS 服务和原生命令驱动，不是公开 GitHub 正式更新通道、鼠标点击端到端、
所有系统授权提示或 Apple Gatekeeper 验收。发布时仍须对最终签名的正式包做实际旧版到新版检查，不能直接套用测试包摘要。
