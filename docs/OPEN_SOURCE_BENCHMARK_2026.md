# OmniShare 开源产品对标分析（2026）

## 1. 对标目的

对标不是拼接功能，而是识别成熟产品反复证明过的用户价值、风险控制和工程底线。OmniShare 的定位不是单纯的 AirDrop 替代品，也不是完整知识库，而是“私有部署、多设备可访问、内容可沉淀、分享可控制”的轻量内容中枢。

## 2. 对标项目

| 项目 | 成熟能力 | 对 OmniShare 的启示 | 本期处理 |
|---|---|---|---|
| LocalSend | 无外部服务器、局域网发现、HTTPS、跨平台、多种分发包 | 默认本地优先；设备发现、端口和防火墙诊断必须产品化 | 已有发现和六平台构建；HTTPS列入P1 |
| Syncthing | 数据不丢、TLS、证书身份、自动同步、跨平台 | 数据安全优先级高于功能数量；必须有回收、版本、冲突和恢复机制 | 本期新增回收站；版本历史和设备证书列入P1 |
| PairDrop | 配对码、二维码、临时房间、接受后传输、批量进度 | 临时分享与可信设备应独立于全局管理密钥 | 本期新增独立分享令牌；配对码/二维码列入P1 |
| croc | PAKE、端到端加密、中继、断点续传、多文件 | 跨网络大文件不能只靠普通 HTTP 上传 | 分片、恢复、中继、E2EE列入P1/P2 |
| Pingvin Share | 链接有效期、访问次数、密码、反向分享、病毒扫描 | 分享必须可撤销、可限制、可审计 | 已实现有效期/次数/撤销/审计；密码和扫描列入P1 |
| Joplin | 离线优先、全文搜索、E2EE、导入导出、插件 | 内容必须可迁移，离线可用；不能锁死在私有格式 | 已有备份；Markdown/JSON导入导出列入P1 |
| TriliumNext | 版本历史、公开发布、保护笔记、移动端、REST API | 文档要有历史、保护、链接与自动化 | 已有冲突保护和分享；版本历史列入P1 |
| KDE Connect | 可信设备配对、共享剪贴板、系统集成 | 桌面程序不能只是浏览器快捷方式，后续需托盘、右键和剪贴板入口 | 列入P1桌面增强 |

## 3. 关键结论

### 3.1 数据安全是第一产品功能

用户可以接受少一个视图，但不能接受误删后无法恢复。删除、覆盖、同步冲突和备份恢复必须成为显式业务流程，而不是底层实现细节。

### 3.2 全局密钥不能承担全部分享职责

全局访问密钥适合管理节点，不适合发给临时接收者。分享必须按对象授权，具有到期、次数限制、撤销和审计能力。

### 3.3 “多端”不仅是能编译

真正的多端包括安装、升级、自动启动、托盘、右键菜单、系统分享入口、签名、故障诊断和平台差异测试。当前 v1.2.0 完成六平台构建和基础安装包，尚未完成全部原生集成。

### 3.4 局域网成功率决定第一印象

发现失败、公共网络防火墙、AP 隔离、多网卡、IPv6、端口占用和系统权限需要可诊断。后续应增加网络自检向导，而不是只写在文档里。

### 3.5 必须明确“存储中枢”和“即时传输”的边界

当前 OmniShare 以节点存储为中心；PairDrop、croc 更偏即时点对点传输。后续可以增加“直传模式”，但不能破坏现有内容沉淀、审计和回收机制。

## 4. 来源

- LocalSend: https://github.com/localsend/localsend
- LocalSend Protocol: https://github.com/localsend/protocol
- Syncthing: https://github.com/syncthing/syncthing
- PairDrop: https://github.com/schlagmichdoch/PairDrop
- croc: https://github.com/schollz/croc
- Pingvin Share: https://github.com/stonith404/pingvin-share
- Joplin: https://github.com/laurent22/joplin
- TriliumNext: https://github.com/TriliumNext/Trilium
- KDE Connect: https://github.com/KDE/kdeconnect-kde
