# OmniShare v1.3.0-rc1 五轮测试报告

## 结论

**五轮全部通过，但仅批准进入 RC 验证。** 测试通过不消除单 JSON 架构、平台实机和供应链签名风险。

## 第一轮：源码、版本、前端契约

- `gofmt`、`go vet`。
- 根 VERSION、Go 内置版本、前端 package、Service Worker 缓存版本一致。
- 前端 JavaScript 语法与安全契约。
- 核心存储、配置、API、单实例、发现和 durable 回归。

结果：通过。

## 第二轮：Race Detector

- `go test -race ./...`。
- 再次执行核心回归。
- 覆盖配置密钥缓存轮换、候选状态提交、发现 Registry 并发和锁。

结果：通过，无 Race Detector 报告。

## 第三轮：安全与故障对抗

动态启动真实服务并验证：

- 恶意 Origin 403；伪造 Host 400；尾随 JSON 400。
- 设置访问密钥后，配置文件无明文；`?key=` 401，请求头 200。
- 受限笔记列表脱敏，manage/Raw/HEAD 不消费，真实 GET 消费。
- TTL 到期后管理详情 404。
- 分享 HEAD 不消费；同 Cookie 会话可重复读取；新会话达到上限后 410。
- 两个相同内容上传获得不同逻辑 ID、相同 Blob 路径。
- 媒体票据 Range 返回正确内容。
- 删除物理 Blob 后备份 500；重新上传恢复 Blob。
- 第二个进程不能使用同一数据目录。
- 备份后新增对象，恢复后新增对象消失、原对象保留。

结果：通过。

## 第四轮：六目标构建

构建并识别：

- Windows amd64/arm64：PE32+。
- Linux amd64/arm64：静态 ELF。
- macOS amd64/arm64：Mach-O。

结果：通过。

## 第五轮：发布包与真实安装

- 八类安装包/归档结构检查。
- 包内 SHA-256 全部通过。
- Linux amd64 用户级安装、启动、健康检查、卸载。
- 卸载后程序删除，用户数据保留。
- 每轮再次执行核心回归。

结果：通过。

## 覆盖率

| 包 | 语句覆盖率 |
|---|---:|
| API | 52.5% |
| Config | 71.2% |
| Discovery | 32.1% |
| Durable | 66.7% |
| Instance | 82.6% |
| Storage | 49.9% |
| 总计 | 51.9% |

`cmd/omnishare`、desktop、frontend Go 包为 0% 或无行为级覆盖。前端测试仍以契约为主，没有真实浏览器和 Service Worker Cache API 自动化。

## 环境限制

本轮在 Linux 环境真实执行安装和运行。Windows/macOS 只完成交叉编译、二进制格式与安装包结构验证，不能替代实体平台冒烟测试。
