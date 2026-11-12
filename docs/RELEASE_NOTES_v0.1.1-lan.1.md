# OmniShare v0.1.1-lan.1 · 局域网预览版

> **这是预发布测试版，不是已经完成三平台验收的正式版。** 本次直接发布已构建的 `4e9e809` 安装包，不修改安装包内部字节；程序、安装器内部版本仍为 `0.1.1`。标签用于区分这批局域网改动与旧 `v0.1.1`，不会覆盖旧版附件或将本版标为 Latest。

## 这次更新

- **访问密钥保护改为独立开关。** 新安装默认关闭；开启后自行设置非空密钥，不限制最小长度或字符组合，支持中文、数字、符号、表情和空格，保留首尾空格。关闭后立即停止密钥校验，重新开启可以沿用已保存的密钥。
- **默认启用局域网，无需初始化 Token。** 新安装监听 `0.0.0.0`，本机和同网可达设备可以直接使用；取消 init Token 的生成、校验和桌面导航注入。
- **自动发现已部署设备。** 在活动 IPv4 组播网卡上广播与发现 OmniShare；支持多网卡、网络变化后重试、定时刷新和 HTTP/HTTPS 地址。发现不需要配对，访问受保护的其他节点时遵循对方的密钥设置，不转发当前节点的密钥。
- 包含 Ubuntu 22.04 构建基线、特殊字符 SQLite 数据目录和桌面后端正常退出等修复；这些源码改动不代表所有平台安装场景已通过验收。

## 下载安装

[本版附件列表](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1-lan.1)

| 系统 | 选择的附件 | 使用方式 |
| --- | --- | --- |
| Windows x64 | `OmniShare-4e9e809-windows-x64-test-setup.exe` 或 `OmniShare-4e9e809-windows-x64-test.msi` | 原生桌面安装包，两种任选其一 |
| Ubuntu x64 | `OmniShare-4e9e809-ubuntu-amd64-test.deb` | 使用 APT 安装，基于 Ubuntu 22.04 构建 |
| Linux x64 | `OmniShare-4e9e809-linux-amd64-test.AppImage` | AppImage 格式，完整安装验收未完成 |
| macOS 12+，Intel / Apple 芯片 | `OmniShare-4e9e809-macos-universal-test.dmg` | 双架构原生桌面测试包，**未完成 Developer ID 签名和 Apple 公证，仍可能无法直接打开** |
| Windows x64 / ARM64 | `OmniShare-4e9e809-windows-{amd64,arm64}-portable.zip` | 后台服务＋浏览器，不是原生桌面窗口；完整解压后运行 `start-omnishare.cmd` |
| Linux x64 / ARM64 | `OmniShare-4e9e809-linux-{amd64,arm64}-portable.tar.gz` | 后台服务＋浏览器，解压后运行 `omnishare` |

Ubuntu DEB：

```bash
sudo apt install ./OmniShare-4e9e809-ubuntu-amd64-test.deb
```

**升级前先备份并完全退出应用及后端。** 预览包与旧版内部版本号相同，安装器可能识别为同版本维护，而不是升级；必要时卸载旧应用但保留数据，再安装。不要覆盖正在运行的程序，也不要删除数据目录。旧 `1.x` 安装还需考虑版本号重置。

**新安装采用新默认值，已有安装保留原网络和密钥设置。** 已有用户可在“设置 → 访问与网络”中关闭密钥保护、启用局域网并保存；密钥开关立即生效，监听地址或局域网开关变化后重启服务。

## 验证状态与已知问题

| 范围 | 已确认的结果与限制 |
| --- | --- |
| 源码与基础功能 | 源码 CI 通过；前端 41 项行为测试、Go 测试及真实 HTTP 检查通过 |
| 已下载 Linux x64 便携包 | 隔离 Linux 容器中的 HTTP、免密局域网、无 init Token、正常退出和双进程组播发现检查通过；不是三台实体设备互联测试 |
| Windows 桌面 | 本批 EXE/MSI 构建、微软运行时输入签名和文件摘要验证通过；本次导出未完成端到端安装验收。同源码较早构建在 WebView 私有调试端口连接处超时，不能记为整体通过 |
| Ubuntu 桌面 | DEB/AppImage 构建成功，依赖和 glibc 静态检查完成；Ubuntu 22.04/24.04 完整安装运行验收未完成 |
| macOS Universal 桌面 | 构建及双架构检查通过，完整运行验收未完成；无 Apple 公证，不能承诺首次打开放行。另一路轻量启动包的端口冲突重启检查失败，不作为本版附件 |

不会通过关闭 Gatekeeper、清除全局安全配置或将失败检查改成成功来宣称正式可用。`build-manifest.json` 记录准确来源和验证边界；`SHA256SUMS.txt` 覆盖其余全部附件，供下载后核对。

## 局域网使用边界

关闭密钥后，能连接到服务的设备可以读取、修改和删除内容，也可以修改配置。`0.0.0.0` 是全部 IPv4 接口，并不自动识别可信网段；不要将免密服务映射到公网。CORS、Host、路径和备份完整性保护仍然保留。

自动发现依赖 IPv4 组播 `239.255.42.99:48281/UDP`。防火墙、路由器客户端隔离、不同 VLAN 或系统局域网权限可能阻断发现；程序不会自动关闭这些保护，手动添加节点地址仍可使用。

## 构建来源

应用源码：[`4e9e809bd5a70e4bbaa4c6bc71adfab05cc56dde`](https://github.com/Azurefly/omnishare/commit/4e9e809bd5a70e4bbaa4c6bc71adfab05cc56dde)。发布标签包含发布文档与流程，应用源码和二进制不变；功能代码仍在 PR #33，未合并 `main`。

- [源码 CI](https://github.com/Azurefly/omnishare/actions/runs/43173998528)
- [桌面构建与原验收](https://github.com/Azurefly/omnishare/actions/runs/43173998632)
- [服务端打包及 Mac 轻量包原验收](https://github.com/Azurefly/omnishare/actions/runs/43173998801)
- [本批 Windows 安装包构建](https://github.com/Azurefly/omnishare/actions/runs/43175171262)
