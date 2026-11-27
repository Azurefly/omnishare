# macOS 构建与诊断

[下载页](../README.md#下载)提供 Intel 和 Apple Silicon 共用的 DMG。打开后将 OmniShare 拖入「应用程序」。候选版采用临时签名，尚未经过 Apple 公证，首次下载时 Gatekeeper 可能拦截启动。桌面应用的最低系统要求为 macOS 12 Monterey。

在项目根目录按[桌面构建说明](../desktop/README.md#build)构建。默认数据目录位于 `~/.omnishare`。更新前请退出应用并备份实际使用的数据目录。

签名、公证和安装包发布步骤见 [macOS 正式签名与公证](MACOS_SIGNING.md)。

## 能发现设备，但绑定超时

在对方的「设备与网络 → 当前设备 → 连接详情」中复制双方都能访问的连接地址。使用 Tailscale 时选择 Tailscale 地址，使用同一局域网时选择对应的局域网地址。在 Mac 的「添加设备并合并工作区」中粘贴完整地址，包括端口。如果对方启用了访问密钥保护，在绑定窗口填写对方的密钥。

如果对方显示「仅本机访问」，在对方的「设置 → 访问与网络」检查允许访问的网络和监听地址。更改网络设置后退出并重新打开对方的应用，再从 Mac 刷新设备列表。

两端都更新到 rc.6 或更高版本后，可以容忍一分钟以内的系统时差。如果绑定窗口提示时差过大，在两台设备上启用自动设置日期与时间，然后重新绑定。两地使用不同时区也能绑定。

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

- `codesign -d` 显示签名身份。正式分发包应显示与发布团队一致的 Developer ID Application 身份和时间戳。
- `codesign --verify` 检查代码及资源完整性。出现签名无效、缺失资源或资源被更改时，保留原输出，不要重新签名掩盖问题。
- `spctl` 和 `syspolicy_check` 检查系统分发策略。ad-hoc 包可能完整性正确但策略仍拒绝；公证成功也不能代替实际安装验证。

报告问题时附上下载文件名、SHA-256、macOS 版本、芯片信息、准确警告与命令输出。遮盖个人路径，不要提供访问密钥、初始化令牌或业务数据。Apple 的[公证产品测试说明](https://developer.apple.com/forums/thread/130560)解释了命令行评估与真实首次下载测试的区别。

