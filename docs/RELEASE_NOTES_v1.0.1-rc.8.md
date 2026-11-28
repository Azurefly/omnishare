# OmniShare v1.0.1-rc.8

在「工作台 → AI 额度 → 查看额度 → 连接账号」选择来源。OmniShare 直接访问所选供应商、网关或本机官方客户端。验证成功后，工作台显示额度、单位、读数时间，以及供应商提供的重置时间。

| 可连接的来源 | 读数 |
| --- | --- |
| Codex、Claude 桌面版、Antigravity CLI、Kiro CLI | Codex 已登录订阅的周期额度，Claude 桌面版的用量历史，Antigravity 的模型组额度与可用积分，Kiro 的积分明细 |
| DeepSeek、Moonshot 开放平台（中国、国际） | 账户可用余额及服务端提供的充值、现金或赠送明细 |
| Kimi Code、GLM Coding Plan（中国、国际）、MiniMax Coding Plan（中国、国际）、OpenCode Go | 编程套餐返回的滚动、周或月额度 |
| OpenRouter | 当前密钥的支出上限、剩余额度及已用金额 |
| Google Gemini Code Assist、xAI API | Google 项目各模型余量及重置时间，xAI 团队 API 预付账面余额。分别需要有权查看项目的 Google OAuth 访问令牌、具有账单读取权限的 xAI Management Key |
| New API、One API、Sub2API、通用余额 | 用户指定站点的令牌配额、订阅限额或账户余额，以对应接口的单位展示 |

密钥可存入本机系统凭据库，也可选择「仅本次会话」并在退出时清除连接。请为不同账号设置别名。连接默认仅在本机可见。在账号卡打开「共享给绑定设备」后，其他设备可以选择该设备查看上次读数。Kiro 和 Antigravity 的命令行读数只在本机展示，因为其报告无法稳定标识登录账号。配置和密钥不随工作区备份或设备绑定传输。

额度适配参考了岛仔的供应商实现。各服务分别使用本机官方客户端、订阅接口或余额接口。[岛仔额度模块](https://github.com/Renly1994/xiaotang-daozai/blob/1c0c6338d4b71c0ab02c70c55aec74088cd9ce66/src/quota.rs) · [OpenRouter 密钥额度](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key) · [Moonshot 余额](https://platform.moonshot.ai/docs/api/balance)

刷新失败会显示错误并标出上次读数。周期已结束时显示「待刷新」。Claude 记录由官方桌面版更新，Antigravity 需要 1.1.11 或更高版本，Kiro 需要支持非交互 /usage 的 3.1 或更高版本。各供应商的个人订阅、开放平台余额和令牌预算分别显示。

本次使用隔离的供应商响应、临时凭据和独立工作区验证。供应商账户的实际开通状态、地区和权限会影响可用读数。此版本为候选版，macOS 通用包使用临时签名，尚未经过 Apple 公证。
