# OmniShare (Antigravity-Share) 局域网与 Tailscale 虚拟内网跨设备速记与视频/文件共享平台
# 功能设计文档 (Functional Design Specification)

- **文档版本**：v1.0.0
- **撰写时间**：2026年8月
- **项目代号**：OmniShare / Antigravity-Share
- **密级**：内部开源工程规范
- **目标读者**：架构师、后端研发工程师、前端研发工程师、测试工程师、运维工程师

---

## 目录 (Table of Contents)

1. [系统总体架构与设计原则 (System Overall Architecture & Principles)](#1-系统总体架构与设计原则)
   - 1.1 系统架构图 (Architecture Diagram)
   - 1.2 数据流与交互图 (Data Flow Diagram)
   - 1.3 核心设计原则 (Core Principles)
2. [网络发现与设备状态感知引擎设计 (Network Discovery & Sensing Engine)](#2-网络发现与设备状态感知引擎设计)
   - 2.1 mDNS Layer-2 组播引擎原理与实现方案
   - 2.2 Tailscale Layer-3 MagicDNS & tsnet 适配引擎
   - 2.3 WebSocket 实时长连接心跳保活与节点注册表 (Peer Registry)
   - 2.4 RTT 延时测算与智能路由网络选路算法
3. [极速随手记与剪贴板引擎设计 (Quick Notes Engine Design)](#3-极速随手记与剪贴板引擎设计)
   - 3.1 Markdown 解析与代码高亮引擎
   - 3.2 标签 (Tags) 动态索引与倒排搜索机制
   - 3.3 Curl-Friendly RAW 接口设计
   - 3.4 阅后即焚与 TTL 定时清理后台协程调度器
4. [大文件传输与分片断点续传引擎设计 (Chunked File Transfer Engine)](#4-大文件传输与分片断点续传引擎设计)
   - 4.1 HTML5 Slice + Tus/Chunked 上传状态机
   - 4.2 文件切片 SHA-256 并行哈希校验与去重
   - 4.3 管道化 ZIP 流式打包下载引擎 (Zero-Disk ZIP Streaming)
5. [超大视频 HTTP 206 字节流与 Faststart 引擎设计 (HTTP 206 Range Streaming Engine)](#5-超大视频-http-206-字节流与-faststart-引擎设计)
   - 5.1 RFC 7233 Byte-Range 状态机与 Handler 伪代码设计
   - 5.2 MP4 MOOV Atom 自动前置 (Faststart) 转换引擎原理
   - 5.3 Plyr HTML5 播放器前端控制层与键盘响应机制
   - 5.4 视频封面与微缩图异步提取器 (Thumbnail Generator)
6. [数据库物理设计与并发优化 (Database Design & Performance Tuning)](#6-数据库物理设计与并发优化)
   - 6.1 SQLite 3 DDL 表结构与索引详细定义
   - 6.2 WAL (Write-Ahead Logging) 模式与锁优化配置
   - 6.3 SQL 预编译语句 (Prepared Statements) 与连接池设计
7. [API 规范与 WebSocket 协议契约 (API & WebSocket Contract)](#7-api-规范与-websocket-协议契约)
   - 7.1 RESTful API 契约全景图
   - 7.2 API 详细 Request / Response JSON Schema 规范
   - 7.3 WebSocket 实时双向帧协议 (Frame Protocol) 规范
8. [安全防护、鉴权与防穿越机制设计 (Security & Authentication Design)](#8-安全防护鉴权与防穿越机制设计)
   - 8.1 HMAC-SHA256 签名的 JWT / Cookie 鉴权中间件
   - 8.2 绝对路径安全校验与防目录穿越 (Anti-Path Traversal) 校验器
   - 8.3 IP 网段白名单机制 (Subnet CORS Guard)
9. [打包部署与自动化运维工程设计 (Deployment & Packaging Engineering)](#9-打包部署与自动化运维工程设计)
   - 9.1 Pure-Go 零依赖 CGO_ENABLED=0 编译流程
   - 9.2 前端 Vue 3 + Vite 产物 embed 嵌入方案
   - 9.3 Tailscale Serve & Funnel 命令行自动挂载与 HTTPS 证书配置

---

## 1. 系统总体架构与设计原则

### 1.1 系统架构图 (Architecture Diagram)

OmniShare 采用清晰的分层微内核架构，分为 **UI 表现层 (Presentation Layer)**、**API 与 WebSocket 控制层 (Controller Layer)**、**业务引擎层 (Business Engine Layer)**、**网络双路适配层 (Network Infrastructure Layer)** 以及 **物理存储层 (Storage Layer)**。

```
+---------------------------------------------------------------------------------------------------+
|                                    OmniShare 系统总体架构图                                       |
+---------------------------------------------------------------------------------------------------+

[ UI 表现层 (Modern Web / PWA) ]
+--------------------------------------------------------------------------------------------------+
| - Vue 3 Responsive App     - Pinia State Management      - Plymouth / Native HTML5 Video Player  |
| - Service Worker / PWA     - Web Share Target Handler    - Markdown / Code Highlight Renderer    |
+--------------------------------------------------------------------------------------------------+
                                                |  (HTTP REST / Range / WebSocket)
                                                v
[ HTTP & WebSocket 控制层 (Controller Layer) ]
+--------------------------------------------------------------------------------------------------+
| - Gin / Chi HTTP Router    - JWT / Auth Middleware       - WebSocket Upgrade Handler             |
| - Static Asset Handler     - Anti-Traversal Guard        - Subnet CORS Middleware                |
+--------------------------------------------------------------------------------------------------+
                                                |
                                                v
[ 业务引擎层 (Business Engine Layer) ]
+------------------------------------+ +-----------------------------------+ +---------------------+
|      随手记引擎 (Note Engine)      | |    文件分片引擎 (Transfer Engine) | | 视频 206 流播引擎 |
| - Markdown / Tag Inverted Index    | | - Tus / Chunked Upload Handler    | | - Byte-Range Handler|
| - TTL / Burn Cleanup Cron Scheduler| | - Zero-Disk Streaming ZIPer     | | - MOOV Atom Parser|
+------------------------------------+ +-----------------------------------+ +---------------------+
                                                |
                                                v
[ 网络双路适配层 (Network Infrastructure Layer) ]
+---------------------------------------------------+ +--------------------------------------------+
|            LAN mDNS 广播发现引擎                  | |        Tailscale MagicDNS / tsnet 适配器   |
| - UDP 5353 Multicast (Zeroconf / Bonjour)         | | - Layer-3 Tailnet Address Resolution     |
| - Active Peer Heartbeat & Peer Registry Table     | | - Tailscale Serve HTTPS Auto Certificate |
+---------------------------------------------------+ +--------------------------------------------+
                                                |
                                                v
[ 存储与持久层 (Physical Storage Layer) ]
+---------------------------------------------------+ +--------------------------------------------+
|             SQLite 3 数据库 (WAL Mode)            | |              本地物理磁盘存储              |
| - quick_notes / file_assets / device_nodes        | | - uploads/ (分片与文件实体)                 |
| - PRAGMA journal_mode=WAL; PRAGMA sync=NORMAL;    | | - thumbnails/ (视频静态缩略图)             |
+---------------------------------------------------+ +--------------------------------------------+
```

---

### 1.2 数据流与交互图 (Data Flow Diagram)

下图展示了从用户上传视频到移动端接收并流畅观看的全链路数据流：

```
[发送设备 (MacBook)]          [OmniShare Go 后端]          [SQLite 数据库]        [接收设备 (iPhone/Tailnet)]
       |                              |                            |                           |
       |--- 1. POST /api/v1/files --->|                            |                           |
       |    (分片上传 4K 视频)        |                            |                           |
       |                              |--- 2. 写入磁盘 & 哈希校验->|                           |
       |                              |--- 3. 异步 MOOV 检查/重排->|                           |
       |                              |--- 4. 插入 file_assets --->|                           |
       |                              |                            |                           |
       |<-- 5. 返回 201 Created (ID) -|                            |                           |
       |                              |                                                        |
       |--- 6. WebSocket 广播: "file_created" 消息 (含 File ID) ----------------------------->|
       |                              |                                                        |
       |                              |<-- 7. GET /api/v1/files/f_123/stream (Range: bytes=0-)---|
       |                              |                                                        |
       |                              |--- 8. 解析 Range, 返回 206 Partial (Content-Range) --->|
       |                              |    (在线秒播视频)                                      |
```

---

### 1.3 核心设计原则 (Core Principles)
1. **Zero-Copy / Streaming First (零拷贝流式优先)**：对于文件下载与视频播放，拒绝在 RAM 中构建完整文件 Buffer；全程使用 Go `io.Copy` 或 `http.ServeContent`，内存占用控制在 64KB 管道 Buffer 水平。
2. **Crash-Safe Persistence (崩溃安全存储)**：SQLite 开启 WAL (Write-Ahead Logging) 模式；写文件采用原子写入规范（先写入 `.tmp` 临时文件，校验成功后执行 `os.Rename` 重命名）。
3. **Decoupled Discovery (解耦式发现机制)**：局域网 mDNS 与 Tailscale 引擎相互独立，统一注册到全局内存 `PeerRegistry` 集中管理。

---

## 2. 网络发现与设备状态感知引擎设计

### 2.1 mDNS Layer-2 组播引擎原理与实现方案

#### 2.1.1 协议规范与结构
OmniShare 后端集成 Pure-Go 实现的 `grandcat/zeroconf` 库，注册 DNS-SD 服务：
- **Service Name**：`_omnishare._tcp`
- **Domain**：`local.`
- **Port**：默认 `8080`
- **TXT Records (附加元数据)**：
  - `id`: 设备唯一 UUID（如 `node-550e8400-e29b-41d4-a716-446655440000`）
  - `name`: 设备友好名称（如 `MacBook-Pro-Work`）
  - `ver`: 软件版本号（如 `1.0.0`）

#### 2.1.2 mDNS 服务监听与注册协程 (Go 伪代码设计)
```go
package discovery

import (
	"context"
	"log"
	"time"

	"github.com/grandcat/zeroconf"
)

type MDNSDiscovery struct {
	server *zeroconf.Server
	nodeID string
	name   string
	port   int
}

func NewMDNSDiscovery(nodeID, name string, port int) *MDNSDiscovery {
	return &MDNSDiscovery{nodeID: nodeID, name: name, port: port}
}

func (m *MDNSDiscovery) StartRegister(ctx context.Context) error {
	txtRecords := []string{
		"id=" + m.nodeID,
		"name=" + m.name,
		"ver=1.0.0",
	}
	server, err := zeroconf.Register(
		m.name,
		"_omnishare._tcp",
		"local.",
		m.port,
		txtRecords,
		nil,
	)
	if err != nil {
		return err
	}
	m.server = server
	log.Printf("[mDNS] Registered service _omnishare._tcp on port %d", m.port)
	return nil
}

func (m *MDNSDiscovery) StartBrowse(ctx context.Context, registry *PeerRegistry) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Printf("[mDNS] Failed to create resolver: %v", err)
		return
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		for entry := range entries {
			for _, ip := range entry.AddrIPv4 {
				nodeID := parseTXT(entry.Text, "id")
				nodeName := parseTXT(entry.Text, "name")
				if nodeID != "" && nodeID != m.nodeID {
					registry.UpsertPeer(PeerInfo{
						ID:          nodeID,
						Hostname:    nodeName,
						IP:          ip.String(),
						Port:        entry.Port,
						NetworkType: "lan",
						LastSeen:    time.Now(),
					})
				}
			}
		}
	}()

	err = resolver.Browse(ctx, "_omnishare._tcp", "local.", entries)
	if err != nil {
		log.Printf("[mDNS] Failed to browse: %v", err)
	}
}

func parseTXT(records []string, key string) string {
	prefix := key + "="
	for _, r := range records {
		if len(r) > len(prefix) && r[:len(prefix)] == prefix {
			return r[len(prefix):]
		}
	}
	return ""
}
```

---

### 2.2 Tailscale Layer-3 MagicDNS & tsnet 适配引擎

#### 2.2.1 运行机制
当命令行指定 `--enable-tailscale` 或检测到 Tailscale 环境变量时，OmniShare 后端启动 `tsnet.Server`：
1. 自动与 Tailscale 本地 Daemon 对话（或使用 AuthKey 直接嵌入 WireGuard 用户态协议栈）；
2. 获取本机在 Tailnet 中的 CGNAT IPv4（`100.x.y.z`）及 FQDN 域名（`node.tailnet-name.ts.net`）；
3. 通过 `tsnet` 自动拉取 Let's Encrypt 域名 HTTPS 证书。

```
+-------------------------------------------------------------------+
|                        tsnet 适配引擎数据流                       |
+-------------------------------------------------------------------+
  [Tailnet Overlay] ---> tsnet.Server (Pure-Go WireGuard Stack)
                              |
                              +---> 获取 100.x.y.z IP & MagicDNS 名字
                              |
                              +---> 自动配置 HTTPS TLS 证书 Listener
                              |
                              v
                   注册至 PeerRegistry (Type="tailscale")
```

---

### 2.3 WebSocket 实时长连接心跳保活与节点注册表 (Peer Registry)

全局内存维护一个线程安全的 `PeerRegistry` 表，包含所有在线节点。

#### 2.3.1 内存节点结构与淘汰策略
```go
package discovery

import (
	"sync"
	"time"
)

type PeerInfo struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	NetworkType string    `json:"network_type"` // "lan" 或 "tailscale"
	RTTMs       int64     `json:"rtt_ms"`
	LastSeen    time.Time `json:"last_seen"`
}

type PeerRegistry struct {
	mu    sync.RWMutex
	peers map[string]PeerInfo
}

func NewPeerRegistry() *PeerRegistry {
	pr := &PeerRegistry{peers: make(map[string]PeerInfo)}
	go pr.startEvictionLoop(30 * time.Second)
	return pr
}

func (pr *PeerRegistry) UpsertPeer(info PeerInfo) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.peers[info.ID] = info
}

func (pr *PeerRegistry) ListPeers() []PeerInfo {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	list := make([]PeerInfo, 0, len(pr.peers))
	for _, p := range pr.peers {
		list = append(list, p)
	}
	return list
}

func (pr *PeerRegistry) startEvictionLoop(timeout time.Duration) {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		pr.mu.Lock()
		now := time.Now()
		for id, peer := range pr.peers {
			if now.Sub(peer.LastSeen) > timeout {
				delete(pr.peers, id) // 超时自动下线淘汰
			}
		}
		pr.mu.Unlock()
	}
}
```

---

### 2.4 RTT 延时测算与智能路由网络选路算法

当某节点同时存在 LAN IP（如 `192.168.1.50`）与 Tailscale IP（如 `100.115.82.43`）时，系统前端/后端按照如下选路优先级公式计算得分：

$$\text{Score} = \text{NetworkWeight} \times 1000 - \text{RTT (ms)}$$

其中：
- `NetworkWeight(LAN)` = $2.0$
- `NetworkWeight(Tailscale)` = $1.0$

由于 LAN 通常拥有 1ms~5ms 延时及千兆物理带宽，得分配比远高于 Tailscale 异地 WAN，因此系统优先尝试通过 LAN IP 直连传输；当 LAN 连通性中断时，得分自动调整并平滑回退至 Tailscale IP。

---

## 3. 极速随手记与剪贴板引擎设计

### 3.1 Markdown 解析与代码高亮引擎
- 前端使用 `marked.js` + `highlight.js` 实现 Markdown 实时渲染与语法高亮；
- 后端在存入数据库时，使用正则表达式解析内容中的 `#标签名`，并自动构建 JSON 标签数组。

### 3.2 标签 (Tags) 动态索引与倒排搜索机制
为了在 SQLite 中高效搜索标签，除了提取 JSON 标签存储在 `quick_notes.tags` 字段中外，在创建随手记时同步更新倒排索引辅助视图或直接使用 SQLite FTS5 (Full-Text Search) 模块：

```sql
-- 创建 FTS5 全文检索虚表
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
    note_id UNINDEXED,
    content,
    tags
);
```

---

### 3.3 Curl-Friendly RAW 接口设计
在后端控制层，特别设计一个专用的 RAW 访问 Handler：
当客户端请求 `GET /n/:id/raw` 时，系统剥离所有 HTML/JS 包装，直接输出原始字节：

```go
func HandleNoteRaw(c *gin.Context) {
    id := c.Param("id")
    note, err := store.GetNoteByID(id)
    if err != nil {
        c.String(404, "Note not found")
        return
    }
    // 增加计数与阅后即焚检查
    go store.IncrementNoteReadCount(id)

    c.Header("Content-Type", "text/plain; charset=utf-8")
    c.String(200, note.Content)
}
```

如此一来，终端用户可以直接运行：
```bash
curl -s http://192.168.1.10:8080/n/x8k9a/raw | sh
```

---

### 3.4 阅后即焚与 TTL 定时清理后台协程调度器

启动后台定时任务协程（Cron Worker），定期清理过期的随手记与文件：

```go
func StartCleanupScheduler(db *sql.DB, dataDir string) {
    ticker := time.NewTicker(1 * time.Minute)
    go func() {
        for range ticker.C {
            // 1. 删除已过期的随手记
            db.Exec("DELETE FROM quick_notes WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP")

            // 2. 删除阅后即焚达到上限的随手记
            db.Exec("DELETE FROM quick_notes WHERE max_read_count > 0 AND read_count >= max_read_count")

            // 3. 查出过期的 file_assets 并清除物理磁盘文件
            rows, _ := db.Query("SELECT id, storage_path FROM file_assets WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP")
            for rows.Next() {
                var id, path string
                rows.Scan(&id, &path)
                os.Remove(filepath.Join(dataDir, path))
                db.Exec("DELETE FROM file_assets WHERE id = ?", id)
            }
            rows.Close()
        }
    }()
}
```

---

## 4. 大文件传输与分片断点续传引擎设计

### 4.1 HTML5 Slice + Tus/Chunked 上传状态机

```
[客户端前端]                                                [服务端 Storage Engine]
     |                                                               |
     |--- 1. POST /api/v1/files/upload/init (文件名, 大小, SHA256) --->|
     |                                                               |--- 创建 .tmp 分片元数据
     |<-- 2. 返回 201 Created (UploadToken, ReceivedChunks: []) ------|
     |                                                               |
     |--- 3. PUT /api/v1/files/upload/chunk (Token, ChunkIndex=0) --->|
     |                                                               |--- 写入 offset 0..5MB
     |<-- 4. 返回 200 OK (ChunkAck) ---------------------------------|
     |                                                               |
     |  (中途网络断开重连...)                                         |
     |                                                               |
     |--- 5. GET /api/v1/files/upload/status?token=XXX ------------->|
     |<-- 6. 返回 { ReceivedChunks: [0] } (秒获断点偏移) -----------|
     |                                                               |
     |--- 7. PUT /api/v1/files/upload/chunk (Token, ChunkIndex=1) --->|
     |                                                               |
     |--- 8. POST /api/v1/files/upload/complete (Token) ------------>|
     |                                                               |--- 校验总 Hash & 重命名
     |<-- 9. 返回 200 OK (FileAsset JSON) ---------------------------|
```

---

### 4.2 文件切片 SHA-256 并行哈希校验与去重
前端上传时，使用 `Web Workers` 线程并行计算每个 5MB Chunk 的 SHA-256 哈希值；后端收到切片后即刻比对校验。若系统中已存在相同 SHA-256 哈希的文件，自动触发**秒传 (Instant Duplicate Deduplication)** 机制，直接增加引用计数，避免重复磁盘开销。

### 4.3 管道化 ZIP 流式打包下载引擎 (Zero-Disk ZIP Streaming)
当用户选择多个文件进行批量打包下载时，传统做法会在服务器生成一个临时 `bundle.zip` 大文件，容易瞬间撑爆磁盘。
OmniShare 采用 **Zero-Disk Piping ZIP Streaming** 机制，基于 Go `archive/zip` 包装 `http.ResponseWriter`：

```go
func HandleBatchDownloadZip(c *gin.Context, filePaths []string) {
    c.Header("Content-Disposition", "attachment; filename=\"omnishare_files.zip\"")
    c.Header("Content-Type", "application/zip")

    zipWriter := zip.NewWriter(c.Writer)
    defer zipWriter.Close()

    for _, path := range filePaths {
        file, err := os.Open(path)
        if err != nil {
            continue
        }
        w, err := zipWriter.Create(filepath.Base(path))
        if err != nil {
            file.Close()
            continue
        }
        io.Copy(w, file) // 直接从磁盘流式 piping 到 HTTP 输出管道！
        file.Close()
    }
}
```

---

## 5. 超大视频 HTTP 206 字节流与 Faststart 引擎设计

### 5.1 RFC 7233 Byte-Range 状态机与 Handler 伪代码设计

HTTP 206 Byte-Range 是实现大视频无卡顿秒播的核心。

```go
func ServeVideoStream(c *gin.Context, filePath string) {
    file, err := os.Open(filePath)
    if err != nil {
        c.String(404, "Video File Not Found")
        return
    }
    defer file.Close()

    stat, err := file.Stat()
    if err != nil {
        c.String(500, "Internal Error")
        return
    }

    fileSize := stat.Size()
    rangeHeader := c.GetHeader("Range")

    // 若无 Range Header，回退至普通全量 ServeContent (200 OK)
    if rangeHeader == "" {
        c.Header("Content-Length", fmt.Sprintf("%d", fileSize))
        c.Header("Content-Type", "video/mp4")
        c.Header("Accept-Ranges", "bytes")
        http.ServeContent(c.Writer, c.Request, stat.Name(), stat.ModTime(), file)
        return
    }

    // 解析 Range: bytes=start-end
    ranges, err := parseRangeHeader(rangeHeader, fileSize)
    if err != nil || len(ranges) == 0 {
        c.Header("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
        c.Status(http.StatusRequestedRangeNotSatisfiable) // 416
        return
    }

    r := ranges[0]
    contentLength := r.End - r.Start + 1

    c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, fileSize))
    c.Header("Accept-Ranges", "bytes")
    c.Header("Content-Length", fmt.Sprintf("%d", contentLength))
    c.Header("Content-Type", "video/mp4")
    c.Status(http.StatusPartialContent) // 206 Partial Content

    file.Seek(r.Start, io.SeekStart)
    io.CopyN(c.Writer, file, contentLength) // 零缓冲管道传输
}
```

---

### 5.2 MP4 MOOV Atom 自动前置 (Faststart) 转换引擎原理

MP4 文件由各种 Box (Atom) 组成。关键 Box 包括：
- `ftyp`：File Type Atom（文件类型标示）；
- `mdat`：Media Data Atom（真正的音视频压缩原始数据）；
- `moov`：Movie Atom（包含时间轴、帧偏移量索引表、采样点分布）。

```
[未经 Faststart 优化的 MP4 文件结构 (拖拽卡顿)]
+--------+------------------------------------------------+--------+
| ftyp   | mdat (包含数 GB 媒体原始数据)                  | moov   |
+--------+------------------------------------------------+--------+
 0 KB                                                      文件末尾 (必须加载完整个文件才能解析播放!)

[经过 Faststart 优化重排后的 MP4 文件结构 (秒开拖拽)]
+--------+--------+------------------------------------------------+
| ftyp   | moov   | mdat (包含数 GB 媒体原始数据)                  |
+--------+--------+------------------------------------------------+
 0 KB     头部索引 (浏览器打开只需读取前 64KB 即可瞬间开始拖拽播放!)
```

#### Faststart 转换管道
上传成功后，检测 MP4 `moov` 位置。若在 `mdat` 之后，后台自动触发 Go 原生 MP4 结构整理算法：
1. 提取 `moov` atom 数据块并重算 `stco` / `co64` 块偏移表 (Chunk Offset Table) 增量值；
2. 将 `moov` 写入临时文件 `ftyp` 之后，紧接着拼入 `mdat` 数据；
3. 完成原子重命名替代。

---

### 5.3 Plyr HTML5 播放器前端控制层与键盘响应机制

前端在 Vue 3 中封装 `VideoPlayer.vue` 组件，集成 Plyr.js：

```html
<template>
  <div class="video-container">
    <video ref="videoRef" class="plyr-player" controls playsinline>
      <source :src="streamUrl" type="video/mp4" />
    </video>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue';
import Plyr from 'plyr';
import 'plyr/dist/plyr.css';

const props = defineProps({ streamUrl: String });
const videoRef = ref(null);
let player = null;

onMounted(() => {
  player = new Plyr(videoRef.value, {
    controls: ['play-large', 'play', 'progress', 'current-time', 'mute', 'volume', 'captions', 'settings', 'pip', 'airplay', 'fullscreen'],
    seekTime: 5,
    speed: { selected: 1, options: [0.5, 0.75, 1, 1.25, 1.5, 2] }
  });
});

onBeforeUnmount(() => {
  if (player) player.destroy();
});
</script>
```

---

## 6. 数据库物理设计与并发优化

### 6.1 SQLite 3 DDL 表结构与索引详细定义

全套 SQLite DDL 语句如下：

```sql
-- 1. 随手记主表
CREATE TABLE IF NOT EXISTS quick_notes (
    id TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    content_type TEXT DEFAULT 'text/plain',
    tags TEXT,
    is_burn_after_read INTEGER DEFAULT 0,
    read_count INTEGER DEFAULT 0,
    max_read_count INTEGER DEFAULT 0,
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_notes_expires ON quick_notes(expires_at);
CREATE INDEX IF NOT EXISTS idx_notes_created ON quick_notes(created_at DESC);

-- 2. 文件资产表
CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,
    file_name TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    mime_type TEXT NOT NULL,
    storage_path TEXT NOT NULL,
    file_hash TEXT,
    download_count INTEGER DEFAULT 0,
    is_video INTEGER DEFAULT 0,
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_hash ON file_assets(file_hash);
CREATE INDEX IF NOT EXISTS idx_files_created ON file_assets(created_at DESC);

-- 3. 视频元数据扩展表
CREATE TABLE IF NOT EXISTS video_stream_metas (
    file_id TEXT PRIMARY KEY,
    duration_seconds REAL DEFAULT 0,
    width INTEGER DEFAULT 0,
    height INTEGER DEFAULT 0,
    codec_name TEXT,
    is_faststart INTEGER DEFAULT 0,
    thumbnail_path TEXT,
    FOREIGN KEY(file_id) REFERENCES file_assets(id) ON DELETE CASCADE
);

-- 4. 设备节点保活表
CREATE TABLE IF NOT EXISTS device_nodes (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    ip_addresses TEXT NOT NULL,
    network_type TEXT NOT NULL,
    last_seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    user_agent TEXT
);
```

---

### 6.2 WAL (Write-Ahead Logging) 模式与锁优化配置

Go 后端在初始化 SQLite 数据库时，执行以下 PRAGMA 优化参数以保障并发性能：

```go
func InitDatabase(dbPath string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
    if err != nil {
        return nil, err
    }

    pragmas := []string{
        "PRAGMA journal_mode = WAL;",       // 启用 WAL 模式，允许并发读写
        "PRAGMA synchronous = NORMAL;",     // 提升磁盘写入吞吐
        "PRAGMA foreign_keys = ON;",        // 启用外键约束
        "PRAGMA temp_store = MEMORY;",      // 临时表与索引存入内存
    }

    for _, p := range pragmas {
        if _, err := db.Exec(p); err != nil {
            return nil, err
        }
    }

    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(1 * time.Hour)

    return db, nil
}
```

---

## 7. API 规范与 WebSocket 协议契约

### 7.1 RESTful API 契约全景图

| HTTP 方法 | 路径 (Path) | 功能描述 | 鉴权要求 |
| :--- | :--- | :--- | :--- |
| `POST` | `/api/v1/auth/login` | 口令登录获取 JWT Token | 公开 |
| `GET` | `/api/v1/notes` | 分页查询随手记列表 | Token / PIN |
| `POST` | `/api/v1/notes` | 创建一条新随手记 | Token / PIN |
| `GET` | `/n/:id/raw` | Curl 友好型纯文本 Raw 访问 | 公开 (受 Burn 约束) |
| `DELETE`| `/api/v1/notes/:id` | 删除特定随手记 | Token / PIN |
| `GET` | `/api/v1/files` | 查询上传文件列表 | Token / PIN |
| `POST` | `/api/v1/files/upload` | 上传单文件/分片数据 | Token / PIN |
| `GET` | `/api/v1/files/:id/download`| 下载物理文件 | Token / PIN |
| `GET` | `/api/v1/files/:id/stream` | HTTP 206 视频流式点播 | Token / PIN |
| `GET` | `/api/v1/devices` | 获取在线设备节点列表 | Token / PIN |
| `GET` | `/ws` | WebSocket 实时双向帧长连接 | Token / Query Token |

---

### 7.2 API 详细 Request / Response JSON Schema 规范

#### 1. 创建随手记 Request / Response
`POST /api/v1/notes`

**Request Payload**:
```json
{
  "content": "修改 Tailscale 配置参数: `tailscale up --accept-routes=true` #tailscale #devops",
  "is_burn_after_read": false,
  "max_read_count": 0,
  "ttl_seconds": 86400
}
```

**Response Payload (201 Created)**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "id": "n_k9x2a",
    "content": "修改 Tailscale 配置参数: `tailscale up --accept-routes=true` #tailscale #devops",
    "content_type": "text/markdown",
    "tags": ["#tailscale", "#devops"],
    "is_burn_after_read": false,
    "read_count": 0,
    "expires_at": "2026-08-02T15:30:00Z",
    "raw_url": "http://192.168.1.10:8080/n/n_k9x2a/raw",
    "created_at": "2026-08-01T15:30:00Z"
  }
}
```

---

### 7.3 WebSocket 实时双向帧协议 (Frame Protocol) 规范

WebSocket 连接地址：`ws://<host>:8080/ws?token=XXX`

#### 帧消息格式 (JSON Frame)
```json
{
  "event": "event_type_name",
  "payload": {}
}
```

#### 事件类型列表
1. **`peer_update` (服务端推送)**：在线节点变动更新。
   ```json
   {
     "event": "peer_update",
     "payload": {
       "peers": [
         { "id": "node-1", "hostname": "MacBook-Pro", "ip": "192.168.1.105", "network_type": "lan" },
         { "id": "node-2", "hostname": "Unraid-NAS", "ip": "100.115.82.43", "network_type": "tailscale" }
       ]
     }
   }
   ```
2. **`note_created` (服务端推送)**：有其他节点发布了新的随手记，前端自动弹 Toast 刷新列表。
3. **`airdrop_push` (端到端投送)**：节点 A 向节点 B 发起定向文件/文本投送。

---

## 8. 安全防护、鉴权与防穿越机制设计

### 8.1 HMAC-SHA256 签名的 JWT / Cookie 鉴权中间件
后端的 Auth 中间件生成并校验 JWT Token（密钥在初始化时由 Go 自动生成 32 字节随机盐或衍生自用户设置的 Passphrase）：

```go
func AuthMiddleware(secretKey []byte) gin.HandlerFunc {
    return func(c *gin.Context) {
        tokenString := c.GetHeader("Authorization")
        if tokenString == "" {
            tokenString, _ = c.Cookie("omnishare_token")
        }
        if tokenString == "" {
            c.JSON(401, gin.H{"error": "Unauthorized"})
            c.Abort()
            return
        }

        claims, err := parseJWT(tokenString, secretKey)
        if err != nil {
            c.JSON(401, gin.H{"error": "Invalid Token"})
            c.Abort()
            return
        }
        c.Set("user_id", claims.UserID)
        c.Next()
    }
}
```

---

### 8.2 绝对路径安全校验与防目录穿越 (Anti-Path Traversal) 校验器

为防止恶意客户端通过相对路径请求读取系统敏感文件（如 `GET /api/v1/files/download?path=../../../../etc/passwd`），系统构建防目录穿越校验器：

```go
func SafePathCombine(baseDir, reqPath string) (string, error) {
    cleanBase := filepath.Clean(baseDir)
    targetPath := filepath.Clean(filepath.Join(baseDir, reqPath))

    // 强制判断目标路径的前缀是否完全等于 BaseDir
    if !strings.HasPrefix(targetPath, cleanBase+string(filepath.Separator)) && targetPath != cleanBase {
        return "", fmt.Errorf("security alert: path traversal attempt detected: %s", reqPath)
    }
    return targetPath, nil
}
```

---

## 9. 打包部署与自动化运维工程设计

### 9.1 Pure-Go 零依赖 CGO_ENABLED=0 编译流程
在 Go 构建脚本中强制禁用 CGO，使用纯 Go 的 SQLite 驱动实现完全零 C 动态库依赖：

```bash
# 环境变量配置与交叉编译指令
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/omnishare-linux-amd64 ./cmd/omnishare
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/omnishare-windows-amd64.exe ./cmd/omnishare
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/omnishare-darwin-arm64 ./cmd/omnishare
```

---

### 9.2 前端 Vue 3 + Vite 产物 embed 嵌入方案

在 Go 项目结构中，定义 `embed.go` 文件：

```go
package frontend

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist/*
var embedFrontend embed.FS

func GetFileSystem() http.FileSystem {
	sub, err := fs.Sub(embedFrontend, "dist")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}
```

在 REST 路由中直接注册静态根节点：
```go
router.NoRoute(gin.WrapH(http.FileServer(frontend.GetFileSystem())))
```

这样即实现了**整个前端 UI 完全打包入 Golang 单二进制文件中**的目标！

---

## 10. 结论与交付总结

本《功能设计文档》从架构图、网络发现双引擎、HTTP 206 视频秒播算法、SQLite WAL 并发优化到完整的 API 契约与防目录穿越安全中间件，给出了 360 度覆盖的技术落地细节。

**下一阶段**：
立即进入阶段四《交互设计与评审文档》(`docs/INTERACTION_SPEC.md`) 的生成与评审签署，随后正式启动代码落地开发。
