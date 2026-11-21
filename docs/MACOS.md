# macOS 构建与诊断

v1.0.0 提供源码归档。桌面应用的最低系统要求为 macOS 12 Monterey。

在项目根目录按[桌面构建说明](../desktop/README.md#build)构建。默认数据目录位于 `~/.omnishare`。更新前请退出应用并备份实际使用的数据目录。

签名、公证和安装包发布步骤见 [macOS 正式签名与公证](MACOS_SIGNING.md)。

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

