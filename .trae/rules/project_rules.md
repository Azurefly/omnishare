# 项目工作与执行约定

## 1. 核心执行流程

本项目所有任务的执行逻辑均遵循六步工作流：

1. **分析 (Analyze)**：深入理解用户需求、项目上下文与现有代码规范，明确目标与边界。
2. **生成 ToDoList (Generate ToDoList)**：在开始具体代码操作前，利用任务管理工具拆解清晰、可追踪的步骤列表。
3. **执行 (Execute)**：按步骤专注实施改动，严格遵守代码规范，避免无关改动。
4. **验证 (Verify)**：执行完整的测试验证（后端 Go 测试、前端测试契约、桌面端发版契约等），确保改动安全无误。
5. **修改 ToDoList (Update ToDoList)**：实时更新任务状态，标明已完成任务并补充新发现的子任务。
6. **Next (流转下一项)**：推进下一项待办，直至整体任务闭环。

## 2. 常用测试与构建验证命令

* **后端 Go 测试**：`cd backend && go test -v ./... && go vet ./...`

* **前端契约与构建**：`cd frontend && node scripts/test.mjs && node scripts/build.mjs`

* **桌面端发版契约**：`cd desktop && node tests/contracts.mjs`

