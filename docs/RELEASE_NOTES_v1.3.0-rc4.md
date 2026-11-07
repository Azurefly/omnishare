# OmniShare v1.3.0-rc4 发布说明

发布日期：2026-11-07。版本定位：**Release Candidate / 预发布**。

RC4 在 RC3 的统一存储、安全备份和桌面可靠性基线上，更新工作区界面、剪贴板发送和传输队列。功能与交互参考 Oink 后独立实现，保持 OmniShare 的自托管内容共享定位。

## 使用体验

- **清晰的主工作区**：桌面端在左侧集中导航、设备与存储，右侧显示当前模块。卡片、图标、间距、焦点和明暗主题使用统一视觉规则；窄屏保留连接状态和可滚动导航。
- **分组设置**：外观、网络与访问、存储与保留、手动设备分别组织，说明文字靠近对应操作。纠正“哈希链保证不可篡改”和“仅支持未加密 ZIP”等旧文案。
- **剪贴板先预览再发送**：用户点击后读取纯文本或 PNG，预览目标节点、内容和保留时长后确认。文本保存为随手记，图片进入文件队列，默认保留 24 小时；取消会清空预览。
- **可管理的传输队列**：排队项立即可见，支持取消、手动重试、清理结束记录；上传到 100% 后显示服务端处理状态，收到成功响应才标记完成。失败原因保留在任务行内。
- **设备分组**：展示当前节点、在线节点和离线节点。发现与在线状态不代表授权，打开其他节点仍需要该节点独立验证。

## 安全与正确性

- 不后台监控剪贴板，不将预览写入长期存储，不把当前管理密钥传给发现的节点。
- 剪贴板文本按纯文本处理；HTML、SVG 和其他富内容不作为活动页面导入。文本预览上限 256 KB，序列化后的请求还受 1 MB 上限约束。
- 图片仅接受 PNG，最多 16 MB，同时遵守节点上传上限；预览前检查 PNG 头、宽高和总像素，通过浏览器实际解码后才开放确认，关闭后释放本地 Blob URL。
- 修复上传失败后手动重试绕过 3 个并发上限的问题；重试重新排队，取消与迟到回调只释放一次并发名额。
- 不自动重试创建内容的请求。连接中断后，服务端可能已经保存文件或文本，应先检查列表再重试；取消上传不能撤回已经保存的数据。
- 设备链接仅允许纯 HTTP/HTTPS origin，拒绝凭据、查询参数、片段及路径，打开时不携带 Referer 或当前访问密钥。
- 修复上传区域内选择保留时长时误弹文件选择器的问题，增加键盘激活。

此版本未引入系统键鼠控制、输入监听、虚拟音频驱动、管理员服务或设备组自动授权。Oink 的代码、品牌和素材没有被复制，也没有新增运行时依赖。参考来源与取舍见 [吸收记录](https://github.com/Azurefly/omnishare/blob/main/docs/OINK_ADOPTION.md)。

## 下载选择

附件见 [v1.3.0-rc4 Release](https://github.com/Azurefly/omnishare/releases/tag/v1.3.0-rc4)。

| 使用方式 | 附件 |
| --- | --- |
| Windows x64 桌面 | `OmniShare_1.3.0-rc4_x64-setup.exe` 或 `OmniShare_1.3.0-rc4_x64_en-US.msi` |
| Linux x64 桌面 | `OmniShare_1.3.0-rc4_amd64.AppImage`、`OmniShare_1.3.0-rc4_amd64.deb`、`OmniShare-1.3.0-0.rc4.x86_64.rpm` |
| Windows 后端 + Web | `OmniShare-v1.3.0-rc4-windows-amd64.zip` / `OmniShare-v1.3.0-rc4-windows-arm64.zip` |
| Linux 后端 + Web | `OmniShare-v1.3.0-rc4-linux-amd64.tar.gz` / `OmniShare-v1.3.0-rc4-linux-arm64.tar.gz`，或 `omnishare-server_1.3.0-rc4_{amd64,arm64}.deb` |
| macOS 后端 + Web | `OmniShare-v1.3.0-rc4-macos-amd64.zip` / `OmniShare-v1.3.0-rc4-macos-arm64.zip` |

macOS ZIP 是未签名的 Go 后端启动包装，不是 Tauri Universal 桌面安装器。macOS Tauri 签名、公证与真机 Gatekeeper 验收仍待完成；Windows 安装包尚未提供商业代码签名。

### 发布附件命名修复

RC3 发布时，GitHub 将服务器 DEB 的 `~rc3` 文件名改为 `.rc3`，导致原 SHA256 清单中的这两个名字与公开附件不一致。包字节和哈希没有变化；仓库提供 [RC3 更正清单](https://github.com/Azurefly/omnishare/blob/main/release/checksums/v1.3.0-rc3/SHA256SUMS.txt)，旧 tag 和二进制保持原样。

RC4 首次发布时，服务器和桌面 AMD64 DEB 仅通过 `omnishare` / `OmniShare` 的大小写区分。上传动作失败后，22 个附件中缺少服务器 AMD64 DEB；此前的构建、运行验证及聚合验包均成功。现在服务器 DEB 使用独立的 `omnishare-server_1.3.0-rc4_*.deb` 名称，内部 `Package: omnishare` 与 `Version: 1.3.0~rc4` 保持不变。

本次发布修复绑定原 [构建运行 42350497115](https://github.com/Azurefly/omnishare/actions/runs/42350497115) 的已验证产物：补齐缺失包、重命名已上传的 ARM64 服务器包，并更正 README、本文和 SHA256 清单。全部二进制字节保持原样，`v1.3.0-rc4` 标签继续指向 [`c96c102`](https://github.com/Azurefly/omnishare/commit/c96c102e7eb0c327b2c8a8248416be12f614d4d3)。原标签下的说明及包内嵌入的 README 因此仍可能使用首次发布名称；下载和校验应使用当前 Release 的附件与清单。

[修复工作流](https://github.com/Azurefly/omnishare/actions/workflows/repair-rc4-release.yml) 先只读校验来源、压缩包 SHA-256、现有附件和具体修改计划，再在 main 上执行白名单内的修复；遇到未知附件或内容变化会停止。修复后逐个核对全部 22 个公开附件的名称、大小和 SHA-256。常规发布仍保留原有标签目标检查，并增加上传前的大小写重名拒绝。

修复支持已完成写入后的重复执行，以及文本附件删除与重新上传之间的中断恢复。若 GitHub 留下 `starter` 状态、零字节或没有摘要的未完成附件，流程会停止并要求单独核对，不会自动删除二进制占位物或覆盖未知内容。

## 升级与兼容性

升级前备份现有数据并退出旧进程，安装新版本时继续使用原数据目录。RC4 没有改变 SQLite schema 或备份格式：继续导出 v3 `.osb`，读取 v3、v2 和 ZIP；v3 仍需要 RC3 或更新程序恢复。

Windows 便携脚本默认数据目录为 `%APPDATA%\OmniShare`；Tauri 桌面及直接运行后端默认为 `%USERPROFILE%\.omnishare`。Linux Tauri 默认 `~/.omnishare`，tar.gz 安装脚本使用 XDG 目录。切换启动方式前应核对原路径；Tauri 可用 `OMNISHARE_DATA_DIR`，后端可用 `--data-dir` 指定已有目录。

Service Worker 使用新的版本化静态缓存。升级后仍显示旧页面时关闭其他 OmniShare 标签页并重新加载；剪贴板预览和传输队列只属于当前页面，不会在刷新后恢复。

## 验证与限制

- 前端语法、静态契约，以及会话恢复、备份、剪贴板同意和上传调度行为测试。
- 固定版本 Playwright 的真实 Chromium 测试：桌面明暗主题、全部导航、设置、剪贴板确认、上传队列、320/390/768/1024 像素布局及外部请求检查，并保存截图和结果。
- Go 测试、Race Detector、`go vet`、Staticcheck、覆盖率门槛与独立 HTTP 进程集成验证。
- 六目标服务器包、Windows/Linux 桌面安装包，Windows 安装布局与 watchdog 运行验证，发布前后校验。

Chromium 测试使用合成 API 数据验证前端；后端鉴权与持久化由独立 Go/HTTP 测试覆盖。自动化测试不等于 Windows 托盘、macOS 权限、Safari 和所有触屏设备的真机验收。

当前仍为单管理员访问密钥模型；接收方身份绑定的点对点配对、逐设备权限、系统级 KVM、音频共享和上传分片续传尚未实现。清除访问密钥后的初始化令牌续发、清理与恢复的协调、审计锚点备份恢复等既有专项仍见 [整合记录](https://github.com/Azurefly/omnishare/blob/main/docs/INTEGRATION_2026-11-07.md)。
