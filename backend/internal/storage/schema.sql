-- OmniShare SQLite Storage Engine Schema
-- Schema Version: 2 (Lossless compatible with omnishare.json state schema v2)
-- Architecture: WAL mode, crash-safe, strictly ordered Merkle audit chain

PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA temp_store = MEMORY;

-- ============================================================================
-- 元数据版本控制表 (Schema Meta)
-- ============================================================================
CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('schema_version', '2');
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('format', 'sqlite_wal');
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('timestamp_precision', 'rfc3339_milli');

-- ============================================================================
-- 1. 随手记主表 (QuickNote)
-- 对应 model.QuickNote 结构体
-- ============================================================================
CREATE TABLE IF NOT EXISTS quick_notes (
    id TEXT PRIMARY KEY,                             -- 格式: n_<milli>_<hex12>
    content TEXT NOT NULL DEFAULT '',                -- 文本正文
    content_redacted INTEGER NOT NULL DEFAULT 0,     -- 是否已被安全脱敏 (0: false, 1: true)
    content_type TEXT NOT NULL DEFAULT 'text/plain', -- MIME 类型，如 text/plain, text/markdown
    tags TEXT NOT NULL DEFAULT '[]',                 -- 标签列表，JSON 数组格式 (例: ["#tag1","tag2"])
    pinned INTEGER NOT NULL DEFAULT 0,               -- 置顶状态 (0: false, 1: true)
    is_burn_after_read INTEGER NOT NULL DEFAULT 0,   -- 阅后即焚 (0: false, 1: true)
    read_count INTEGER NOT NULL DEFAULT 0,           -- 已读次数
    max_read_count INTEGER NOT NULL DEFAULT 0,       -- 最大可读次数，0 表示无限制
    expires_at TEXT,                                 -- 过期时间 (RFC3339 UTC 毫秒精度: YYYY-MM-DDTHH:MM:SS.sssZ)，NULL 表示永不过期
    deleted_at TEXT,                                 -- 软删除时间 (进入回收站)，NULL 表示未删除
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 UTC 毫秒精度)
    updated_at TEXT NOT NULL                         -- 更新时间 (RFC3339 UTC 毫秒精度)
);

-- 索引优化：活跃随手记列表与置顶排序
CREATE INDEX IF NOT EXISTS idx_notes_active_lookup ON quick_notes(deleted_at, expires_at, pinned, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notes_deleted_at ON quick_notes(deleted_at);
CREATE INDEX IF NOT EXISTS idx_notes_expires_at ON quick_notes(expires_at);

-- ============================================================================
-- 2. 文件资产表 (FileAsset)
-- 对应 model.FileAsset 结构体
-- ============================================================================
CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,                             -- 格式: f_<milli>_<hex12>
    file_name TEXT NOT NULL,                         -- 原始文件名 (经文件名清理)
    file_size INTEGER NOT NULL,                      -- 文件物理大小 (字节)
    mime_type TEXT NOT NULL,                         -- MIME 类型
    storage_path TEXT NOT NULL DEFAULT '',           -- 磁盘相对路径 (基于 uploads/blobs/...)
    file_hash TEXT NOT NULL DEFAULT '',              -- 文件内容 SHA-256 摘要
    download_count INTEGER NOT NULL DEFAULT 0,       -- 下载累计次数
    is_video INTEGER NOT NULL DEFAULT 0,             -- 是否为视频流文件 (0: false, 1: true)
    download_url TEXT NOT NULL DEFAULT '',           -- 下载接口路径
    stream_url TEXT NOT NULL DEFAULT '',             -- 流播放接口路径
    expires_at TEXT,                                 -- 过期时间 (RFC3339 UTC 毫秒精度)
    deleted_at TEXT,                                 -- 软删除时间 (回收站状态)
    created_at TEXT NOT NULL                         -- 创建时间 (RFC3339 UTC 毫秒精度)
);

-- 索引优化：基于哈希和大小的极速秒传/去重查找，以及活跃文件检索
CREATE INDEX IF NOT EXISTS idx_files_hash_size ON file_assets(file_hash, file_size, deleted_at);
CREATE INDEX IF NOT EXISTS idx_files_storage_path ON file_assets(storage_path);
CREATE INDEX IF NOT EXISTS idx_files_deleted_at ON file_assets(deleted_at);
CREATE INDEX IF NOT EXISTS idx_files_expires_at ON file_assets(expires_at);
CREATE INDEX IF NOT EXISTS idx_files_created_at ON file_assets(created_at DESC);

-- ============================================================================
-- 3. 协同文档便签表 (PadDocument)
-- 对应 model.PadDocument 结构体
-- ============================================================================
CREATE TABLE IF NOT EXISTS pad_documents (
    id TEXT PRIMARY KEY,                             -- 格式: pad_<milli>_<hex12>
    title TEXT NOT NULL DEFAULT '',                  -- 文档标题
    content TEXT NOT NULL DEFAULT '',                -- 文档内容 (Markdown/纯文本)
    version INTEGER NOT NULL DEFAULT 1,              -- 乐观锁并发版本号 (单调自增)
    deleted_at TEXT,                                 -- 软删除时间
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 UTC 毫秒精度)
    updated_at TEXT NOT NULL                         -- 更新时间 (RFC3339 UTC 毫秒精度)
);

-- 索引优化：回收站过滤与最近编辑文档列表
CREATE INDEX IF NOT EXISTS idx_pads_deleted_at ON pad_documents(deleted_at);
CREATE INDEX IF NOT EXISTS idx_pads_updated_at ON pad_documents(updated_at DESC);

-- ============================================================================
-- 4. 分享链接表 (ShareLink)
-- 对应 model.ShareLink 结构体
-- ============================================================================
CREATE TABLE IF NOT EXISTS share_links (
    id TEXT PRIMARY KEY,                             -- 格式: shr_<milli>_<hex12>
    token TEXT NOT NULL UNIQUE,                      -- 随机加密分享 Token (十六进制字符串)
    object_type TEXT NOT NULL,                       -- 共享对象类别: 'note' | 'file' | 'pad'
    object_id TEXT NOT NULL,                         -- 共享对象 ID
    name TEXT NOT NULL DEFAULT '',                   -- 共享展示名称
    url TEXT NOT NULL DEFAULT '',                    -- 分享 URL 路径
    access_count INTEGER NOT NULL DEFAULT 0,         -- 已消费访问次数
    max_access_count INTEGER NOT NULL DEFAULT 0,     -- 最大允许访问次数，0 表示无限制
    expires_at TEXT,                                 -- 分享失效时间
    revoked_at TEXT,                                 -- 手动或联动撤销时间
    last_accessed_at TEXT,                           -- 最后访问时间
    created_at TEXT NOT NULL,                        -- 创建时间 (RFC3339 UTC 毫秒精度)
    status TEXT NOT NULL DEFAULT 'active'            -- 状态标识: active/revoked/expired/exhausted
);

-- 索引优化：Token 秒查、目标对象联动失效过滤
CREATE INDEX IF NOT EXISTS idx_shares_token ON share_links(token);
CREATE INDEX IF NOT EXISTS idx_shares_target ON share_links(object_type, object_id, revoked_at);
CREATE INDEX IF NOT EXISTS idx_shares_status ON share_links(status);

-- ============================================================================
-- 5. 审计事件链表 (AuditEvent)
-- 对应 model.AuditEvent 结构体
-- 采用单调自增序号 seq 维持物理绝对追加时序，保证 Merkle Hash 链完整无断层
-- ============================================================================
CREATE TABLE IF NOT EXISTS audit_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,           -- 物理绝对顺序序列号，严格保证链式依赖
    id TEXT NOT NULL UNIQUE,                         -- 事件唯一 ID (evt_<milli>_<hex12>)
    action TEXT NOT NULL,                            -- 操作动作 (create/update/read/trash/restore/purge/share/access/revoke/upload/rename/cleanup)
    object TEXT NOT NULL,                            -- 涉及对象类型 (note/file/pad/share/trash/system)
    object_id TEXT NOT NULL DEFAULT '',              -- 涉及对象 ID (可为空)
    summary TEXT NOT NULL DEFAULT '',                -- 事件简述摘要
    metadata TEXT,                                   -- 结构化元数据 JSON 文本 (为 NULL 时对齐 Go 的 nil，非 NULL 时存储 JSON 对象)
    prev_hash TEXT NOT NULL DEFAULT '',              -- 前序事件哈希 (首条事件为空字符串)
    hash TEXT NOT NULL,                              -- 当前事件 SHA-256 校验和 (保证防篡改)
    created_at TEXT NOT NULL                         -- 事件发生时间 (RFC3339 UTC 毫秒精度)
);

-- 索引优化：哈希防篡改校验遍历、对象事件追踪
CREATE INDEX IF NOT EXISTS idx_audits_id ON audit_events(id);
CREATE INDEX IF NOT EXISTS idx_audits_hash ON audit_events(hash);
CREATE INDEX IF NOT EXISTS idx_audits_object ON audit_events(object, object_id);
CREATE INDEX IF NOT EXISTS idx_audits_seq_desc ON audit_events(seq DESC);
