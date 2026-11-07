# Oink 功能与界面吸收记录

日期：2026-11-07。参考仓库：[oinkvm/oink](https://github.com/oinkvm/oink)，固定审查版本：[`546f65b2135e7a1491d2d743f0a1e072a74df36f`](https://github.com/oinkvm/oink/tree/546f65b2135e7a1491d2d743f0a1e072a74df36f)。本记录对应 OmniShare `1.3.0-rc4`。

## 产品边界

Oink 是具有键鼠控制、剪贴板、拖拽传输和音频能力的点对点软件 KVM。OmniShare 是自托管内容工作区，现有架构围绕一个节点的随手记、文件、协作文档和受控分享。本轮吸收与这些场景直接相关的交互能力，独立实现；不把“发现节点”显示为“已配对”，不以全局管理员密钥模拟设备权限。[Oink README](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/README.md)

## 已实现的对应关系

| 参考方向 | RC4 的实现 | 权限与行为边界 |
| --- | --- | --- |
| 设备列表与状态组织 | 当前节点、在线、离线分组；名称、连接状态、地址和次级说明分层 | 状态来自现有 API；没有新增“已信任”或“已配对”推断 |
| 剪贴板跨设备使用入口 | 点击读取、预览、确认后保存纯文本或上传 PNG；显示当前目标节点与保留时长 | 只向当前节点发送；不轮询、不后台同步，不读取其他格式 |
| 任务状态可见 | 排队项立即可见，显示上传、服务端处理、完成、失败和取消；允许清理记录 | 仅响应成功后标记完成；取消无法召回已到达的数据 |
| 取消与重试 | 取消排队任务、终止进行中的连接；失败或取消后手动回队列重试 | 所有尝试共享 3 个并发名额，不自动重试内容创建 |
| 单一主工作区 | 左侧导航与设备区，右侧当前模块；窄屏适配 | 保留现有 API 和功能入口，设备发现不触发内容发送 |
| 章节化设置与状态反馈 | 设置按用途分节；统一焦点、图标、状态文字与浅色对比度 | 重要后果直接呈现，避免只藏在 Tooltip 或颜色中 |

设备分组和状态层级参考其 [DeviceList.tsx](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/apps/oink/src/devices/DeviceList.tsx)。没有复制组件代码、样式、品牌、图标或宣传素材。

## 安全实现

### 剪贴板

读取只从用户点击开始。支持 `navigator.clipboard.read()` 的环境优先选择 PNG，否则选择纯文本；仅支持 `readText()` 的环境使用文本。权限拒绝不会触发另一条隐式读取路径。仅含 HTML、SVG 等格式时拒绝导入，预览使用文本值或本地 Blob URL。

纯文本最多 256 KB，并在发送前校验序列化 JSON 不超过后端 1 MB 请求上限。PNG 最多 16 MB，进一步受节点文件上限约束；预览前检查签名与 IHDR、单边 8192 像素和总计 16×1024×1024 像素上限，限制图片解码资源。图片还必须通过浏览器实际解码后才开放确认；无法解码时清理预览且不上传。关闭预览会清空文本、文件引用与 Blob URL。

确认框显示目标节点的真实 origin。保留时长默认 24 小时，可选择 1 小时或 7 天。确认采用防重复提交状态；错误时保留行内提示，不自动重复创建内容。图片确认后进入与普通文件相同的上传队列。浏览器通常要求 HTTPS 或 loopback 才提供剪贴板读取；不支持时继续使用手动粘贴和文件选择。

### 设备与网络

设备链接只接受纯 HTTP/HTTPS origin，拒绝用户名密码、路径、查询参数、片段与相对地址；新页面使用 `noopener noreferrer` 和 `no-referrer`。管理密钥继续保留在当前会话，请求只发到当前节点的相对 API 路径。

“在线”仅表示现有健康检查或发现状态。当前节点的访问密钥状态直接读取配置；其他节点仍需独立认证。设备名称、地址和错误信息按文本处理，不进入可执行 HTML。

### 传输

失败重试进入同一个调度器，修复旧逻辑在 Promise 已结束后直接重新发送而绕过并发限制的问题。任务有独立状态和终结保护，排队取消不发送请求，迟到事件不能把取消改成完成。请求体发送完毕后仍等待服务端响应。队列有记录上限，并允许清理终态任务、释放文件引用。

这不提供可回滚的网络交付：连接中断或取消发生时，服务端可能已经保存内容。重试前应检查列表。上传分片续传、接收方批准和对端身份绑定需要新的服务端协议，不能仅用进度条或设备标签模拟。

## 未引入的系统能力

Oink 的剪贴板策略默认允许发送和接收，并有策略 epoch 用于使旧 reader 失效；OmniShare 本轮采用每次显式操作，未移植后台同步策略。[policy.rs](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/crates/oink-clipboard/src/policy.rs)

Oink 的 Windows 安装包配置了自动启动的 LocalSystem 输入服务及覆盖所有网络配置文件的入站规则。这些权限服务于其 KVM 用途，不能作为普通内容共享的默认安装行为。本轮没有增加系统服务、防火墙开放、输入监听、辅助功能授权或音频驱动。[Package.wxs](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/packaging/windows/Package.wxs)

若后续实现直接设备配对，需先定义单设备身份、双方确认、发送/接收方向权限、授权范围与到期时间、即时撤销、接收同意和会话重放防护。不能以六位短码加普通哈希代替经过审查的密码协议，也不能把组内成员资格直接等同于内容管理权限。

## 许可与依赖

参考仓库提供 [GPL v3](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/LICENSE) 和 [附加条款](https://github.com/oinkvm/oink/blob/546f65b2135e7a1491d2d743f0a1e072a74df36f/LICENSE-ADDITIONAL-TERMS.md)。本轮按公开功能和交互思想独立实现，没有引入 Oink 源码或资产，没有改变 OmniShare 的许可声明，也不暗示得到 Oink 作者背书。

运行时依赖未增加。Playwright `1.62.1` 是锁定的开发测试依赖，仅用于浏览器检查，不进入 Go 内嵌前端或桌面运行包。

## 验证入口

```bash
npm --prefix frontend run check
npm --prefix frontend test
npm --prefix frontend run build
npm --prefix frontend ci --ignore-scripts
(cd frontend && npx --no-install playwright install --with-deps chromium)
npm --prefix frontend run test:browser
```

剪贴板和队列测试覆盖确认前无请求、取消清理、迟到结果、重复提交、类型与大小限制、重试并发和错误反馈。Chromium 检查实际 HTML/CSS/JS、明暗主题、导航、设置、剪贴板、上传与多种屏宽；使用合成数据并将截图作为 CI artifact 保存。后端鉴权和持久化继续由 Go 与独立 HTTP 进程测试覆盖。
