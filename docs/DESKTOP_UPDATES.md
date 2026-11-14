# 桌面应用检查与升级

## 用户使用

安装包含本功能的版本后，从“设置 → 桌面软件更新”或桌面菜单/托盘的“检查更新…”进入。
更新窗口打开时检查 GitHub Releases；稳定版默认只查稳定版，候选版默认允许候选版，可自行切换通道。
按真实语义化版本比较，`rc.10` 比 `rc.9` 新，不自动降级。网络失败不能显示成“已是最新版”。

“下载并验证”不停止应用、不安装文件；只有下载完整、签名及签名内版本均通过，才出现安装确认。
勾选已保存内容后，点击“退出并安装更新”。只更新当前电脑，保留数据目录、端口、访问密钥配置和设备绑定；其他绑定设备不会被关闭或升级。
安装前阻止看门狗拉起后端，正常关闭自有后端并等待数据刷盘。遇到超时会取消升级而不是强杀。
独立启动的外部后端不会被杀掉；需要用户先退出它并重新启动桌面应用。
失败会恢复原服务的运行意图。成功后 Windows 由原安装类型的安装器重启，macOS/Linux 重新启动桌面应用。
系统权限提升、文件锁、安装失败和 Apple Gatekeeper 提示仍遵循系统规则，不关闭安全保护。

**旧版本升级入口限制：** RC2 及更早的程序没有内置该更新器，必须先手动覆盖安装一次带本功能且内置可信公钥的新版本，保留数据目录。不能通过远端修改旧二进制，凭空让旧版出现“检查更新”。
Windows 使用 EXE/MSI 桌面版；服务器/浏览器 portable 包不是桌面更新目标。

## 发布者首次启用（一次性）

当前源码的 `desktop/src-tauri/updater-public-key.txt` 留空，避免嵌入临时或所有人都知道私钥的测试密钥。
空公钥允许检查版本，但明确禁止自动安装；缺少 `latest.json` 的历史 Release 也不会冒充可直接升级。

在发布者自己的可信电脑执行（Tauri CLI 已锁定为 2.12.4）：

```bash
npm --prefix desktop ci
npm --prefix desktop exec -- tauri signer generate -w "$HOME/.omnishare-updater.key"
```

将生成的 `.pub` 文件内容复制进 `desktop/src-tauri/updater-public-key.txt` 并提交。
把私钥文件内容保存到仓库 Actions Secret `TAURI_SIGNING_PRIVATE_KEY`；导出密码保存到 `TAURI_SIGNING_PRIVATE_KEY_PASSWORD`。
私钥不要提交到仓库、Issue、日志或聊天。备份原密钥，不要每次发布重新生成；旧客户端必须继续信任同一公钥。
这与 Apple Developer ID 及公证是两套独立配置：更新签名保护下载包来源，不能替代 Apple 公证。

## 发布流程

`.github/workflows/signed-desktop-updates.yml` 在新 Release 发布后读取其精确 tag 与已发布桌面包，核对 `build-manifest.json` 的源码/版本/摘要；不另行重编译不同的安装器。GitHub Actions 自身创建的 Release 可能不会触发其他工作流，此时在该工作流手动选择对应 tag 执行。
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
这些不是跨平台真实安装升级验收。发布时仍须用旧安装包真实执行升级，核对本机数据与绑定未丢失、安装类型及重启版本正确。
旧 Issue #36 的原生 UI 与 Apple 签名限制不能因新增更新器而自动标记解决。
