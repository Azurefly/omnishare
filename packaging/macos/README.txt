OmniShare v__VERSION__ macOS

需要 macOS 12 Monterey 或更新版本。Apple 芯片选择 arm64，Intel 选择 amd64。
将 OmniShare.app 放入 Applications 后启动；本包运行后端并打开默认浏览器。
数据默认位于 ~/.omnishare，也可以通过 OMNISHARE_DATA_DIR 指定目录。

完整应用已做 ad-hoc 签名，用于校验可执行文件、Info.plist 和资源完整性。
ad-hoc 签名不提供 Apple Developer ID 身份，也不等于 Apple 公证。
下载后的首次打开仍可能被 Gatekeeper 阻止。请先核对同版本 Release 的
SHA256SUMS.txt，再按 macOS“系统设置 > 隐私与安全性”的提示处理首次信任。
自动化验证覆盖实际 ZIP 解压后的完整性和本机启动，没有宣称通过浏览器
下载隔离状态下的首次 Gatekeeper 信任检查。详细说明见同版本 Release。

默认仅监听 127.0.0.1；开启局域网前必须设置至少 16 字节访问密钥。
