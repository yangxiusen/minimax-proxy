# `v0.0.1` 交付 PRD 快照

## 1. 版本目标

交付一个基于 Go、SQLite 和 Docker 的 MiniMax H3 V2 API 中转服务。它对外提供 `/v2` 异步视频生成、任务查询/列表和取消/删除，对内调度多个独占的私有 MiniMax-H3 实例。

本快照的权威详细需求为：[version_0.0.1.md](../../product/requirements/version_0.0.1.md)。后续版本内变更必须进入 `changes/`，不得直接形成不可追踪的口头差异。

## 2. 本版交付范围

- 多 Bearer API Key 及资源隔离。
- 官方 V2 `content + role` 参数支持文生、图生和多模态参考，并适配 Gradio 32 位参数。
- 本地 FIFO、队首 3 位保护、每 Key 10/全局 100 未结束任务默认上限。
- 多上游实例并行，每实例单任务，独占提交和 Gallery 差异绑定。
- `task_id -> task.content.url` 主动查询流程。
- 可选 `Idempotency-Key`、SQLite 恢复和 7 天逻辑删除。
- YAML + 环境变量配置、Docker 单容器和关键链路日志。

## 3. 明确不交付

- 页面与原型、H3-Context-IR、视频再生成、回调、计费、优先级队列、代理下载、对象存储、上游物理文件删除。

## 4. 交付门禁

- 自动化测试通过不代表真实私有服务联调、生产性能或业务验收完成。
- Gallery 结构、媒体兼容矩阵、公开下载地址和多实例并行必须人工验证。
- 技术方案需明确 Gallery 解析、V2 到私有参数映射及官方能力差异，再进入开发。

## 5. 追溯关系

- 主 PRD：`specs/product/PRD_MAIN.md`
- 版本需求：`specs/product/requirements/version_0.0.1.md`
- 变更目录：`specs/developing/version_0.0.1/changes/`
- 原型快照：`specs/developing/version_0.0.1/PROTOTYPE_SNAPSHOT.md`
