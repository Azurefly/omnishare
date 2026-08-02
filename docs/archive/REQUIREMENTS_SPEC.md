# OmniShare (Antigravity-Share) 局域网与 Tailscale 虚拟内网跨设备速记与视频/文件共享平台
# 需求设计文档 (Requirements Specification Document)

- **文档版本**：v1.0.0
- **撰写时间**：2026年8月
- **项目代号**：OmniShare / Antigravity-Share
- **密级**：内部开源工程规范
- **目标读者**：架构师、研发工程师、测试工程师、运维工程师

---

## 目录 (Table of Contents)

1. [执行摘要与项目背景 (Executive Summary & Project Background)](#1-执行摘要与项目背景)
2. [痛点分析与行业标杆开源项目深度对比 (Deep Benchmark Analysis of Open-Source Projects)](#2-痛点分析与行业标杆开源项目深度对比)
   - 2.1 Memos (usememos)
   - 2.2 Microbin
   - 2.3 LocalSend
   - 2.4 PairDrop / Snapdrop
   - 2.5 Pingvin Share
   - 2.6 Sharry
   - 2.7 12维开源项目综合对比矩阵
3. [网络架构与传输拓扑深度拆解 (Network Architecture & Transport Topology)](#3-网络架构与传输拓扑深度拆解)
   - 3.1 局域网 Layer-2 mDNS / UDP 5353 组播发现机制与原理
   - 3.2 Tailscale Layer-3 WireGuard CGNAT (100.64.0.0/10) 虚拟网络原理
   - 3.3 Layer-2 组播与 Layer-3 覆盖网络的技术冲突与限制
   - 3.4 双引擎网络自适应与发现路由融合方案
4. [核心功能需求规范 (Functional Requirements Specification)](#4-核心功能需求规范)
   - 4.1 极速随手记与富文本片段引擎需求 (Quick Notes Engine)
   - 4.2 大文件与跨设备传输引擎需求 (File Sharing & Transfer Engine)
   - 4.3 超大视频在线流式播放与快进引擎需求 (HTTP 206 Video Streaming Engine)
   - 4.4 自动化设备发现与连接感知需求 (Auto Device Discovery & Status Sensing)
   - 4.5 权限控制、访问安全与隐私保护需求 (Security & Privacy Control)
5. [非功能性需求规范 (Non-Functional Requirements Specification)](#5-非功能性需求规范)
   - 5.1 性能与吞吐量需求 (Performance & Throughput)
   - 5.2 并发处理与极低内存占用需求 (Concurrency & Low Footprint)
   - 5.3 存储持久性、一致性与 SQLite 事务需求 (Storage & Durability)
   - 5.4 跨平台兼容性与 Web Share Target PWA 需求 (Compatibility & PWA)
6. [领域实体与数据模型分析 (Domain Entity & Data Model Analysis)](#6-领域实体与数据模型分析)
7. [部署与运维需求规范 (Deployment & Operational Requirements)](#7-部署与运维需求规范)
   - 7.1 单二进制文件 Golang 零依赖部署规范
   - 7.2 Docker 容器化与轻量部署规范
   - 7.3 Tailscale Serve & Funnel HTTPS 证书自动续签集成规范
8. [系统边界、约束条件与演进规划 (System Boundaries & Roadmap)](#8-系统边界约束条件与演进规划)

---

## 1. 执行摘要与项目背景

### 1.1 业务痛点与行业背景
在现代多设备数字工作流中，个人用户与小微团队通常拥有多台不同操作系统的物理设备，例如：
- 桌面端：Windows 11 工作站、macOS 开发机、Ubuntu 临时服务器；
- 移动端：Android 智能手机、iOS / iPadOS 设备；
- 存储端：家庭 NAS (Synology / Unraid / TrueNAS)、树莓派、或边缘计算节点。

用户在日常工作与生活中，高频产生以下三类协同需求：
1. **跨设备极速剪贴板/文字速记**：需要将一段 API Key、验证码、命令行指令、文本草稿或 URL 从手机瞬间同步到电脑，或者从 Mac 传输到 Windows。
2. **大文件与大视频传输**：需要将手机拍摄的 4K 60fps 原画视频（单文件 2GB~20GB）或工程大压缩包发送到桌面电脑。
3. **大视频零下载即时在线流播**：在不将几 GB 视频文件完整下载到本地存储的前提下，能够在平板或手机浏览器中点击即看，且支持秒级拖拽进度条快进快退。

然而，现有市场方案在同时跨越**本地物理局域网 (Wi-Fi / Ethernet LAN)** 与 **Tailscale 异地 WireGuard 虚拟内网 (Tailnet)** 时，遭遇严重痛点：
- 传统局域网工具（如 LocalSend、Snapdrop）严重依赖 Layer-2 组播广播（mDNS / UDP Broadcast）。当用户外出、使用 5G / 异地 Wi-Fi 通过 Tailscale 连回主路由或 NAS 时，由于 WireGuard 属于 Layer-3 虚拟网络，默认不支持 mDNS 广播，导致局域网传输工具直接失效，设备无法发现；
- 传统网盘或协同工具（如 Synology Drive、Nextcloud）过于沉重，网页响应慢，缺少剪贴板随手记的极致快感；且对视频流式播放支持差，拖拽进度条频繁卡顿或触发全量下载。

### 1.2 项目目标与 Vision
**OmniShare (Antigravity-Share)** 旨在打造一款**专注于局域网与 Tailscale 虚拟内网环境**的私有化、轻量级、免安装（基于 Modern Web PWA）、高吞吐量的多设备速记与大视频/文件协同平台。

核心 Vision 总结为：
- **免安装、开箱即用 (Zero-Client PWA)**：任意设备只需打开现代 Web 浏览器（Chrome, Safari, Edge, Firefox），即可获得媲美原生 App 的传输体验；
- **网络双引擎自适应 (Dual-Network Adaptability)**：在 LAN 环境下自动启用 mDNS 零配置广播发现；在 Tailscale 环境下自动融合 MagicDNS 与 `tsnet`，实现跨地域设备无感连通；
- **超大视频秒播 (Instant Range Video Stream)**：内置 HTTP 206 Byte-Range 字节流引擎与 `moov` header Faststart 优化，支持 GB 级视频无卡顿秒在线拖拽播放；
- **单文件开箱即用 (Single Binary Footprint)**：基于 Go + SQLite + 嵌入式 Vue 3 静态前端，整体可执行程序 <20MB，运行内存 <30MB，支持树莓派及低配 Docker 容器极速运行。

---

## 2. 痛点分析与行业标杆开源项目深度对比

为了明确 OmniShare 的技术方案优势，本章节对当前主流开源工具进行全方位的对比分析。

### 2.1 Memos (usememos)
- **定位**：基于 Go + React + SQLite 的开源隐私优先卡片式笔记与微博客系统。
- **优点**：界面精美，Markdown 支持完备，SQLite WAL 高效持久化，提供 REST/gRPC API。
- **缺点**：
  1. 侧重于文本记录与卡片知识库管理，文件上传缺少分片分块与断点续传机制；
  2. 对大视频完全缺乏 HTTP 206 Range 优化与流式播放拖拽优化，超过 1GB 视频上传与播放易崩溃；
  3. 无任何局域网或 Tailscale 设备感知/自动投送功能。

### 2.2 Microbin
- **定位**：基于 Rust 编写的超轻量级 Pastebin 与简单文件共享服务器。
- **优点**：极其轻量，支持密码保护、自动销毁（Burn after reading）、生成可爱的动物名称 URL（如 `dog-cat-pig`），提供 Raw 纯文本接口支持 `curl` 读取。
- **缺点**：
  1. UI 相对单薄，缺少卡片流与 Markdown 富文本预览；
  2. 不支持在线视频播放器交互；
  3. 无法自动感知内网设备，需手动复制粘贴链接。

### 2.3 LocalSend
- **定位**：基于 Flutter 编写的跨平台局域网文件传输 App。
- **优点**：完全点对点（P2P），使用 REST API + mDNS 协议，在同 Wi-Fi 环境下体验极佳，安全性高（内置 TLS 证书生成）。
- **缺点**：
  1. **必须在所有接收端和发送端安装原生 App**，对于临时访问的电脑或手机极为不便；
  2. 依赖 Layer-2 mDNS 广播，**无法直接在 Tailscale 异地 CGNAT 100.x 网段下无缝工作**（除非手动输入 IP）；
  3. 属于同步传输工具，发送方与接收方必须同时在线并保持 App 前台运行，无法做到“先上传到私有节点，其他设备随时离线流播/下载”。

### 2.4 PairDrop / Snapdrop
- **定位**：基于 Node.js WebSocket + WebRTC 技术的浏览器端 P2P 共享工具（AirDrop 的 Web 替代品）。
- **优点**：网页即开即用，无需安装任何客户端；支持局域网配对。
- **缺点**：
  1. 强依赖 WebRTC 数据通道（DataChannel）。当传输几 GB 的大视频时，浏览器内存缓冲膨胀极其严重，极易引发 Chrome 页面崩溃（OOM）；
  2. 传输要求两端设备必须在同一时间保持网页处于前台打卡状态；
  3. WebRTC 穿越 NAT 困难，在复杂 Tailscale 虚拟网段或双重 NAT 下成功率显著下降。

### 2.5 Pingvin Share
- **定位**：基于 Next.js / NestJS / Docker 的自建 WeTransfer 替代品。
- **优点**：支持生成分享链接、限制下载次数、设置密码与过期时间，异步存储于服务器端。
- **缺点**：
  1. 架构偏重（Next.js Node 进程内存占用常态化 200MB+）；
  2. 缺乏随手记（Snippet）极速剪贴板管理；
  3. 视频直接作为文件下载提供，缺少专业的 HTML5 拖拽流播放器集成。

### 2.6 Sharry
- **定位**：基于 Scala / Akka 的高阶文件共享与 Dropzone 系统。
- **优点**：支持用户系统、Alias 客户端上传、功能极其丰富。
- **缺点**：JVM 栈极其臃肿，冷启动慢，硬件资源消耗大，完全不适合轻量级私有云部署。

### 2.7 12维开源项目综合对比矩阵

| 对比维度 | LocalSend | PairDrop | Memos | Microbin | Pingvin Share | **OmniShare (本项目)** |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **客户端依赖** | 强依赖原生 App | 仅 Web (需在线) | 仅 Web | 仅 Web | 仅 Web | **仅 Web (支持 PWA 安装)** |
| **LAN mDNS 自动发现** | 原生支持 | 依赖 WebSocket 信令 | 无 | 无 | 无 | **原生支持 (UDP 5353)** |
| **Tailscale / Tailnet 适配** | 需手动敲 IP | 易连接失败 | 依赖外部反代 | 无 | 依赖外部反代 | **原生内置 tsnet & MagicDNS** |
| **异步离线存储** | ❌ 仅同步 P2P | ❌ 仅同步 P2P | ✅ 支持 | ✅ 支持 | ✅ 支持 | **✅ 支持 (SQLite + Disk)** |
| **文本速记 / 剪贴板** | 弱 (仅临时消息) | 弱 (仅临时消息) | ✅ 强 (Markdown) | ✅ 强 (Raw/Burn) | ❌ 仅支持文件 | **✅ 强 (速记+标签+Markdown)** |
| **GB 级大视频在线流播** | ❌ 无法流播 | ❌ 易内存溢出崩溃 | ❌ 全量下载慢 | ❌ 全量下载 | ❌ 全量下载 | **✅ 支持 (HTTP 206 秒级拖拽)** |
| ** Faststart MOOV 优化** | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 | **✅ 自动化文件头重排优化** |
| **断点续传 / 分片上传** | ✅ 自自定义 HTTP | ❌ 无 | ❌ 无 | ❌ 无 | ✅ 支持 | **✅ 支持 (Tus / HTTP Chunked)** |
| **单文件二极体部署** | ❌ 需安装环境 | ❌ Node 部署 | ❌ 复合静态资源 | ❌ 仅后端 | ❌ Docker 镜像大 | **✅ 单 Golang 二进制 (<20MB)** |
| **运行内存 footprint** | ~50MB (Flutter) | ~150MB (Node) | ~80MB (Go/SQLite) | ~20MB (Rust) | ~250MB (Node) | **< 30MB (Go + Embedded Vue)** |
| **Web Share Target API** | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 | **✅ 支持系统级“分享到...”** |
| **安全与加密保护** | TLS 内置证书 | WebRTC DTLS | Token JWT | Password / AES | Password / Token | ** Password / One-time Token** |

---

## 3. 网络架构与传输拓扑深度拆解

为了在各种复杂的网络拓扑下实现“零配置、秒发现、高吞吐”，必须深刻理解局域网与 Tailscale 在底层协议和网络拓扑上的本质差异。

### 3.1 局域网 Layer-2 mDNS / UDP 5353 组播发现机制与原理
在标准 IEEE 802.3 (以太网) 与 IEEE 802.11 (Wi-Fi) 局域网中：
- 节点位于同一个广播域（Broadcast Domain）或 Layer-2 网段（例如 `192.168.1.0/24`）；
- mDNS (Multicast DNS, RFC 6762) 使用预设的 IPv4 组播地址 `224.0.0.251` 及 UDP 端口 `5353`；
- DNS-SD (DNS Service Discovery, RFC 6763) 允许 OmniShare 后端启动时注册服务类型（例如 `_omnishare._tcp.local.`），并在响应中附带节点 Hostname、Port 及 TXT 键值对（如 `version=1.0`, `node_id=xxx`）。

**局域网发现工作流程**：
1. OmniShare 节点 A 在 `0.0.0.0:5353` 发送 UDP 组播查询包，询问网段内所有 `_omnishare._tcp.local.` 实例；
2. 局域网内节点 B 收到该组播包后，单播/组播回复其 IP 地址（`192.168.1.105`）及服务端口（`8080`）；
3. 客户端前端通过 WebSocket 或 API 轮询，动态更新设备在线列表。

### 3.2 Tailscale Layer-3 WireGuard CGNAT (100.64.0.0/10) 虚拟网络原理
Tailscale 是基于 WireGuard 协议构建的安全覆盖网络（Overlay Network）：
- 节点分配得到一个 CGNAT (Carrier-Grade NAT) IPv4 地址（范围为 `100.64.0.0/10`，如 `100.115.82.43`）以及 IPv6 地址；
- 节点间建立 P2P 加密隧道（DERP 中继兜底）；
- Tailscale 依靠全局控制平面与 MagicDNS 提供内部域名解析（如 `my-macbook.tailnet-name.ts.net`）。

### 3.3 Layer-2 组播与 Layer-3 覆盖网络的技术冲突与限制
为什么不能直接在 Tailscale 环境下使用 mDNS？
1. **WireGuard 协议限制**：WireGuard 属于虚拟 Layer-3 TUN 设备。TUN 接口仅处理 IP 数据包，**默认不会封装或转发 Layer-2 广播包和 ARP/mDNS 组播包**；
2. **跨子网隔离**：Tailscale 节点分散于不同的物理网络（如公司的 Wi-Fi、家里的宽带、手机 5G），底层 IP 包经由 Encapsulated UDP 转发，组播 TTL 被丢弃。

因此，若软件仅设计 mDNS 发现引擎，在用户切换到 Tailscale 网络时，**设备列表将完全变空**，传输功能断连。

### 3.4 双引擎网络自适应与发现路由融合方案
OmniShare 提出了创新的**双引擎网络自适应架构 (Dual-Engine Adaptability Topology)**：

```
                      +------------------------------------------+
                      |         OmniShare 混合适配层             |
                      +------------------------------------------+
                                     /            \
                  (内部检测发现)   /                \  (内部检测发现)
                                  v                  v
                 +-----------------------+  +-----------------------+
                 | LAN mDNS 组播引擎     |  | Tailscale 发现引擎    |
                 | (UDP 5353 / Zeroconf) |  | (tsnet / MagicDNS)    |
                 +-----------------------+  +-----------------------+
                            |                            |
                            | [IPv4 192.168.x.x]         | [IPv4 100.x.y.z]
                            v                            v
                 +--------------------------------------------------+
                 |            统一网络路由表与设备注册表            |
                 | - 节点 ID、设备类型、多网卡 IP 优先级计算         |
                 | - 心跳健康检测 (WebSocket Keep-Alive / HTTP Ping) |
                 +--------------------------------------------------+
                                         |
                                         v
                         +----------------------------------+
                         |     前端 Vue 3 统一视图表现层    |
                         |  (展示 LAN 节点与 Tailnet 节点)  |
                         +----------------------------------+
```

**路由决策逻辑**：
1. **IP 优先级排序算法**：若两台设备同时在 LAN 和 Tailscale 中在线，系统自动测延时（RTT），优先采用 RTT 更短的物理 LAN IP（如 `192.168.1.100`），获得物理直连千兆带宽；
2. **Tailscale MagicDNS 备用路由**：当检测到物理 LAN mDNS 心跳中断，且 IP 属于 `100.x.y.z` 时，自动无缝平滑切到 Tailscale 隧道进行传输；
3. **Tailscale Serve 自动代理**：利用 Tailscale 内置的 Serve API，自动绑定系统的 `8080` 端口到 `https://<node-name>.<tailnet>.ts.net`，免去手动配置 Let's Encrypt TLS 证书的繁琐流程。

---

## 4. 核心功能需求规范

### 4.1 极速随手记与富文本片段引擎需求 (Quick Notes Engine)

#### 4.1.1 需求描述
用户在移动端或桌面端触发随手记时，需要输入或粘贴文本信息（如剪贴板代码段、短网址、临时记忆文本、Markdown 草稿），支持发布、分类管理与即时消费。

#### 4.1.2 详细功能点规范
- **F-NOTE-01 纯文本与 Markdown 混排解析**：支持标准 Markdown 语法预览，自动识别 HTTP/HTTPS 链接并将其转化为可点击的超链接卡片；支持代码块语法高亮（C, C++, Go, Python, JS, Shell, HTML/CSS）。
- **F-NOTE-02 一键剪贴板读取与一键复制**：
  - 界面提供快捷按钮“粘贴剪贴板内容”，前端自动调用 `navigator.clipboard.readText()` API；
  - 列表每张卡片右上角提供“一键复制”按钮，自动复制卡片原文并弹出轻量 Toast 提示。
- **F-NOTE-03 文本分类与标签系统 (Tags)**：自动解析正文中的 `#标签名` 格式，生成侧边栏动态标签分类过滤。
- **F-NOTE-04 RAW / API 访问支持 (Curl Friendly)**：
  - 每条随手记生成唯一短 ID（例如 `/n/x8k9a`）；
  - 当通过命令行 `curl -L http://<host>:8080/n/x8k9a/raw` 访问时，后端直接返回 `text/plain; charset=utf-8` 的纯文本内容，方便脚本提取或 Linux 服务器直接管道读取。
- **F-NOTE-05 阅后即焚与定时自动销毁 (Burn/TTL)**：
  - 支持发帖时设置有效寿命：`15分钟`、`1小时`、`24小时`、`7天` 或 `永久`；
  - 支持“阅读 1 次后自动物理擦除 (Burn after reading)”选项。

---

### 4.2 大文件与跨设备传输引擎需求 (File Sharing & Transfer Engine)

#### 4.2.1 需求描述
系统必须提供高吞吐、高稳定性的文件上传与下载机制，支持多文件拖拽、文件夹结构保持以及针对大文件（>2GB）的分片断点续传。

#### 4.2.2 详细功能点规范
- **F-FILE-01 拖拽与多文件批处理上传**：
  - Web 界面支持直接拖拽任意数量的文件或文件夹到上传区域；
  - 动态计算上传进度条、当前实时传输速率（MB/s）、预计剩余时间（ETA）。
- **F-FILE-02 大文件分片与断点续传 (Resumable Chunked Upload)**：
  - 对于切片文件（单文件 > 50MB），前端采用 HTML5 `File.slice()` 按 5MB 分片切块，计算 SHA-256 / MD5 哈希校验值；
  - 支持标准 Tus.io 协议或轻量 HTTP Chunked 分片协议。若网络中途断开（如 Wi-Fi 切换或 Tailscale 隧道重连），再次上传时自动从断点切片继续传输，无须重新发送已有数据。
- **F-FILE-03 压缩包自动打包与批量下载**：勾选多个文件或随手记附件时，后端动态生成 ZIP 流式压缩包（`application/zip`），无需在磁盘生成临时大压缩文件，直接管道化输出给浏览器下载。
- **F-FILE-04 二维码快捷扫码下载**：前端自动为每个分享文件生成包含完整 URL 的动态 SVG 二维码，方便移动端手机相机扫码直接打开浏览器下载。

---

### 4.3 超大视频在线流式播放与快进引擎需求 (HTTP 206 Video Streaming Engine)

#### 4.3.1 需求描述
对于手机或电脑上传的大容量视频文件（如 4K MP4, MOV, MKV, WebM，单文件可达 10GB~30GB），用户**决不能等待整本视频下载完成后再看**，也**不能忍受卡顿与漫长的转码等待**。要求系统具备即时点播与任意位置秒级拖拽能力。

#### 4.3.2 详细功能点规范
- **F-VID-01 HTTP 206 Byte-Range 响应机制**：
  - 后端文件服务模块必须完备实现 RFC 7233 规范；
  - 准确解析请求头 `Range: bytes=start-end`；
  - 返回 HTTP 状态码 `206 Partial Content`，并在响应头中正确写入 `Content-Range: bytes start-end/total` 以及 `Content-Length`；
  - 正确处理 `200 OK` (非 Range 请求) 与 `416 Range Not Satisfiable` (请求越界) 异常情况。
- **F-VID-02 视频 Atom 元数据 Header 前置 (Faststart Optimization)**：
  - 许多手机录制的 MP4 视频默认将索引原子（`moov` atom）放置在文件末尾。若直接流播，浏览器必须下载完整文件的末尾才能开始播放；
  - 后端在收到 MP4 上传后，自动轻量检测 `moov` 位置；若处于尾部，后台异步调用内置纯 Go MP4 Parser 或 `ffmpeg` 执行 `-movflags +faststart`，将 `moov` 移至文件头部（`ftyp` 之后）。优化完成后，实现网页秒开播放。
- **F-VID-03 Plyr.js 高阶播放器集成**：
  - 网页内置 Plyr HTML5 播放器，提供优雅的播放/暂停、音量控制、全屏切换、画中画 (Picture-in-Picture) 模式；
  - 支持 `0.5x`, `1.0x`, `1.25x`, `1.5x`, `2.0x` 多倍速播放；
  - 支持键盘快捷键控制（`Space` 暂停，`← / →` 快退/快进 5 秒，`↑ / ↓` 音量调节，`F` 全屏）。
- **F-VID-04 封面图与动态微缩图提取 (Thumbnail Generation)**：
  - 后端自动在视频第 3 秒提取一张 JPEG 格式的静态缩略图作为预览封面，减少全量视频数据的初始传输开销。

---

### 4.4 自动化设备发现与连接感知需求 (Auto Device Discovery & Status Sensing)

#### 4.4.1 需求描述
用户在任意设备上打开 OmniShare Web 页面，界面右侧或顶部面板能够直观显示当前物理 LAN 与 Tailscale 网络中**所有运行 OmniShare 节点的设备状态**（如：“MacBook Pro (LAN 192.168.1.12)”、“Unraid NAS (Tailscale 100.115.82.43)”）。

#### 4.4.2 详细功能点规范
- **F-DISC-01 mDNS / UDP 广播注册与监听**：
  - 启动后自动以 `_omnishare._tcp` 服务名进行局域网组播；
  - 定期发送 UDP 心跳包（每 10 秒）；自动淘汰 30 秒内未收到心跳的异常失效节点。
- **F-DISC-02 Tailscale Node Agent 感知**：
  - 后端自动检查系统环境变量或本地 Tailscale Daemon 套接字 (`/var/run/tailscale/tailscaled.sock` 或 Windows pipe)；
  - 自动获取本机在 Tailnet 中的 MagicDNS 名字（如 `nas.tailnet.ts.net`）及 `100.x.y.z` 地址，并显示于设备节点面板。
- **F-DISC-03 一键定向投送 (Direct AirDrop-like Push)**：
  - 用户选定一条随手记或一个文件后，可以在设备列表中点击“投送至 [iPhone 15 Pro]”；
  - 目标设备的 OmniShare 网页通过 WebSocket 及时弹出极简模态框：“收到来自 [MacBook] 的文件 [demo.mp4]”，并提供一键预览/接收按钮。

---

### 4.5 权限控制、访问安全与隐私保护需求 (Security & Privacy Control)

#### 4.5.1 需求描述
由于系统部署在局域网与 Tailscale 虚拟网段中，必须防止未授权的第三方（如公共 Wi-Fi 下的其他访客或 Tailnet 中的非信任节点）非法读取私有数据。

#### 4.5.2 详细功能点规范
- **F-SEC-01 访问密码与 Token 保护机制**：
  - 支持全局访问口令（Passphrase）模式：访问 Web 界面前须输入口令，验证成功后颁发 HMAC-SHA256 签名的 HTTP-Only Cookie 或 JWT Token；
  - 支持无密码（Public Read-Only / Guest Mode）模式：公开查看公共文本，但上传文件或发布随手记需要输入 Admin Pin。
- **F-SEC-02 单次分享临时提取码 (PIN Access)**：
  - 对单个敏敏感文件或随手记生成 4 位 / 6 位数字提取码；
  - 访问者只需输入提取码即可完成单次兑换下载。
- **F-SEC-03 路径安全与防目录穿越 (Anti-Directory Traversal)**：
  - 后端对所有文件下载及静态资源访问路径执行严格校验（使用 Go `filepath.Clean`），禁止通过 `../` 越界读取系统物理敏感文件（如 `/etc/passwd`）。
- **F-SEC-04 CORS 与 Safe-Origin 限制**：
  - 仅允许预设的网段（`192.168.0.0/16`, `10.0.0.0/8`, `172.16.0.0/12`, `100.64.0.0/10`）访问 API，阻断来自公网非授权 IP 的恶意探针攻击。

---

## 5. 非功能性需求规范

### 5.1 性能与吞吐量需求 (Performance & Throughput)
- **NF-PERF-01 网络传输吞吐量**：在 1000Mbps 千兆局域网环境下，点对点文件上传/下载速度应达到物理极限（>= 110 MB/s）；在 Tailscale P2P 隧道下，传输速率仅取决于 WireGuard 加解密及物理 WAN 上行带宽。
- **NF-PERF-02 视频拖拽响应延迟**：在 HTML5 播放器中点击视频任意时间轴位置，HTTP 206 首帧响应时间（TTFB）须在 **< 200ms** 内（本地 LAN 环境）或 **< 500ms** 内（Tailscale 异地环境）。
- **NF-PERF-03 API 响应时延**：文本随手记的创建、检索、读取等 API 平均响应时间低于 **20ms**。

### 5.2 并发处理与极低内存占用需求 (Concurrency & Low Footprint)
- **NF-MEM-01 内存 Footprint 限制**：Go 后端在静态空闲状态下，RSS 内存占用须 **<= 25 MB**；在大文件流式传输及多路 HTTP 206 视频播放高负载状态下，内存占用须控制在 **<= 60 MB**（禁止将整文件 Buffer 读入 RAM）。
- **NF-MEM-02 单二进制文件体积**：打包压缩后的可执行文件体积 **<= 20 MB**（内部包含完全嵌入的前端 HTML/JS/CSS 静态产物）。
- **NF-CONC-03 并发连接能力**：在 SQLite WAL 模式与 Go 协程池支撑下，系统须支持至少 **200 个并发 HTTP 206 视频流连接**无死锁、无报错崩溃。

### 5.3 存储持久性、一致性与 SQLite 事务需求 (Storage & Durability)
- **NF-STOR-01 数据持久化引擎**：使用轻量级嵌入式 SQLite 3 数据库保存随手记元数据、文件索引、设备节点信息与系统配置。
- **NF-STOR-02 WAL 模式并发优化**：数据库强制启用 `PRAGMA journal_mode=WAL;` 与 `PRAGMA synchronous=NORMAL;`，实现读写分离高并发，防止磁盘慢导致写锁阻塞。
- **NF-STOR-03 磁盘空间防护**：提供磁盘剩余空间安全阈值告警（如少于 2GB 时，自动拒绝接收新的文件上传，防止主宿主机磁盘满崩溃）。

### 5.4 跨平台兼容性与 Web Share Target PWA 需求 (Compatibility & PWA)
- **NF-COMP-01 现代浏览器全面兼容**：完美兼容 Chrome >= 90, Safari >= 14, Edge >= 90, Firefox >= 88, 移动端 iOS Safari 与 Android Chrome。
- **NF-COMP-02 Web Progressive App (PWA)**：支持 Web App Manifest 规范，用户可点击“添加到主屏幕”，以独立原生窗口形态运行；
- **NF-COMP-03 Web Share Target API**：在 Android 设备上安装 PWA 后，系统“分享”菜单中自动出现“OmniShare”图标。用户选择任意文件或文本点击分享，自动唤起网页并将数据预填入上传框。

---

## 6. 领域实体与数据模型分析

为了确保数据层结构的严谨性与未来扩展性，系统核心数据实体与关系设计如下：

```
+------------------+         1 : N         +--------------------+
|   User / Auth    | --------------------> |     QuickNote      |
| (用户/验证凭证)  |                       | (随手记/文本片段)  |
+------------------+                       +--------------------+
         |                                           |
         | 1 : N                                     | 1 : N (可选附件)
         v                                           v
+------------------+                       +--------------------+
|  DeviceNode      |                       |     FileAsset      |
| (网络设备节点)   |                       | (文件/超大视频资产)|
+------------------+                       +--------------------+
                                                     |
                                                     | 1 : 1
                                                     v
                                           +--------------------+
                                           |   VideoStreamMeta  |
                                           | (视频流/Faststart) |
                                           +--------------------+
```

### 6.1 核心实体数据结构定义

#### 1. QuickNote (随手记片段)
```sql
CREATE TABLE IF NOT EXISTS quick_notes (
    id TEXT PRIMARY KEY,                 -- 唯一短 ID (如: n_x8k9a)
    content TEXT NOT NULL,               -- 文本正文 (Markdown/Plaintext)
    content_type TEXT DEFAULT 'text/plain', -- 内容类型 (text/markdown, text/plain, text/code)
    tags TEXT,                           -- 标签 JSON 数组 (如: ["#go", "#tailscale"])
    is_burn_after_read INTEGER DEFAULT 0,-- 是否阅后即焚 (1: 是, 0: 否)
    read_count INTEGER DEFAULT 0,        -- 已累计读取次数
    max_read_count INTEGER DEFAULT 0,    -- 最大允许读取次数 (0 表示无限制)
    expires_at DATETIME,                 -- 定期销毁截止时间 (NULL 表示永久)
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

#### 2. FileAsset (文件资产表)
```sql
CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,                 -- 文件资产 ID (如: f_99z2b)
    file_name TEXT NOT NULL,             -- 原始文件名
    file_size INTEGER NOT NULL,          -- 文件字节大小 (Bytes)
    mime_type TEXT NOT NULL,             -- 媒体类型 (如: video/mp4, image/png, application/zip)
    storage_path TEXT NOT NULL,          -- 物理磁盘存储相对路径
    file_hash TEXT,                      -- SHA-256 哈希校验值
    download_count INTEGER DEFAULT 0,    -- 累积下载次数
    is_video INTEGER DEFAULT 0,          -- 是否为视频文件 (1: 是, 0: 否)
    expires_at DATETIME,                 -- 到期物理删除时间
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

#### 3. VideoStreamMeta (视频流元数据扩展表)
```sql
CREATE TABLE IF NOT EXISTS video_stream_metas (
    file_id TEXT PRIMARY KEY,            -- 关联 file_assets.id
    duration_seconds REAL DEFAULT 0,     -- 视频时长 (秒)
    width INTEGER DEFAULT 0,             -- 视频帧宽度 (px)
    height INTEGER DEFAULT 0,            -- 视频帧高度 (px)
    codec_name TEXT,                     -- 编码格式 (h264, hevc, vp9, av1)
    is_faststart INTEGER DEFAULT 0,      -- MOOV header 是否处于头部 (1: 已优化, 0: 尚未优化)
    thumbnail_path TEXT,                 -- 提取的封面图静态路径
    FOREIGN KEY(file_id) REFERENCES file_assets(id) ON DELETE CASCADE
);
```

#### 4. DeviceNode (网络设备节点表)
```sql
CREATE TABLE IF NOT EXISTS device_nodes (
    id TEXT PRIMARY KEY,                 -- 节点 UUID / Node ID
    hostname TEXT NOT NULL,              -- 主机名 (如: macbook-pro.local)
    ip_addresses TEXT NOT NULL,          -- IP 地址列表 JSON 数组
    network_type TEXT NOT NULL,          -- 网络类型 ('lan', 'tailscale', 'hybrid')
    last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP, -- 最后一次收到心跳的时间
    user_agent TEXT                      -- 设备的 User-Agent 标示
);
```

---

## 7. 部署与运维需求规范

为了让 OmniShare 能够适应从“普通笔记本临时运行”到“家庭 NAS 24小时长期后台运行”的各种场景，系统在设计上必须保证极其简易的运维部署体验。

### 7.1 单二进制文件 Golang 零依赖部署规范
- **CGO_ENABLED=0 静态编译**：采用 pure-Go 实现（使用现代 Go 内置 `modernc.org/sqlite` 或静态编译 SQLite），生成无任何系统 C 库依赖（无 libc/glibc 依赖）的纯纯静态可执行文件。
- **静态资源内嵌 (`//go:embed`)**：Vue 3 前端构建好的 HTML, CSS, JavaScript, 字体及 Web 图标自动打包嵌入 Golang 可执行二进制文件内部。
- **极简命令行参数**：
  ```bash
  # 最简启动指令 (默认监听 0.0.0.0:8080，使用当前目录 omnishare.db)
  ./omnishare

  # 自定义端口与数据存储路径
  ./omnishare --port 9090 --data-dir /var/lib/omnishare --passphrase "my-secret-key"
  ```

### 7.2 Docker 容器化与轻量部署规范
- **Docker 镜像体积规范**：镜像基于 Alpine Linux 或 Scratch 极小空镜像，总体 Docker 镜像体积控制在 **< 30MB**。
- **Docker Compose 配置需求**：
  ```yaml
  version: '3.8'
  services:
    omnishare:
      image: antigravity/omnishare:latest
      container_name: omnishare
      restart: unless-stopped
      network_mode: "host" # 建议 host 模式以支持 Layer-2 mDNS 组播广播
      volumes:
        - ./data:/app/data
      environment:
        - OMNISHARE_PORT=8080
        - OMNISHARE_PASSPHRASE=secret
        - TAILSCALE_ENABLE=true
  ```

### 7.3 Tailscale Serve & Funnel HTTPS 证书自动续签集成规范
- **Tailscale Serve 内置集成**：支持启动参数 `--enable-tailscale`。启动后，后端自动通过本地 Tailscale API 创建 HTTP/HTTPS 监听，自动暴露给 Tailnet 用户，且自动获配匹配你 Tailnet 域名的合法 Let's Encrypt TLS 证书。
- **Tailscale Funnel 选配支持**：当用户开启 `--enable-funnel` 时，安全地将节点公开给公共互联网访问，同时保持内部密码保护状态。

---

## 8. 系统边界、约束条件与演进规划

### 8.1 系统边界与非目标 (Non-Goals)
为了保持系统的轻量与聚焦，以下功能明确界定为**本项目的非目标**：
1. **不做复杂的重型多用户权限分配系统**：系统专注于个人或信任家庭/小团队内部共享，不搞复杂的大企业 RBAC 级多租户隔离；
2. **不做云端转码重构系统**：对于非 HTML5 原生支持的极端格式（如古老的 RMVB、FLV），系统仅提供文件下载，不在服务端消耗大量 CPU 资源进行实时重编码；
3. **不做商业化云存储替代品**：系统侧重于“跨设备协同与快速中转”，不针对 PB 级冷数据备份场景进行设计。

### 8.2 演进路线图 (Evolution Roadmap)

```
+-----------------------------------------------------------------------------------+
|                            OMNISHARE 阶段演进路线图                               |
+-----------------------------------------------------------------------------------+
|  [ Phase 1: MVP 核心研发期 (当前目标) ]                                           |
|  - 实现 Go 后端核心框架与 SQLite 存储                                            |
|  - 实现 mDNS 局域网广播 + Tailscale 基础连通                                     |
|  - 实现 Markdown 随手记 + HTTP 206 视频流播放器                                  |
|  - 实现 单二进制文件打包与 Vue 3 界面                                             |
+-----------------------------------------------------------------------------------+
                                         |
                                         v
|  [ Phase 2: 增强体验与端到端进阶 (3-6 个月) ]                                      |
|  - Web Share Target API Android 深度适配                                          |
|  - 支持 Tus 分片断点续传                                                          |
|  - 网页版剪贴板实时自动同步选项 (Opt-in Clipboard Sync)                            |
+-----------------------------------------------------------------------------------+
                                         |
                                         v
|  [ Phase 3: 生态扩展期 (6-12 个月) ]                                              |
|  - 推出 Chrome / Edge / Firefox 浏览器快捷扩展插件                               |
|  - 推出 CLI 命令行小工具 (`omnishare-cli send my-file.mp4`)                        |
+-----------------------------------------------------------------------------------+
```

---

## 9. 结论与下一步

本《需求设计文档》深刻分析了局域网与 Tailscale 环境下的协同传输痛点，通过与 Memos, LocalSend, PairDrop 等 6 款主流开源工具的 12 维对比，确立了 **OmniShare (Antigravity-Share)** “双引擎适配 + 大视频 HTTP 206 流播 + 单二进制嵌入部署”的技术路线。

**下一步工作**：
根据本需求规范，立即开展阶段三《功能设计文档》(`docs/FUNCTIONAL_DESIGN_SPEC.md`) 的编写，详细给出完整的模块架构图、SQLite 表结构、RESTful API 契约及核心算法伪代码实现。
