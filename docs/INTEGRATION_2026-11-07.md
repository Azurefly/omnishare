# OmniShare 分支整合与可靠性优化

日期：2026-11-07。交付分支：`fix/integrated-stability-20261107`。

## 整合范围

本次核对全部远程分支，确认下面的完整祖先关系，并按顺序快进整合。没有遗漏的独立功能分支，也没有使用强制覆盖或重新摘取已有提交。

| 来源 | 集成前的头提交 | 相对上一行的提交数 |
| --- | --- | ---: |
| `main` | `2571b5a7b04dcafbfe0720166d9ad83c4461794b` | 基线 |
| `fix/security-and-backup-hardening`（PR #24） | `28516b56d2643ab398066deeb5de200749b6749a` | 15 |
| `feat/sqlite-encrypted-backup`（PR #25） | `c121dfa19f5b072097c26c1c40bac62d6cbd6122` | 10 |

统一分支包含上述 25 个尚未进入原 `main` 的提交，本轮修复继续提交在这条完整历史之后。原有安全修复、SQLite、加密备份、分页、长传输和桌面打包修复均保留。

## 本轮解决的问题

| 范围 | 已复现的问题 | 修复后的行为 |
| --- | --- | --- |
| 恢复回滚 | 回滚快照早于最终切换，失败恢复会丢失解压期间已经成功新增的文件记录和笔记 | HTTP 数据请求共享读锁；最终切换取得排他锁之后再拍摄回滚快照，状态、配置和上传目录按同一代切换 |
| 恢复上传 | `Request.Body.Close()` 无法打断真实 HTTP 阻塞读取；提前拒绝请求或完成首个 multipart 后，收尾排空也可能挂起 | 使用连接空闲读截止时间；只有完整读取到 HTTP EOF 才清除截止时间，未完成正文的收尾读取同样受限 |
| 加密备份 | 旧容器没有认证完整包头和结束记录，可接受同长度元数据修改、提前结束和尾随数据；空流也不能验证口令 | 新 v3 容器将完整包头摘要、分块序号及记录类型加入 GCM AAD；结束记录单独认证并要求 EOF |
| 密钥派生 | 不可信备份头没有 PBKDF2 迭代次数上限，且自行实现 PBKDF2 | 改用 Go 标准库，派生前校验格式及计算上限；导出默认仍为 210,000 次 |
| SQLite 启动 | 已有数据库打开失败时自动选择陈旧迁移 JSON；未知引擎被默认为 SQLite；重启可能误报未迁移 | 已有数据库错误明确失败，首次导入失败仍可回退；非法引擎报错；成功启动不再误报迁移待办 |
| 受限阅读 | 上限为 2 的笔记连续读取仍反复返回计数 1；并发请求可多次取得阅后即焚内容 | 查询、限制判断和消费在同一写锁范围内；受限读取同步提交并检查实际更新行数，普通阅读仍不写 SQLite 事务 |
| 缓冲计数 | 刷盘失败丢失批次，恢复后旧计数污染新状态，快照忽略尚未刷盘计数 | 失败批次重新入队；成功恢复清理旧批次；一致只读事务快照叠加当前缓冲 |
| 桌面首启 | 后端使用 `--no-browser`，WebView 只获得裸 URL，新鉴权规则使首次安装无法配置 | 为正在运行的自有子进程解析并传入实际数据目录，仅首次导航响应交接初始化令牌；附着其他服务不发送猜测令牌 |
| 浏览器会话 | 禁用或写满 `sessionStorage` 会导致启动失败，失败删除后还可能重新读回旧凭据 | 使用当前页面内存作为会话后备，并优先采用当前页的新值或删除标记 |
| 草稿与传输 | `preservePadDraft` 未定义；等待备份响应后弹保存选择器可能失去用户激活；恢复可重复提交 | 保留最新编辑内容；先选目标再请求，失败时终止下载；恢复期间阻止重复提交，口令不做 trim |

## 验证方式

回归测试先在旧实现上复现失败，再验证修复。测试使用临时数据目录，不读写真实用户数据。

- Go 全仓单元与集成测试、Race Detector、`go vet`、`staticcheck` 和格式检查。
- JSON / SQLite 两种引擎均验证失败恢复保留并发写入。
- 真实 TCP 连接验证正文停发、非 multipart 早退和 multipart 尾部停发；不以 `io.Pipe` 测试代替网络证据。
- 32 并发读取验证阅后即焚恰好成功一次、上限为 3 时恰好成功三次。
- 加密 v3 往返、完整性拒绝、错误口令、空流、v2 兼容及 ZIP 兼容验证。
- 前端静态契约及 12 项 Node VM 行为回归。行为测试执行实际 `app.js` 函数，替换浏览器边界，覆盖存储拒绝、冲突草稿、流式下载、大小限制与防重复恢复。
- 桌面 Node 契约，以及新增初始化模块的 4 项 Rust 单元测试与 rustfmt。
- 独立后端进程的真实 HTTP 检查：SQLite 首启、默认鉴权、中文笔记、上传与 Range、加密备份恢复、错误口令、单实例锁、重启持久化。

常用复验命令：

```bash
npm --prefix frontend run check
npm --prefix frontend test
npm --prefix frontend run build
npm --prefix desktop run test:contracts
bash scripts/test-windows-launcher.sh

(cd backend && go test ./... -count=1 -race)
(cd backend && go vet ./...)
(cd backend && staticcheck ./...)
(cd backend && go build -trimpath -o ../omnishare-http-test ./cmd/omnishare)
python3 scripts/test-http-integration.py ./omnishare-http-test

(cd desktop/src-tauri && cargo fmt --check)
(cd desktop/src-tauri && cargo clippy --all-targets --all-features -- -D warnings)
(cd desktop/src-tauri && cargo test --all-features)
```

CI 已加入真实 HTTP 进程验证。统一 PR 仍须运行仓库 CI 的六目标 Go 交叉构建，以及 Linux、Windows、macOS Universal 的完整桌面构建与测试；其最终状态以该 PR 的 Checks 为准。

## 兼容性与验证边界

- 新版本继续读取 v2 `.osb` 和 ZIP。旧版本程序无法读取新生成的 v3 `.osb`，恢复新备份应使用本次或更新版本程序；旧 v2 容器保留其原始认证能力。
- 成功恢复表示用备份替换当前状态，备份之后的新数据仍会按恢复语义被替换；本次修复保证失败恢复不额外丢失已成功写入的数据。
- 普通计数仍每 5 秒刷新，进程突然崩溃可能丢失最近几秒计数；正常备份快照和失败重试已覆盖缓冲计数。
- 本地 Chromium 安装源返回不可用页面，因此前端行为证据来自 Node VM，未宣称完成真实浏览器交互验收。桌面新增模块通过本地 Rust 测试，完整 Tauri 三平台结果由 PR CI 提供。

## 后续专项

后续优先复核密钥清除及恢复无密钥配置后的初始化令牌续发、自动清理与恢复之间的文件协调，以及审计锚点在备份恢复中的完整保存。这些不作为本轮已经完成的能力。
