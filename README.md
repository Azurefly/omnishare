# OmniShare v1.2.0

OmniShare 是面向个人、家庭、小团队和内网环境的私有化多设备内容中枢。它把随手记、文件与视频、安全分享、协同文档、设备发现、审计、备份和回收站放在一个可离线部署的轻量产品中。

项目采用 **Go 标准库单二进制 + 零依赖静态前端 + 本地 JSON 原子存储**，不依赖公网服务、Node 运行时或独立数据库。

## v1.2.0 核心能力

- 随手记：搜索、标签、编辑、置顶、TTL、阅后即焚、最大读取次数。
- 文件与视频：多文件上传、进度、SHA-256、重复文件复用、重命名、Range 播放和下载。
- 协同文档：自动保存、显式保存、版本号和 409 冲突保护。
- 安全分享：按随手记、文件或文档创建独立链接，支持有效期、最大访问次数、撤销和访问审计。
- 回收站：普通删除可恢复，支持按类型筛选、永久删除、清空和自动保留周期。
- 设备与网络：局域网 UDP 发现、本机多网卡识别、Tailscale 地址识别和手动远端节点。
- 管理能力：统计、审计、ZIP 全量备份、上传上限、访问密钥、过期内容清理。
- 桌面交付：Windows x64/ARM64、Linux x64/ARM64、macOS Intel/Apple Silicon，同时支持 PWA。
- 工程治理：GitHub Actions、五轮测试、跨平台构建、版本化需求、发布说明和安全说明。

## 运行

```bash
cd backend
go run ./cmd/omnishare
```

默认地址为 `http://127.0.0.1:8081`。常用参数：

```text
--port 8081
--data-dir /path/to/data
--name My-Node
--no-browser
```

默认数据目录：

- Windows：`%USERPROFILE%\.omnishare`
- Linux/macOS：`~/.omnishare`

## 构建

```bash
npm --prefix frontend run build
cd backend && go build -o omnishare ./cmd/omnishare
```

完整六平台构建：

```bash
./scripts/build-release.sh
```

## 五轮测试

```bash
./scripts/test-5-rounds.sh
```

测试覆盖源码质量、存储与配置单元测试、API 集成、前端契约与六平台交叉编译、真实服务端到端与安装包结构。

## 安全边界

- 服务默认监听所有网卡，只适合可信局域网或 VPN/Tailscale 环境，不应直接裸露到公网。
- 全局访问密钥用于管理界面与 API；对外分享应使用独立安全分享链接，不再转发带全局密钥的 URL。
- 当前分享链接是能力令牌，获得链接的人即可访问；高敏感场景仍应等待后续的分享密码、端到端加密和可信设备配对。
- Windows/macOS 安装包尚未做商业代码签名和公证。
- 当前协同文档使用乐观锁，不宣称 CRDT/OT 实时多人编辑。

## 需求和路线图

- 产品扩写：`docs/PRODUCT_REQUIREMENTS_v1.2.md`
- 开源项目对标：`docs/OPEN_SOURCE_BENCHMARK_2026.md`
- 架构：`docs/ARCHITECTURE.md`
- 发布说明：`docs/RELEASE_NOTES_v1.2.0.md`
- 五轮测试报告：`docs/TEST_REPORT_5_ROUNDS_v1.2.0.md`
