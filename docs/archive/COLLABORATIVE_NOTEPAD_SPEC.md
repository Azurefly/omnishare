# OmniShare 多人协同编辑记事本与共享平台功能扩展设计规范

- **文档版本**：v1.1.0
- **扩展功能**：多人实时协同编辑记事本 (Multi-User Collaborative Notepad / Real-Time Editor)
- **核心能力**：
  1. **房间/文档级协同**：支持创建独立可分享的协同记事本文档。
  2. **WebSocket 双向实时同步**：多人在同一文档内输入时，毫秒级推送变更。
  3. **OT / CRDT 增量 Operational 变更算法**：支持多人并发编辑内容无缝合并。
  4. **光标位置与在线协同者感知 (Presence Sensing)**：显示当前房间内有哪些用户/设备正在编辑，高亮显示各自的选区与光标位置。
  5. **历史版本与快照回退**：自动保存协同编辑历史快照，支持随时查看或回退。

---

## 一、 架构与数据流设计

```
 [ 用户设备 A (Web) ]       [ 用户设备 B (Web) ]       [ 用户设备 C (Tailnet) ]
        |                          |                          |
        +--------------------------+--------------------------+
                                   | (WebSocket /ws/pad/:pad_id)
                                   v
                   +-------------------------------+
                   |  OmniShare Collaborative Engine|
                   |  - Room Hub & Connection Pool |
                   |  - Operational Sync Transformer|
                   |  - Presence & Cursor Engine   |
                   +-------------------------------+
                                   |
                                   v
                   +-------------------------------+
                   | SQLite Document & Revision Store|
                   +-------------------------------+
```

---

## 二、 后端数据库扩展 Schema Design

```sql
-- 协同记事本房间主表
CREATE TABLE IF NOT EXISTS pad_documents (
    id TEXT PRIMARY KEY,                 -- 房间 ID (如: pad_m8z2k)
    title TEXT NOT NULL,                 -- 文档标题
    content TEXT DEFAULT '',             -- 当前文档最新完整内容
    version INTEGER DEFAULT 1,           -- 版本递增序号
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 协同变更历史操作日志表 (Operation Log)
CREATE TABLE IF NOT EXISTS pad_revisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pad_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    author TEXT NOT NULL,                -- 编辑者名称/IP
    changes TEXT NOT NULL,               -- 变更 delta JSON (如: {op: 'insert', pos: 10, text: 'hello'})
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pad_id) REFERENCES pad_documents(id) ON DELETE CASCADE
);
```

---

## 三、 WebSocket 协同帧协议规范 (Collaborative Protocol)

1. **`join_pad` (加入房间)**：
   ```json
   { "event": "join_pad", "payload": { "pad_id": "pad_m8z2k", "user_name": "MacBook-Pan" } }
   ```
2. **`pad_init` (获取当前初始内容与在线人员列表)**：
   ```json
   {
     "event": "pad_init",
     "payload": {
       "pad_id": "pad_m8z2k",
       "content": "# 项目协同会议记录\n1. 讨论 API 协议",
       "version": 12,
       "users": [ { "id": "usr_1", "name": "MacBook-Pan", "color": "#3B82F6" } ]
     }
   }
   ```
3. **`op_change` (广播编辑变更)**：
   ```json
   {
     "event": "op_change",
     "payload": {
       "pad_id": "pad_m8z2k",
       "version": 13,
       "op": { "from": 12, "to": 15, "text": "及 WebSocket 协同" },
       "cursor": { "line": 2, "ch": 16 }
     }
   }
   ```
4. **`presence_cursor` (实时光标位置广播)**：
   ```json
   {
     "event": "presence_cursor",
     "payload": { "pad_id": "pad_m8z2k", "user_name": "iPhone-15", "cursor": { "line": 3, "ch": 5 } }
   }
   ```
