# OmniShare 数据架构与 SQLite 存储引擎规范 (v2.0)

> **责任人**：户部（数据与资产架构师 / Data & Context）  
> **对接对象**：Chief、研发工程师（工部）、测试工程师（刑部）  
> **适用版本**：OmniShare v1.3.0+ / v2.0 GA  
> **关联代码**：`backend/internal/model/model.go`、`backend/internal/storage/json.go`、`backend/internal/storage/schema.sql`

---

## 一、背景与设计目标

### 1. 现状与架构痛点
在 OmniShare v1.3.0-rc1 之前，系统元数据采用单个 `omnishare.json` 文件进行持久化。每次写操作均遵循：
$$\text{深复制内存状态} \rightarrow \text{修改候选状态} \rightarrow \text{全量序列化 JSON} \rightarrow \text{原子落盘 (durable.WriteFile)}$$
- **$O(N)$ 写放大**：数据量达到数万条随手记或文件时，一次点赞/读取更新都需要全量序列化数 MB 的 JSON。
- **全局写锁互斥**：读写锁颗粒度为整个 Store，无法利用行级/表级事务并发。
- **搜索线性扫描**：关键词与标签检索依赖内存线性扫描，缺少倒排与二级索引。

### 2. 演进目标
1. **无损双向兼容**：SQLite 表结构必须能 100% 无损还原当前的 `state` (Schema Version = 2) JSON 结构，并支持与旧版数据的互转。
2. **审计链防篡改连续性**：严格保持 `AuditEvent` 的 Merkle 链式哈希一致性，杜绝空值/反序列化导致的哈希计算偏差。
3. **时间戳精度固化**：所有时间戳严格遵循 RFC3339 格式，统一为 UTC 时区并固化为**毫秒精度（3位小数）**。
4. **事务与高并发就绪**：启用 SQLite WAL (Write-Ahead Logging) 模式，单写多读无锁并发。

---

## 二、数据实体与 SQLite 表结构设计 (DDL)

DDL 脚本已固化至 `backend/internal/storage/schema.sql`，核心定义与索引设计如下：

### 1. 元数据配置表 (`schema_meta`)
用于跟踪数据库架构版本、存储格式及时间规范。
```sql
CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('schema_version', '2');
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('format', 'sqlite_wal');
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('timestamp_precision', 'rfc3339_milli');
```

### 2. 随手记主表 (`quick_notes`)
对齐 Go 结构体 `model.QuickNote`。
```sql
CREATE TABLE IF NOT EXISTS quick_notes (
    id TEXT PRIMARY KEY,                             -- 格式: n_<milli>_<hex12>
    content TEXT NOT NULL DEFAULT '',                -- 正文
    content_redacted INTEGER NOT NULL DEFAULT 0,     -- 是否安全脱敏 (0/1)
    content_type TEXT NOT NULL DEFAULT 'text/plain', -- MIME 类型
    tags TEXT NOT NULL DEFAULT '[]',                 -- JSON 字符串数组，例: ["#tag1", "tag2"]
    pinned INTEGER NOT NULL DEFAULT 0,               -- 置顶状态 (0/1)
    is_burn_after_read INTEGER NOT NULL DEFAULT 0,   -- 阅后即焚 (0/1)
    read_count INTEGER NOT NULL DEFAULT 0,           -- 已读次数
    max_read_count INTEGER NOT NULL DEFAULT 0,       -- 最大可读限制
    expires_at TEXT,                                 -- 过期时间 (RFC3339 毫秒，可为 NULL)
    deleted_at TEXT,                                 -- 软删除时间 (进入回收站，可为 NULL)
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 毫秒)
    updated_at TEXT NOT NULL                         -- 更新时间 (RFC3339 毫秒)
);

-- 索引覆盖：列表查询过滤已删除/已过期，并按置顶与更新时间倒排
CREATE INDEX IF NOT EXISTS idx_notes_active_lookup ON quick_notes(deleted_at, expires_at, pinned, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notes_deleted_at ON quick_notes(deleted_at);
CREATE INDEX IF NOT EXISTS idx_notes_expires_at ON quick_notes(expires_at);
```

### 3. 文件资产表 (`file_assets`)
对齐 Go 结构体 `model.FileAsset`。
```sql
CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,                             -- 格式: f_<milli>_<hex12>
    file_name TEXT NOT NULL,                         -- 原始文件名
    file_size INTEGER NOT NULL,                      -- 字节大小
    mime_type TEXT NOT NULL,                         -- MIME 类型
    storage_path TEXT NOT NULL DEFAULT '',           -- 磁盘相对路径 (uploads/blobs/...)
    file_hash TEXT NOT NULL DEFAULT '',              -- SHA-256 哈希值
    download_count INTEGER NOT NULL DEFAULT 0,       -- 下载次数
    is_video INTEGER NOT NULL DEFAULT 0,             -- 视频标识 (0/1)
    download_url TEXT NOT NULL DEFAULT '',           -- 下载 API 地址
    stream_url TEXT NOT NULL DEFAULT '',             -- 流播放 API 地址
    expires_at TEXT,                                 -- 过期时间 (可为 NULL)
    deleted_at TEXT,                                 -- 软删除时间 (可为 NULL)
    created_at TEXT NOT NULL                         -- 创建时间 (RFC3339 毫秒)
);

-- 索引覆盖：秒传查重(Hash+Size)、Blob引用探测与回收站
CREATE INDEX IF NOT EXISTS idx_files_hash_size ON file_assets(file_hash, file_size, deleted_at);
CREATE INDEX IF NOT EXISTS idx_files_storage_path ON file_assets(storage_path);
CREATE INDEX IF NOT EXISTS idx_files_deleted_at ON file_assets(deleted_at);
CREATE INDEX IF NOT EXISTS idx_files_expires_at ON file_assets(expires_at);
CREATE INDEX IF NOT EXISTS idx_files_created_at ON file_assets(created_at DESC);
```

### 4. 协同文档便签表 (`pad_documents`)
对齐 Go 结构体 `model.PadDocument`。
```sql
CREATE TABLE IF NOT EXISTS pad_documents (
    id TEXT PRIMARY KEY,                             -- 格式: pad_<milli>_<hex12>
    title TEXT NOT NULL DEFAULT '',                  -- 文档标题
    content TEXT NOT NULL DEFAULT '',                -- 文档内容
    version INTEGER NOT NULL DEFAULT 1,              -- 乐观并发控制版本号
    deleted_at TEXT,                                 -- 软删除时间 (可为 NULL)
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 毫秒)
    updated_at TEXT NOT NULL                         -- 更新时间 (RFC3339 毫秒)
);

CREATE INDEX IF NOT EXISTS idx_pads_deleted_at ON pad_documents(deleted_at);
CREATE INDEX IF NOT EXISTS idx_pads_updated_at ON pad_documents(updated_at DESC);
```

### 5. 分享链接表 (`share_links`)
对齐 Go 结构体 `model.ShareLink`。
```sql
CREATE TABLE IF NOT EXISTS share_links (
    id TEXT PRIMARY KEY,                             -- 格式: shr_<milli>_<hex12>
    token TEXT NOT NULL UNIQUE,                      -- 访问鉴权随机 Token
    object_type TEXT NOT NULL,                       -- 目标类型: 'note' | 'file' | 'pad'
    object_id TEXT NOT NULL,                         -- 目标 ID
    name TEXT NOT NULL DEFAULT '',                   -- 别名展示
    url TEXT NOT NULL DEFAULT '',                    -- 外链 URL
    access_count INTEGER NOT NULL DEFAULT 0,         -- 已用访问数
    max_access_count INTEGER NOT NULL DEFAULT 0,     -- 访问上限
    expires_at TEXT,                                 -- 链接过期时间 (可为 NULL)
    revoked_at TEXT,                                 -- 手工/联动撤销时间 (可为 NULL)
    last_accessed_at TEXT,                           -- 最后访问时间 (可为 NULL)
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 毫秒)
    status TEXT NOT NULL DEFAULT 'active'            -- active/revoked/expired/exhausted
);

CREATE INDEX IF NOT EXISTS idx_shares_token ON share_links(token);
CREATE INDEX IF NOT EXISTS idx_shares_target ON share_links(object_type, object_id, revoked_at);
CREATE INDEX IF NOT EXISTS idx_shares_status ON share_links(status);
```

### 6. 审计事件链表 (`audit_events`)
对齐 Go 结构体 `model.AuditEvent`。
```sql
CREATE TABLE IF NOT EXISTS audit_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,           -- 物理入库自增序号，保证绝对追加顺序
    id TEXT NOT NULL UNIQUE,                         -- 事件唯一 ID: evt_<milli>_<hex12>
    action TEXT NOT NULL,                            -- 操作动作 (create/update/trash...)
    object TEXT NOT NULL,                            -- 对象类别 (note/file/pad/share...)
    object_id TEXT NOT NULL DEFAULT '',              -- 关联对象 ID
    summary TEXT NOT NULL DEFAULT '',                -- 摘要描述
    metadata TEXT,                                   -- 元数据 JSON 文本，若为 NULL 对应 Go 的 nil
    prev_hash TEXT NOT NULL DEFAULT '',              -- 前序事件 Hash
    hash TEXT NOT NULL,                              -- 当前事件 Hash (防篡改)
    created_at TEXT NOT NULL                         -- 发生时间 (RFC3339 毫秒)
);

CREATE INDEX IF NOT EXISTS idx_audits_id ON audit_events(id);
CREATE INDEX IF NOT EXISTS idx_audits_hash ON audit_events(hash);
CREATE INDEX IF NOT EXISTS idx_audits_object ON audit_events(object, object_id);
CREATE INDEX IF NOT EXISTS idx_audits_seq_desc ON audit_events(seq DESC);
```

---

## 三、字段双向映射与数据类型规则

| Go Struct 字段类型 | SQLite 列类型 | JSON (`omnishare.json`) | 转换规则与边界处理 |
| :--- | :--- | :--- | :--- |
| `string` | `TEXT` | `string` | 保持 UTF-8 原样；空串存为 `''`。 |
| `int`, `int64` | `INTEGER` | `number` | 保持数值。 |
| `bool` | `INTEGER` | `boolean` | `true` $\leftrightarrow 1$，`false` $\leftrightarrow 0$。 |
| `[]string` (`Tags`) | `TEXT` | `array of string` | 序列化为 JSON 字符串；读取为空时初始化为 `[]string{}`。 |
| `map[string]interface{}` (`Metadata`) | `TEXT` (可空) | `object` (`omitempty`) | **关键**：若 Go 为 `nil`，存为 SQLite `NULL`；若非空 map，存为 JSON 字符串。 |
| `time.Time` | `TEXT` | `string` (RFC3339) | 规范化为 UTC 并截断至毫秒：`.UTC().Truncate(time.Millisecond)`。 |
| `*time.Time` | `TEXT` (可空) | `string` (`omitempty`) | Go 中 `nil` 存为 SQLite `NULL`；反向转换时 `NULL` 映射为 `nil` 指针。 |

---

## 四、时间戳与 Audit 审计链连续性保障规范

### 1. 时间戳精度偏差根因分析
在 Go 原生实现中：
- `time.Now()` 含有纳秒（Nanosecond）级单调时钟；
- 原生 `json.Marshal` 序列化结构体时会保留可变小数位（最多 9 位，如 `.123456789Z`）；
- 若数据库写入时截断为秒或微秒，从数据库反序列化得到的 `time.Time` 将与原始时间戳产生纳秒差异；
- 该微小差异将直接导致 `sha256(json.Marshal(payload))` 产生完全不一致的雪崩哈希，导致审计链校验断裂。

### 2. 毫秒精度标准化规范（强制）
所有写入 SQLite 和计算 Hash 的时间戳，必须统一调用截断与格式化规范：
```go
// 统一时间戳标准化
func NormalizeTimestamp(t time.Time) time.Time {
    return t.UTC().Truncate(time.Millisecond)
}

// 格式化输出为 RFC3339 毫秒字符串
func FormatMilliTime(t time.Time) string {
    return NormalizeTimestamp(t).Format("2006-01-02T15:04:05.000Z")
}
```

### 3. Audit Hash 计算规范
`auditHash` 的计算载体定义：
```go
type auditPayload struct {
    ID        string                 `json:"id"`
    Action    string                 `json:"action"`
    Object    string                 `json:"object"`
    ObjectID  string                 `json:"object_id"`
    Summary   string                 `json:"summary"`
    PrevHash  string                 `json:"prev_hash"`
    Metadata  map[string]interface{} `json:"metadata"`   // 注意：不能有 omitempty！nil 必须输出为 null
    CreatedAt time.Time              `json:"created_at"` // 必须已 NormalizeTimestamp
}
```
**哈希校验规则**：
1. 首次事件（Genesis Event）：`prev_hash = ""`。
2. 后续追加事件：`e[i].prev_hash = e[i-1].hash`。
3. 从 SQLite 查询审计链时，必须按照 `ORDER BY seq ASC`（或逆序查询 `ORDER BY seq DESC` 倒推前序哈希），以内部自增主键 `seq` 规避毫秒并发可能导致的同时间戳排序歧义。

---

## 五、SQLite WAL 运行时配置与事务推荐

Go 在使用纯 Go 驱动（如 `modernc.org/sqlite`）打开数据库时，应配置如下连接串与 PRAGMA：

```go
db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)")
```

- **`journal_mode = WAL`**：读操作完全不阻塞写操作，写操作不阻塞读操作。
- **`synchronous = NORMAL`**：在 WAL 模式下保障足够的崩溃安全性（无需每次事务全盘 fsync），写性能提升 10~50 倍。
- **`busy_timeout = 5000`**：并发写锁等待时间设为 5 秒，彻底消除 `SQLITE_BUSY` 瞬时报错。
- **连接池设置**：Go 中 SQLite 写入属于单进程单写模型，建议设置 `db.SetMaxOpenConns(1)` 或使用带写通道的连接池，避免驱动层写锁竞争。

---

## 六、交付清单与验收标准

1. **产出物**：
   - DDL 文件：`backend/internal/storage/schema.sql`
   - 规范文档：`docs/DATA_SCHEMA_SQLITE_SPEC.md`
2. **验收标准**：
   - 具备完整外键/索引，支持秒级完成 10 万条记录的哈希链检索；
   - 任意现有 `omnishare.json` 经导入 SQLite 后再导出回 JSON，数据字段值、空指针状态、哈希链 100% 对齐无损。
