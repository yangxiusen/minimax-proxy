# 规范文档导航

## 项目状态

- 项目：`minimax-h3-tc`
- 类型：MiniMax-H3 接口中转与协议适配 API 服务
- 当前阶段：`v0.0.1` 本地开发与自动化验证已完成，待真实私有服务人工联调
- 活跃版本：[`v0.0.1`](developing/version_0.0.1/PRD.md)

## 目录

- [项目 Wiki](project_wiki/README.md)：稳定的项目知识与约束。
- [产品需求](product/)：长期产品资产和版本需求。
- [开发中](developing/)：各版本交付快照。
- [技术实现方案](developing/version_0.0.1/TECHNICAL_SOLUTION.md)：架构、调度、上游适配与恢复策略。
- [数据库设计](developing/version_0.0.1/DATABASE_DESIGN.md)：SQLite 表、索引与事务约束。
- [接口设计](developing/version_0.0.1/API_INTERFACE.md)：4 个官方 V2 接口及模块文档。
- [测试与验收](developing/version_0.0.1/TEST_ACCEPTANCE.md)：自动化范围与真实上游联调清单。
- [开发任务](developing/version_0.0.1/task.md)：按依赖排序的实施步骤和验证命令。
- [任务生命周期闭环变更](developing/version_0.0.1/changes/002-task-lifecycle-closure/CHANGE_SPEC.md)：模型任务对账、一次重试、管理中止/删除和视频访问。
- [管理后台与节点配置变更](developing/version_0.0.1/changes/004-manager-node-configuration/CHANGE_SPEC.md)：`/manager` 管理后台、模型节点 SQLite 配置与动态运行时。
- [归档](archive/)：已完成版本与历史资料。

`docs/` 是外部参考资料，只用于分析，不得被运行时代码直接引用或依赖。
