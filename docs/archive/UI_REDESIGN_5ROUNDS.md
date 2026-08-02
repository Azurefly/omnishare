# OmniShare UI/UX 5 轮设计与迭代规范文档

- **文档版本**：v2.0.0
- **优化目标**：彻底重构当前 OmniShare 的用户界面，消除“程序员审美”与粗糙感，打造媲美 Apple / Vercel 级别的精致设计系统与极佳交互体验。
- **5 轮迭代路线**：
  1. **Round 1 (Design System & Tokens)**：建立统一的色彩语义、玻璃拟物 (Glassmorphism)、层次结构与圆角排版规范；
  2. **Round 2 (Header & Peer Perception Component)**：重构头部导航栏与网络设备节点状态卡片，增强 Tailscale / LAN 视效感；
  3. **Round 3 (Interactive Quick Note & Markdown Canvas)**：重构极速随手记板，增加优雅卡片流、动画反馈与高亮标签；
  4. **Round 4 (Collaborative Multi-User Pad UI)**：重构多人协同编辑记事本，增加在线成员高亮、光标流向、状态指示与全屏编辑态；
  5. **Round 5 (File Transfer & Plyr Stream Player)**：重构大文件拖拽区域与大视频 HTTP 206 Plymouth 播放器窗口。

---

## 一、 Round 1: 设计系统 (Design System & Color Tokens)

### 1.1 颜色系统 (Palette)
- **Background Layer 0**: `#0B0F17` (Deep Obsidian / 极夜黑)
- **Card Layer 1**: `#151C28` (Slate Navy / 灰蓝暗色卡片)
- **Card Layer Hover**: `#1E293B`
- **Accent Primary (Brand)**：`#6366F1` (Indigo 500) -> `#818CF8` (Indigo 400)
- **Accent Collaborative (Pad)**：`#EC4899` (Pink 500) -> `#F472B6` (Pink 400)
- **Success / LAN**: `#10B981` (Emerald 500)
- **Tailscale Magic**: `#8B5CF6` (Purple 500)

### 1.2 字体与阴影效果
- **Font Stack**: `Inter`, `SF Pro Display`, `-apple-system`, `BlinkMacSystemFont`, `Segoe UI`, `PingFang SC`, `Microsoft YaHei`
- **Card Border**: `border border-slate-800/80`
- **Shadow**: `shadow-[0_8px_30px_rgb(0,0,0,0.4)]`
- **Backdrop**: `backdrop-blur-xl bg-slate-900/70`

---

## 二、 Round 2: 头部导航栏与网络节点卡片重构规范

- 采用 **Glassmorphism 玻璃拟态 Header**；
- 节点列表中，LAN 设备显示脉冲绿灯与局域网 IP，Tailscale 设备显示专属紫光与 MagicDNS 域名；
- 增加节点测延时 (RTT) 的呼吸动画指示。

---

## 三、 Round 3: 极速随手记与剪贴板卡片重构规范

- 输入框获得焦点时显示 `ring-2 ring-indigo-500/50` 渐变边框；
- 发布按钮增加精致渐变色彩 `bg-gradient-to-r from-indigo-500 to-purple-600`；
- 随手记卡片右上角增加轻量悬浮工具栏（一键复制、Raw 浏览、删除）。

---

## 四、 Round 4: 多人协同记事本高级编辑界面规范

- 顶栏显示当前文档房间名字、实时协同在线人数微缩头像；
- 编辑区域支持仿 IDE 代码/文档字体，带有当前状态闪烁指示。

---

## 五、 Round 5: 大文件拖拽与视频播放器重构规范

- 拖拽高亮区域支持微渐变边框与虚线脉冲效果；
- Plymouth 播放器窗口使用优雅大圆角与渐变背景模态框。
