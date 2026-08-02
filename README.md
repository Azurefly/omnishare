# OmniShare v1.3.0-rc1

OmniShare 是面向个人、家庭、小团队和内网环境的私有化多设备内容中枢。它将随手记、文件与视频、受控分享、轻量协作文档、设备发现、审计、备份恢复和回收站整合为可离线构建的单二进制产品。

> **发布级别：Release Candidate。** v1.3.0-rc1 完成了 v1.2 深度评审中的主要发布阻断整改，但主存储仍是单 JSON 快照，尚未达到高并发、大规模或高合规生产环境要求。

## 本版本重点

- **默认安全**：首次启动仅监听 `127.0.0.1`；开启局域网必须先设置至少 16 字节访问密钥。
- **密钥保护**：管理密钥不再接受 URL 参数，不再写入浏览器长期存储；服务端只保存 PBKDF2-HMAC-SHA256 派生值。
- **内容票据**：下载、视频与 Raw 内容不再拼接全局密钥；文件使用短时签名票据。
- **分享边界**：HEAD 不消耗次数；同一短时浏览器会话不会被 Range 请求重复扣减；HTML/SVG/XML 强制下载。
- **浏览器隔离**：严格 CSP、`no-store`、`no-referrer`；Service Worker 只缓存固定静态资源。
- **数据可靠性**：写入失败不污染内存；主状态与配置保留上一代备份；文件写入执行文件和目录同步。
- **单实例**：同一数据目录使用跨平台进程锁，拒绝第二个写实例。
- **文件模型**：同内容复用物理 Blob，但每次上传保留独立逻辑文件、文件名与生命周期。
- **完整备份恢复**：备份包含状态、配置、文件清单和 SHA-256；恢复先校验、分阶段替换并支持回滚。
- **发现协议**：局域网公告使用稳定 Ed25519 身份和签名，校验时间、端口、字段长度并确定性去重排序。
- **协作保护**：文档自动保存按文档快照串行执行；切换前保存；冲突时保留本地草稿。
- **六平台交付**：Windows、Linux、macOS 的 x64/ARM64 二进制与安装结构。

## 快速运行

```bash
cd backend
go run ./cmd/omnishare
```

默认地址：`http://127.0.0.1:8081`

常用参数：

```text
--port 8081
--data-dir /path/to/data
--name My-Node
--listen 127.0.0.1
--tls-cert /path/to/cert.pem
--tls-key /path/to/key.pem
--no-browser
```

### 开启局域网

先在本机设置不少于 16 字节的访问密钥，再启用“允许局域网访问”。没有访问密钥时，程序会拒绝绑定非回环地址。跨不可信网络应使用 HTTPS、Tailscale 或可信 VPN。

## 数据目录

直接运行二进制时默认使用：

- Windows：`%USERPROFILE%\.omnishare`
- Linux/macOS：`~/.omnishare`

Linux 用户级安装包使用 XDG 目录：

- 程序：`~/.local/lib/omnishare`
- 启动器：`~/.local/bin/omnishare`
- 数据：`${XDG_DATA_HOME:-~/.local/share}/omnishare`
- 状态与日志：`${XDG_STATE_HOME:-~/.local/state}/omnishare`

卸载程序默认保留用户数据。

## 构建与测试

```bash
npm --prefix frontend run build
cd backend && go build -trimpath -o omnishare ./cmd/omnishare
```

完整六平台构建与打包：

```bash
./scripts/build-release.sh
```

五轮发布门禁：

```bash
./scripts/test-5-rounds.sh
```

单独执行某轮：

```bash
OMNISHARE_ROUNDS=3 ./scripts/test-5-rounds.sh
```

五轮分别验证：源码/版本/前端契约、全量 Race Detector、安全和故障对抗、六目标交叉编译、安装包完整性与真实 Linux 用户级安装/卸载。每轮都会重复核心回归。

## 明确限制

- 主存储仍是单 JSON 快照；数据规模、写并发和审计量增大时存在 O(N) 写放大，下一阶段应迁移 SQLite WAL。
- 当前只有单管理员密钥，没有只读、上传者、审计员等角色和作用域令牌。
- HTTPS 需要用户提供证书；尚无自动证书签发和可信设备配对。
- 上传是流式单请求，不是分片、断点续传或跨设备端到端加密。
- 文档使用乐观锁，不是 CRDT/OT 实时协同。
- Windows/macOS 已完成交叉编译和安装包结构验证，但本轮环境只真实运行了 Linux 安装流程。
- 发布物未做商业代码签名、公证、SBOM 和来源证明。
- 当前 Go 总语句覆盖率为 51.9%，`main`、桌面启动等路径仍需要平台级测试。

## 文档

- [v1.3.0-rc1 发布说明](docs/RELEASE_NOTES_v1.3.0-rc1.md)
- [150 条评审整改映射](docs/REMEDIATION_v1.3.0-rc1.md)
- [五轮测试报告](docs/TEST_REPORT_5_ROUNDS_v1.3.0-rc1.md)
- [架构与安全边界](docs/ARCHITECTURE.md)
- [v1.3 产品需求](docs/PRODUCT_REQUIREMENTS_v1.3.md)
- [安全策略](SECURITY.md)
