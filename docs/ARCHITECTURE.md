# OmniShare v1.3.0-rc1 架构与边界

```text
Browser / PWA / Desktop launcher
              |
  loopback HTTP by default
  optional TLS / trusted LAN
              |
       Go net/http single binary
   ┌──────────┼───────────┐
 REST API  embedded UI  signed LAN discovery
   |                       + bounded peer probes
 candidate-state transaction layer
   |
 durable JSON generations + content-addressed uploads/
   |
 active objects / trash / shares / audit hash chain
```

## 启动与网络边界

1. 程序先取得数据目录进程锁，第二个写实例立即失败。
2. 配置默认 `127.0.0.1`、无局域网访问。
3. 非回环监听必须同时启用 LAN 并存在强访问密钥。
4. HTTP listener 成功后才启动发现广播。
5. 可选 TLS 由 `--tls-cert` 与 `--tls-key` 提供；未配置时不宣称链路加密。

## 鉴权模型

- 管理 API 使用 `X-OmniShare-Key` 请求头。
- 服务端配置只保存 PBKDF2-HMAC-SHA256 派生值、随机盐和迭代次数。
- 成功验证结果以进程内 HMAC 摘要短时缓存；密钥轮换/恢复立即清空。
- 浏览器只在 `sessionStorage` 保存本会话密钥，不写 URL、不写长期存储。
- 文件下载/播放使用 10 分钟签名票据；公开分享使用对象级随机令牌和短时 HttpOnly 会话 Cookie。
- 当前仍是单管理员权限模型，角色和作用域令牌尚未实现。

## 状态提交模型

每次写操作遵循：

```text
读取当前状态 → 深复制候选状态 → 修改候选 → 序列化并 durable write
→ 成功后替换内存状态 / 失败则丢弃候选
```

`durable.WriteFile` 执行临时文件写入、文件同步、上一代备份、原子替换和父目录同步。启动时主文件损坏可回退 `.bak`。

这比 v1.2 的“先改内存再尝试落盘”可靠，但仍不是数据库事务：所有元数据写入仍需序列化完整 JSON，复杂度与数据量线性增长。RC 之后必须评估 SQLite WAL 迁移，JSON 应降级为导入导出格式。

## 文件模型

- 上传流式写入 `.staging` 并计算 SHA-256。
- 实体路径按摘要内容寻址，相同内容复用一个 Blob。
- 每次上传仍创建独立逻辑 `FileAsset`，保留各自文件名、TTL、分享和回收站状态。
- 下载票据只引用逻辑文件 ID；实际读取验证实体存在和大小。
- 备份再次校验文件大小和 SHA-256。

## 分享计数语义

- HEAD、安全扫描和预取不消耗访问次数。
- 首次真实 GET 消耗一次并设置短时、HttpOnly、SameSite=Strict 会话。
- 同一会话的后续 Range/刷新不重复扣减。
- 新会话在达到上限后返回 410。
- 分享对象在消费前先验证元数据和物理文件。
- HTML、SVG、XML 等主动内容强制 attachment；只允许位图、音频、视频白名单内联。

## 备份恢复

备份 ZIP 包含：

- `omnishare.json`
- `config.json`（无明文管理密钥）
- `backup-manifest.json`
- 去重后的所有 Blob

恢复先验证路径、格式版本、状态/配置摘要、每个文件大小与摘要；文件在 staging 中解压，随后分阶段替换上传目录、状态和配置。任一步失败会尝试回滚原文件、原状态和原配置。

## 仍未解决的架构债务

- 单 JSON O(N) 写放大与全局写串行。
- 缺少角色、作用域授权和可信设备配对。
- 发现只支持 IPv4 Multicast；手动节点仍需管理员谨慎配置。
- 无分片/续传、端到端加密、CRDT、原生托盘和自动升级。
- 无跨平台真实 UI 自动化、签名、公证、SBOM 和可重现构建证明。
