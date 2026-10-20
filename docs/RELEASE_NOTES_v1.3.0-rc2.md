# OmniShare v1.3.0-rc2 发布说明

## 发布结论

本版本是针对 `v1.3.0-rc1` Windows 启动器故障的修正版，仍定位为 **Release Candidate**。安全、存储和分享能力沿用 RC1，本次重点修复 Windows ZIP 解压后无法通过 `start-omnishare.cmd` 启动的问题，并同时合入一轮性能与隐藏缺陷整改。

## 修复的问题

RC1 的 `start-omnishare.cmd` 将 PowerShell 管道写为 `^|`，但该符号位于双引号包裹的 `-Command` 字符串中。CMD 没有移除 `^`，PowerShell 最终把它作为位置参数传给 `Get-CimInstance`，产生：

```text
Get-CimInstance : 找不到接受实际参数“^”的位置形式参数。
```

RC2 已完成以下处理：

- `start-omnishare.cmd` 不再内嵌 PowerShell 源码。
- 新增独立 `start-omnishare.ps1` 执行进程检测和启动。
- 按 `omnishare.exe` 完整路径判断当前安装实例是否已经运行。
- 使用 `%APPDATA%\OmniShare` 作为启动脚本的数据目录。
- Windows AMD64 与 ARM64 ZIP 均包含 `.cmd` 和 `.ps1` 两个启动文件。
- 发布构建增加启动器源码与 ZIP 包内容回归检查。
- 发布打包根据 `VERSION` 动态选择发布说明文件。

## 同时合入的性能与隐藏缺陷修复

本版本还包含一轮性能与隐藏缺陷整改，完整记录见 `docs/PERF_AND_HIDDEN_BUG_AUDIT_v1.3.0-rc2.md`。其中最值得注意的是三个**静默失效**问题——它们不报错，只是让功能悄悄不生效：

- **内嵌前端副本落后于源码**：Go 二进制实际分发 `backend/internal/frontend/dist`，但此前只更新了 `frontend/web`，用户运行的是旧 JS。现已同步，并新增逐字节漂移测试；CI 中已有的 `git diff --exit-code` 检查也恢复为通过。
- **桌面看门狗退避被自身抵消**：重启失败后指数退避被重置，仍按 5 秒轮询并持续写日志。现已按失败次数正常递增。
- **健康探测载荷过大**：看门狗原先每 5 秒下载完整首页，改为探测 `/api/v1/health`，单次 22,173 字节降至 71 字节，按周期折算日流量约 383 MB → 1.2 MB。

性能方面：

- 请求热路径去掉全局互斥锁。`isLocalAddress` 在路由前对每个请求执行，原先用互斥锁保护接口地址缓存，等于串行化全部并发请求；改为原子快照 + 单飞刷新后微基准 130.4 → 20–35 ns/op，且不再有串行化点。
- 设备列表复用同一接口快照，省去每轮询一次的 `getifaddrs` 系统调用与 UDP dial。
- 鉴权失败表清理改为按分钟节流，并限制单次淘汰上限。
- 清理线程无事可做时不再克隆并重写整个数据集。
- 前端轮询增加在途去重，后台标签页暂停轮询。

实测（Apple M4，真实二进制）：空闲 30 秒 CPU 占用 0.01%；8 并发下 23,784 rps，单请求 CPU 约 30.9 µs。

> 说明：桌面端 Rust 改动（看门狗退避与健康探测）在本次构建环境未执行 `cargo test`，仅完成源码级复核与契约测试覆盖。建议在具备 Rust 工具链的环境补跑后再用于生产。

## Windows 使用方式

解压对应架构的 ZIP 后，双击：

```text
start-omnishare.cmd
```

不要单独复制该文件；它需要与以下文件保持在同一目录：

```text
omnishare.exe
start-omnishare.ps1
```

也可以执行 `install.ps1` 完成用户级安装。

## 数据兼容性

- RC2 不修改 RC1 的数据格式。
- 现有 `%APPDATA%\OmniShare` 数据目录可以直接继续使用。
- 不需要删除配置、重新初始化访问密钥或迁移上传文件。

## 验证范围

- 版本一致性检查。
- 前端语法、契约与确定性构建，含内嵌副本逐字节漂移检查。
- Go `gofmt`、`go vet` 和 Race Detector。
- 健康检查响应契约（桌面看门狗依赖的字面量标记与载荷上限）。
- Windows 启动器 BOM、调用方式和禁用 `^|` 的静态契约。
- Windows ZIP 中 `.cmd`、`.ps1` 文件存在性和内容检查。
- Windows、Linux、macOS 的 AMD64/ARM64 六目标构建。
- 发布包 SHA-256 校验。

## 仍然存在的限制

本版本仍是 RC，不适用于涉密、高合规、公网直接暴露、高并发写入或要求角色权限、自动 TLS、代码签名、SBOM 和来源证明的生产环境。
