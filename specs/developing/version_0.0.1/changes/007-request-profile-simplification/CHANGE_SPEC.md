# 模型请求配置简化变更说明

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `007-request-profile-simplification` |
| 变更类型 | 缺陷修复 / 配置模型简化 / 页面优化 |
| 优先级 | High |
| 提出日期 | 2026-08-13 |
| 负责人 | 待人工填写 |
| Preflight | 已读取根级与 Proxy 规格索引、项目概览、总体架构、当前 PRD、既有 change、公开 API 文档和当前工作树代码 |

## 2. 背景

当前“模型请求参数”实现了草稿、测试、发布、历史版本和 `model + resolution + scenario` 复合键，但实际管理诉求是简单的即时配置：一个逻辑分辨率只有一份配置，修改立即生效，可以删除。现有复杂模型还造成以下问题：

- 已有配置把场景改成全能参考时，页面提交 PUT，后端因 `scenario` 属于不可变键而报“model、resolution 和 scenario 不可修改”。
- `AIGC 水印`被强制开启，公开 API 的 `false` 和缺省值均被改成 `true`。
- `main_model` 在当前可选模型模式下不生效；`cfg` 未进入 Node 请求；FPS 产品口径应固定 24。
- “逻辑分辨率”实际是公开请求的配置匹配档位，不是直接传给模型的像素尺寸。

## 3. 目标结果

1. 固定 `MiniMax-H3` 下，以 `resolution` 为唯一配置键；`480P`、`768P`、`2K` 每档最多一条。
2. 删除 Profile 版本、状态、测试、发布、复制和场景键；创建/修改成功后立即用于后续新任务。
3. 提供配置删除能力；不检查是否有运行中任务，删除不影响已冻结任务快照。
4. 三种场景由公开请求 `content` 自动判定，共用该分辨率配置和完整比例映射。
5. Profile 页面移除模型、场景、主模型、生成帧率、CFG 和 AIGC 水印。
6. Proxy generation 固定 `fps=24`，模型文件固定跟随 `model_mode`；CFG 不再存在于 Profile。
7. 公开 `aigc_watermark` 可选且默认 `false`，只有 `true` 才追加 watermark 阶段。

## 4. 参数审计结论

| 当前项 | 实际用途 | 结论 |
| --- | --- | --- |
| `model` | 公开 API 固定模型；Profile 中无选择价值 | Profile 页面/schema 删除，服务端固定 `MiniMax-H3` |
| `resolution` | 选择配置，再由 ratio 映射实际宽高 | 保留为唯一键，页面名改为“请求分辨率” |
| `scenario` | 可由公开请求内容判定；所有比例映射本已完整保存 | 从 Profile 删除 |
| `model_mode` | Node 选择高质量/低显存模型 | 保留 |
| `main_model` | 仅 custom 模式使用，当前 Profile 无 custom | 删除，固定跟随模型模式 |
| `fps` | Node 可用，但 Proxy 产品口径固定 24 | Profile 删除，编排固定 24 |
| `steps` | 进入 Node generation parameters | 保留 |
| `cfg` | Node schema 和工作流均无消费点 | 删除 |
| SageAttention / 缓存 | 进入 Node 请求并参与能力匹配 | 保留 |
| 比例基础宽高 | 决定 generation width/height | 保留完整 7 种映射 |
| LoRA / 补帧 / 高清修复 | 进入阶段编排 | 保留 |
| AIGC 水印 | 单次公开请求选项 | 移出 Profile；默认关闭 |

## 5. 数据迁移口径

- 迁移版本预留为 v11，避开已设计的 v9 H3 取消状态和 v10 API Key 管理。
- 每个 `resolution` 仅保留 `updated_at` 最大的一条；时间相同按 `id` 最大的一条保留，其余配置及测试记录删除。
- 保留行转为即时配置，不保留版本、状态、发布人、来源等业务语义。
- 历史 `video_tasks` 保留 `config_snapshot_json/config_hash`；配置删除前将引用该配置的 `profile_id` 置空，再物理删除配置。
- 新任务仍冻结配置 JSON 和阶段 JSON，因此保存/删除只影响后续新任务。

## 6. 影响范围

| 影响项 | 是否影响 | 说明 |
| --- | --- | --- |
| Manager 页面/API | Y | 简化为列表、新建/覆盖、删除 |
| 公共 V2 API | Y | 修正水印默认及 false 语义 |
| Profile 服务/存储 | Y | 删除版本生命周期和测试发布链 |
| 任务匹配/编排 | Y | 按 resolution 查询；固定 FPS/模型；条件水印 |
| 数据库 | Y | v11 收敛唯一键并移除测试/发布业务数据 |
| Node API | N | 保留节点通用 FPS、custom model 和 watermark 能力 |

## 7. 验收标准

- 同一请求分辨率无法产生第二条配置；保存修改后下一条新任务立即使用新值。
- 配置可删除，删除后该分辨率的新任务返回无可用配置；既有任务快照不变。
- 页面不存在版本、状态、测试、发布、复制、场景、主模型、FPS、CFG、水印控件。
- 文生、图生、全能参考均能按相同 resolution 配置冻结正确场景和比例尺寸。
- `aigc_watermark` 省略或 false 不创建水印阶段，true 创建一个水印阶段。
- generation 始终下发 `fps=24` 和 `__follow_model_mode__`。
