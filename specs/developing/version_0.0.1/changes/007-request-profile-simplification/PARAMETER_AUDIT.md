# 模型请求参数契约审计

## 1. 公开 API

| 参数 | 结论 |
| --- | --- |
| `model/content/resolution/duration/ratio/callback_url` | 均有实际校验或执行用途，保留 |
| `aigc_watermark` | 当前被强制 true；修正为可选、默认 false、逐请求生效 |

## 2. Profile 到 Node

| Profile 字段 | 消费位置 | 结论 |
| --- | --- | --- |
| `model_mode` | Node `model_mode` | 保留 |
| `main_model` | 只有 `custom` 模式使用，当前不可达 | 删除 |
| `fps` | Node generation | 固定 24，不开放配置 |
| `steps` | Node generation | 保留 |
| `cfg` | 无消费点 | 删除 |
| `sage_attention` | Node generation / capability | 保留 |
| `cache_mode` | Node generation / capability | 保留 |
| `ratios` | generation/restoration 尺寸 | 保留全部比例，不再按 Profile 场景裁剪 |
| `loras/interpolation/restoration` | generation 和后处理阶段 | 保留 |
| `watermark` | 当前强制独立阶段 | 移出 Profile，由公开请求决定 |

## 3. 请求分辨率含义

公开任务的 `resolution` 用于读取唯一配置，之后按任务 `ratio` 从配置中取 `base_width/base_height` 下发模型。它是配置匹配档位，不是像素尺寸；像素尺寸由比例映射维护。

## 4. 报错根因

当前 Profile 主键语义包含 `scenario`，而页面允许修改已保存 Profile 的场景，`Service.Update` 随后拒绝复合键变化。新设计彻底删除 Profile 的场景维度，由任务内容判定 t2va/i2va/r2va，因此不再存在“改成全能参考配置”的操作或报错。
