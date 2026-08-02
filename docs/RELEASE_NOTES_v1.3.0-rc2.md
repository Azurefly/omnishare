# OmniShare v1.3.0-rc2 发布说明

## 发布结论

本版本是针对 `v1.3.0-rc1` Windows 启动器故障的修正版，仍定位为 **Release Candidate**。安全、存储和分享能力沿用 RC1，本次重点修复 Windows ZIP 解压后无法通过 `start-omnishare.cmd` 启动的问题。

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
- 前端语法、契约与确定性构建。
- Go `gofmt`、`go vet` 和 Race Detector。
- Windows 启动器 BOM、调用方式和禁用 `^|` 的静态契约。
- Windows ZIP 中 `.cmd`、`.ps1` 文件存在性和内容检查。
- Windows、Linux、macOS 的 AMD64/ARM64 六目标构建。
- 发布包 SHA-256 校验。

## 仍然存在的限制

本版本仍是 RC，不适用于涉密、高合规、公网直接暴露、高并发写入或要求角色权限、自动 TLS、代码签名、SBOM 和来源证明的生产环境。
