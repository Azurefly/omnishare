# OmniShare (Antigravity-Share) 局域网与 Tailscale 虚拟内网跨设备速记与视频/文件共享平台
# 交互设计与评审文档 (Interaction Design & Review Specification)

- **文档版本**：v1.0.0
- **撰写时间**：2026年8月
- **项目代号**：OmniShare / Antigravity-Share
- **密级**：内部开源工程规范
- **评审状态**：APPROVED (评审通过)

---

## 1. 设计理念与 UI 架构规范 (Design Philosophy & UI Architecture)

### 1.1 设计理念
OmniShare 的设计哲学核心为 **“无感协同，极速流转”**：
1. **极致轻量 (Minimalist & Frictionless)**：无需注册账号、无需冗长的引导流程，打开 Web 即直接呈现“随手记”与“拖拽文件投送”核心区域。
2. **多端自适应 (Mobile & Desktop First)**：针对桌面端宽屏与移动端单手操作分别优化布局。
3. **黑夜/白天模式自适应 (Theme Adaptability)**：默认跟随操作系统 `prefers-color-scheme` 自动切换 Dark/Light 视觉主题。

### 1.2 全局色彩与视觉系统
- **主品牌色 (Primary Color)**：`#3B82F6` (Tailwind `blue-500`) / 暗黑模式 `#60A5FA` (`blue-400`)
- **成功与连通指示色 (Success/Online)**：`#10B981` (Tailwind `emerald-500`)
- **Tailscale 标识色 (Tailnet Tag)**：`#8B5CF6` (Tailwind `purple-500`)
- **背景与卡片色 (Backgrounds)**：
  - 白天模式：主背景 `#F9FAFB` (`gray-50`)，卡片背景 `#FFFFFF`
  - 暗黑模式：主背景 `#111827` (`gray-900`)，卡片背景 `#1F2937` (`gray-800`)

---

## 2. 设备适配与布局策略 (Layout Strategies)

### 2.1 桌面端布局 (Desktop Wide Layout, >= 1024px)
采用三栏/双栏自适应网格布局：
- **左侧导航与节点感知栏 (Sidebar, 260px)**：展示系统 Logo、主菜单（随手记、文件列表、大视频流）、已感知设备节点（LAN 节点与 Tailscale 节点列表及 RTT 延时）；
- **主内容工作区 (Main Container, Flexible)**：顶部为极速文本发布框与文件拖拽区，下方为随手记卡片流；
- **右侧视频/文件预览抽屉 (Preview Drawer, 380px, 可选展开)**：点击视频或文件卡片时从右侧平滑滑动抽屉，内置 Plyr HTML5 播放器秒级播放视频。

### 2.2 移动端布局 (Mobile Layout, < 768px)
- **顶部 Header**：包含 OmniShare 品牌标示、当前网络状态（如 `LAN 192.168.1.5` 或 `Tailnet` 标签）及全局设置按钮；
- **底部 Tab 导航栏 (Bottom Navigation Bar, 56px)**：
  - 📝 **速记** (Notes)
  - 📁 **文件** (Files)
  - 🎬 **视频** (Videos)
  - 💻 **设备** (Devices)
- 移动端浮动操作按钮 (FAB)：右下角居上提供 `+` 悬浮按钮，一键唤起全局上传与文本粘贴面板。

---

## 3. 核心交互流线图 (Interaction Flow Diagrams)

### 3.1 随手记极速发布与剪贴板一键同步流 (Quick Notes Flow)
```
[用户在任意设备打开 Web] 
          |
          v
[点击 "一键粘贴剪贴板" 按钮] ---> (浏览器弹出 Clipboard 授权提示)
          |
          v
[自动提取文本并预填入输入框] 
          |
  (可选择设置标签 #tag 或阅后即焚)
          |
          v
[点击 "发布" / 按下 Ctrl+Enter]
          |
          v
[后端写入 SQLite 数据库] ---> [通过 WebSocket 向所有在线节点广播 note_created 事件]
          |
          v
[其他所有设备的 Web 界面上无需刷新，秒级自动滑动出现新卡片并伴随轻微动画通知]
```

### 3.2 大文件/文件夹拖拽分片上传流 (File Chunked Upload Flow)
```
[用户将大文件拖入浏览器上传区域]
          |
          v
[页面触发 Visual Drag Hover 高亮态]
          |
          v
[前端 Web Worker 启动：计算 5MB 分片及 SHA-256 哈希]
          |
          v
[校验秒传接口 GET /api/v1/files/check]
   |                        |
(哈希已存在: 秒传成功)    (哈希不存在: 启动 Tus 分片并发上传)
   |                        |
   v                        v
[界面显示 "100% 秒传!"]  [显示分片进度条、实时 MB/s 速度与 ETA 倒计时]
```

### 3.3 超大视频 HTTP 206 字节流秒级点播与快进流 (Video Stream Flow)
```
[用户点击视频卡片 "立即播放"]
          |
          v
[弹出的 Video Player Modal 呼出 Plyr 播放器]
          |
          v
[发送 HTTP GET /api/v1/files/:id/stream (Header: Range: bytes=0-)]
          |
          v
[后端响应 HTTP 206 Partial Content + Content-Range: bytes 0-65535/3221225472]
          |
          v
[播放器在 < 200ms 内瞬间播出生动画质]
          |
          v
[用户拖拽进度条至 75% 处 (如第 45 分钟)]
          |
          v
[播放器立即触发新请求 Range: bytes=2415919104-] ---> [后端直接 Seek 磁盘输出字节, 无需重新下载前 75% 数据]
```

---

## 4. 评审意见记录与签署 (Review Log & Sign-off Panel)

### 4.1 评审记录矩阵 (Review Log)

| 评审项 | 评审专家观点 | 针对性修改与优化落地方案 | 状态 |
| :--- | :--- | :--- | :--- |
| **网络感知指示** | 移动端网络环境多变，用户无法直观辨别当前使用的是 LAN 还是 Tailscale。 | 在 Header 头部增加高亮 Status Badge：绿灯显示 `LAN (192.168.x.x)`，紫色显示 `Tailscale (100.x.y.z)`。 | **RESOLVED** |
| **剪贴板读取授权** | 部分移动端 Safari 限制非安全 HTTP 下使用 `navigator.clipboard`。 | 增加优雅降级方案：若 Clipboard API 被禁用，自动聚焦 input 并提示用户直接使用长按粘贴。 | **RESOLVED** |
| **超大视频拖拽体验** | 拖拽快进时如果反复发送大量 Range 请求，容易引发后端协程堆积。 | 在前端组件中为 Range Request 添加 150ms 节流 (Debounce)，连续拖拽只取最后落点。 | **RESOLVED** |

### 4.2 评审结论与签署 (Sign-off)
- **架构师签署**：已审核，符合局域网与 Tailscale 双引擎设计规范。 (Signed)
- **UI/UX 专家签署**：已审核，符合响应式、多端自适应及 Plyr 交互规范。 (Signed)
- **研发负责人签署**：已审核，批准进入代码实现阶段。 (Signed)
