# SQLite WAL 存储引擎改造测试用例集规范与覆盖矩阵

> **归属单位**：测试与安全合规专家（刑部 / Testing & SecOps）  
> **文档版本**：v1.0.0  
> **生效工程**：`omnishare/backend/internal/storage`  
> **基准比对**：现有 `json.go` & `json_test.go` (100% PASS)

---

## 一、现有存储测试用例体系分析

当前 `backend/internal/storage/json_test.go` 针对单 JSON 文件持久化机制建立了 5 大核心测试套件，执行表现如下：

| 测试用例名 | 覆盖领域 | 核心校验机制与断言 |
|---|---|---|
| `TestStoreNoteLifecycleExpirationTrashAndRollback` | 便签生命周期 | 增删改查、全文/标签过滤、过期隐藏、垃圾箱恢复、持久化失败时内存态原子回滚 |
| `TestBurnAfterReadMaxReadAndShareRevocation` | 安全与时效控制 | 阅后即焚（单次读取即焚）、读取上限阻断、关联共享令牌联动作废 |
| `TestFileValidationLogicalDedupReferencesAndTrash` | 文件与引用计数 | SHA-256 秒传与去重、跨资产引用保护、引用计数归零安全 Purge |
| `TestPadConflictShareLifecycleAndAuditChain` | 协作文档与审计链 | 乐观并发版本冲突校验 (CAS)、令牌消费额度控制、SHA-256 链式防篡改审计追踪 |
| `TestCleanupOnlyPersistsChangesAndRecoveryBackup` | 清理与灾备 | TTL 过期自动归档、回收站自动清空、主文件损坏时从 `.bak` 备份自动容灾恢复 |

**现有体系痛点与 WAL 改造驱动**：
1. 全量状态单 JSON 序列化导致所有写入持有全局排他互斥锁（`sync.RWMutex`），读写并发存在锁竞争瓶颈；
2. 现有灾备仅依赖同目录 `.bak` 副本，在极高并发或系统强杀时缺少 WAL 预写日志级别的崩溃恢复能力；
3. 需建立针对 SQLite WAL 专有并发特性、读写分离连接池及事务原子性的独立测试用例集。

---

## 二、SQLite WAL 测试用例集设计架构

### 1. 测试用例矩阵 (Test Matrix)

| 用例编号 | 用例名称 | 核心测试目标 | 验证手段与断言标准 |
|---|---|---|---|
| **WAL-TC-01** | `TestWAL_100ConcurrentReadWriteContention` | 100 并发读写锁争用 | 80 个并发读协程 + 20 个并发写协程同时高频访问；设置 `busy_timeout=5000ms`；断言：0 死锁、0 个 `database is locked` 崩溃、所有提交数据 100% 具备一致性 |
| **WAL-TC-02** | `TestWAL_ReadOnlyConnectionWriteInterception` | `mode=ro` 读连接池误写拦截 | 使用附加 `mode=ro` 的只读 DSN 连接池尝试执行 INSERT/UPDATE/DELETE 及破坏性 DDL；断言：必须严格抛出 `readonly database` 异常，读操作不受影响 |
| **WAL-TC-03** | `TestWAL_SingleTxMigrationRollbackAndCleanup` | 单事务迁移原子回滚与脏库清理 | 模拟从 JSON 迁移至 SQLite 过程中注入致命错误（IO 错误/校验失败/异常中断）；断言：迁移事务全局回滚、损坏的临时/脏库文件彻底清理、原始数据源零损坏 |
| **WAL-TC-04** | `TestWAL_CrashPowerLossRecovery` | 崩溃与断电断盘恢复 | 大量未 Checkpoint 的事务驻留于 `.db-wal` 时强行终止连接/进程；重新挂载后校验 SQLite 自动恢复；断言：已提交事务全部回放成功，审计哈希链未损坏 |

---

## 三、四大核心测试用例详细规格说明

### 1. WAL-TC-01: 100 并发读写锁争用测试 (High Concurrency Contention)
- **前置条件**：数据库初始化开启 `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`。
- **执行过程**：
  1. 预填入 50 条基础 Note/File/Pad 数据；
  2. 启动 `sync.WaitGroup` 编排 100 个并发 goroutine：
     - **20 个 Writer**：循环执行随机添加 Note、追加 Audit、更新 Pad 操作（总计每个写 50 次，全生命周期 1000 次写）；
     - **80 个 Reader**：以极高频率并发执行 `ListNotes`、`GetPad`、`ListAudits` 查询；
  3. 收集所有协程的 Error 返回与耗时。
- **断言要求**：
  - Error 计数为 0；
  - 最终写入的总记录数恒等于基准值 + 1000；
  - 审计链哈希逐级连续校验无分叉或断链。

### 2. WAL-TC-02: mode=ro 读连接池误写拦截反例测试 (Security & Role Separation)
- **前置条件**：配置双连接池：
  - **Writer Pool**：`file:omnishare.db?cache=shared&_journal_mode=WAL` (MaxOpenConns = 1)
  - **Reader Pool**：`file:omnishare.db?mode=ro&cache=shared` (MaxOpenConns = 10)
- **执行过程**：
  1. 尝试直接从 Reader Pool 执行 `INSERT INTO notes (id, content) VALUES ('illegal', 'hack')`；
  2. 尝试从 Reader Pool 执行 `DROP TABLE pads`；
  3. 尝试通过 Reader Pool 修改敏感 PRAGMA（如关闭安全模式）。
- **断言要求**：
  - 所有写语句返回包含 `attempt to write a readonly database` 或权限拒绝错误；
  - 原始数据完整无任何篡改；
  - Reader Pool 在遭遇写注入尝试后，后续执行合法 `SELECT` 仍能正常工作，连接池未中毒。

### 3. WAL-TC-03: 单事务迁移原子回滚与脏库清理测试 (Atomic Migration & Dirty Cleanup)
- **前置条件**：准备现有格式合法的 `omnishare.json` 测试数据集（含 Note 100 条、Pad 20 条、File 10 条）。
- **执行过程**：
  1. 启动迁移引擎，开启单一显式 `BEGIN EXCLUSIVE TRANSACTION`；
  2. 顺序导入 Notes -> Files -> Pads；
  3. 在迁移到 Pad 第 15 条时触发注入故障（模拟磁盘已满或违反外键约束）；
  4. 触发异常捕获逻辑，执行 `ROLLBACK` 并调用 `CleanDirtyDBArtifacts()` 清理中间产物。
- **断言要求**：
  - 目标 SQLite `.db`、`.db-wal`、`.db-shm` 若未完成则不应处于活动或半同步状态，且脏文件被安全移动或抹除；
  - 原始 `omnishare.json` 文件校验和（SHA-256）未发生任何改变；
  - 再次启动迁移流程能够干净重试并成功。

### 4. WAL-TC-04: 崩溃断电恢复测试 (Crash / Power-Loss Recovery)
- **前置条件**：配置 WAL Checkpoint 阈值为大容量，确保大量提交数据在写入阶段保留于 WAL。
- **执行过程**：
  1. 并发写入 500 个包含 SHA-256 审计链条的业务记录；
  2. 确认 `.db-wal` 文件尺寸增长且未触发 Checkpoint 到主 `.db`；
  3. 模拟操作系统骤停（非平稳关闭 DB 句柄，直接截断或复制只包含原始 `.db` + `.db-wal` 的现场镜像到独立隔离测试沙盒）；
  4. 使用新 Store 实例加载该沙盒目录。
- **断言要求**：
  - SQLite 引擎在冷启动阶段成功执行 WAL 恢复，日志无损坏告警；
  - 500 条记录全部完好无缺；
  - 审计链 SHA-256 完整性校验 100% 通过（无孤儿哈希或尾部断裂）。

---

## 四、安全合规与御史凭证准则
1. **零外部残留**：所有测试用例必须使用 `t.TempDir()`，严禁在生产或代码库根目录遗留 `.db-wal` 或测试日志。
2. **审计链强保密防伪**：迁移与 WAL 恢复过程中，所有 Audit Event 的 `PrevHash` 必须严格递推，断绝任何断链可能。
3. **只读池硬约束**：`mode=ro` 为架构安全基线，杜绝任何代码路径中以读连接处理写入任务。
