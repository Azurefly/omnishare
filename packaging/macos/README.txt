OmniShare v__VERSION__ macOS

需要 macOS 12 Monterey 或更新版本。Apple 芯片选择 arm64，Intel 选择 amd64。
DMG：打开映像，将 OmniShare.app 拖入 Applications，推出映像后从应用程序启动。
ZIP：用 Finder 解压，将完整 OmniShare.app 复制到 Applications 后启动。
本包运行 Go 后端并打开默认浏览器，不是 Tauri 桌面安装器。
数据默认位于 ~/.omnishare，也可以通过 OMNISHARE_DATA_DIR 指定目录。
更新前退出旧后端并备份数据；替换整个应用，不要合并新旧应用内部文件。

__SIGNING_NOTICE__

请从对应版本的官方 GitHub Release 下载，并用 SHA256SUMS.txt 核对同名文件。
首次打开可能出现 macOS 正常的“从互联网下载，是否打开”确认；正式签名包
的验收目标是不需要“仍要打开”安全例外，组织管理策略可能另有限制。
如果仍显示“已损坏”或身份、公证被拒绝，请保留准确提示、下载文件摘要与
macOS/芯片信息，按仓库 docs/MACOS.md 收集只读诊断。
自动化签名及运行检查不等于 Safari 首次下载验收；实际状态以对应版本
Release 的说明和 omnishare-macos-<arch>-verification.json 为准。

默认仅监听 127.0.0.1；开启局域网前必须设置至少 16 字节访问密钥。
