# 视频生成系统 API 中文文档

## 1. 基本信息

### API 服务地址

```text
http://192.168.1.200:7861/
```

### API 基础路径

```text
http://192.168.1.200:7861/gradio_api/call/
```

### 接口数量

当前系统共提供：

```text
17 个 API 接口
```

---

# 2. API 调用方式

Gradio API 的一次完整调用通常需要发送两个请求：

1. 使用 `POST` 请求提交参数。
2. POST 请求返回一个 `EVENT_ID`。
3. 使用 `GET` 请求并携带 `EVENT_ID` 获取执行结果。

完整流程如下：

```text
POST /gradio_api/call/{API名称}
                ↓
          返回 EVENT_ID
                ↓
GET /gradio_api/call/{API名称}/{EVENT_ID}
                ↓
          返回最终执行结果
```

## 2.1 POST 请求格式

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/API名称 \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      参数1,
      参数2
    ]
  }'
```

POST 请求通常返回：

```json
{
  "event_id": "xxxxxxxxxxxxxxxx"
}
```

## 2.2 GET 请求格式

```bash
curl -N \
  http://192.168.1.200:7861/gradio_api/call/API名称/EVENT_ID
```

其中：

```text
EVENT_ID
```

需要替换成第一次 POST 请求返回的事件 ID。

## 2.3 文档中的组合命令说明

原始文档使用以下命令将 POST 和 GET 合并：

```bash
curl -X POST http://192.168.1.200:7861/gradio_api/call/API名称 \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N http://192.168.1.200:7861/gradio_api/call/API名称/$EVENT_ID
```

其中：

```bash
awk -F'"' '{ print $4}'
```

用于从 POST 响应中提取 `EVENT_ID`。

```bash
curl -N
```

用于保持连接并以流式方式接收 Gradio 返回结果。

---

# 3. API 接口目录

| 序号 | API 名称 | 作用 |
|---:|---|---|
| 1 | `/check_and_get_video_1` | 查询视频结果、任务状态、队列、系统资源和日志 |
| 2 | `/lambda7` | 根据快速提示词更新完整提示词 |
| 3 | `/save_quick_prompt` | 保存快速提示词 |
| 4 | `/delete_quick_prompt` | 删除快速提示词 |
| 5 | `/update_mode_visibility` | 根据生成模式更新界面和提示词 |
| 6 | `/lambda8` | 根据模型模式更新相关配置 |
| 7 | `/preview_batch_images` | 预览批量上传的图片 |
| 8 | `/refresh_model_choices` | 刷新自定义模型列表 |
| 9 | `/swap_width_height` | 交换视频宽度和高度 |
| 10 | `/lambda9` | 生成或重置 Seed |
| 11 | `/save_ui_config` | 保存当前界面配置 |
| 12 | `/submit_minimax_from_slots` | 提交视频生成任务 |
| 13 | `/interrupt_current_task` | 中断当前任务 |
| 14 | `/clear_pending_queue` | 清空等待队列 |
| 15 | `/check_and_get_video` | 查询视频结果和任务状态 |
| 16 | `/open_output_folder` | 打开输出目录 |
| 17 | `/build_video_gallery` | 重新构建视频结果列表 |

---

# 4. 接口详细说明

## 4.1 查询视频生成结果

### API 名称

```text
/check_and_get_video_1
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/check_and_get_video_1
```

### 功能说明

查询当前视频生成结果及系统运行状态，包括：

- 视频生成结果
- 当前任务状态
- 队列状态
- 系统资源占用
- 实时日志

该接口不需要传入参数。

### 调用统计

```text
请求次数：76
成功率：100%
p50：12 ms
p90：13 ms
p99：14 ms
```

这里的时间通常只是接口函数本身的响应时间，不代表视频生成时间。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/check_and_get_video_1 \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/check_and_get_video_1/$EVENT_ID
```

### 返回值

返回包含 5 个元素的列表：

| 索引 | 类型 | 说明 |
|---:|---|---|
| `[0]` | Gallery 数据 | 显示在“🎬 生成结果”组件中的视频结果 |
| `[1]` | string | 显示在“📡 当前任务状态”文本框中的内容 |
| `[2]` | string | 显示在“📋 队列状态”文本框中的内容 |
| `[3]` | string | 显示在“💻 系统资源占用”文本框中的内容 |
| `[4]` | string | 显示在“📝 实时日志”文本框中的内容 |

---

## 4.2 选择快速提示词

### API 名称

```text
/lambda7
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/lambda7
```

### 功能说明

根据“快速提示词”下拉框选中的内容，返回对应的完整视频与音频提示词。

`lambda7` 是 Gradio 为匿名函数自动生成的接口名称，并不代表具体业务名称。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | “快速提示词”下拉框当前选中的值 |

### 请求示例

```json
{
  "data": [
    "快速提示词内容"
  ]
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/lambda7 \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "快速提示词内容"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/lambda7/$EVENT_ID
```

### 返回值

返回 1 个字符串：

```text
完整的视频与音频提示词
```

该结果显示在：

```text
“视频与音频提示词”文本框
```

---

## 4.3 保存快速提示词

### API 名称

```text
/save_quick_prompt
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/save_quick_prompt
```

### 功能说明

将当前“视频与音频提示词”文本框中的内容保存为快速提示词。

保存成功后，接口会返回更新后的快速提示词下拉框数据。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | 当前“视频与音频提示词”文本框中的内容 |

### 请求示例

```json
{
  "data": [
    "一艘巨型宇宙战舰缓慢驶过银河，电影级运镜，史诗感音乐。"
  ]
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/save_quick_prompt \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "一艘巨型宇宙战舰缓慢驶过银河，电影级运镜，史诗感音乐。"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/save_quick_prompt/$EVENT_ID
```

### 返回值

返回 1 个元素：

```text
更新后的“快速提示词”下拉框数据
```

---

## 4.4 删除快速提示词

### API 名称

```text
/delete_quick_prompt
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/delete_quick_prompt
```

### 功能说明

删除“快速提示词”下拉框中当前选中的提示词。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | 需要删除的快速提示词 |

### 请求示例

```json
{
  "data": [
    "需要删除的快速提示词内容"
  ]
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/delete_quick_prompt \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "需要删除的快速提示词内容"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/delete_quick_prompt/$EVENT_ID
```

### 返回值

返回 1 个元素：

```text
删除后更新的“快速提示词”下拉框数据
```

---

## 4.5 更新生成模式对应的界面状态

### API 名称

```text
/update_mode_visibility
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/update_mode_visibility
```

### 功能说明

当用户切换“生成模式”时，更新相关界面组件的可见状态，并可能更新默认提示词内容。

例如：

- 文生视频
- 图生视频
- 首尾帧视频
- 全能参考视频
- 批量图生视频

具体支持的模式取决于程序内部配置。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | “生成模式”单选框当前值 |

### 请求示例

```json
{
  "data": [
    "文生视频"
  ]
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/update_mode_visibility \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "文生视频"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/update_mode_visibility/$EVENT_ID
```

### 返回值

返回 1 个字符串。

该字符串显示在：

```text
“视频与音频提示词”文本框
```

---

## 4.6 更新模型模式

### API 名称

```text
/lambda8
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/lambda8
```

### 功能说明

当用户修改“模型模式”时，更新与模型相关的配置或界面组件。

`lambda8` 是 Gradio 为匿名函数自动生成的接口名称。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | “模型模式”单选框当前值 |

### 请求示例

```json
{
  "data": [
    "high_quality"
  ]
}
```

其中：

```text
high_quality
```

表示高质量模式。

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/lambda8 \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "high_quality"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/lambda8/$EVENT_ID
```

### 返回值

返回 1 个元素。

原始自动文档没有标明该元素对应的具体组件，可能是 Gradio 组件更新对象或内部配置结果。

---

## 4.7 预览批量图片

### API 名称

```text
/preview_batch_images
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/preview_batch_images
```

### 功能说明

读取用户在“上传批量图片”组件中上传的文件，并在“批量图片预览”Gallery 中显示图片。

### 请求参数

接受 1 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | any | 是 | “上传批量图片”文件组件中的文件列表 |

### 原始自动示例

```python
[
  handle_file(
    "https://github.com/gradio-app/gradio/raw/main/test/test_files/sample_file.pdf"
  )
]
```

原始文档使用 PDF 作为自动测试文件，但实际业务中通常应该传入：

```text
JPG、JPEG、PNG、WEBP 等图片文件
```

### 请求结构示例

```json
{
  "data": [
    [
      {
        "path": "/tmp/gradio/example.png",
        "meta": {
          "_type": "gradio.FileData"
        }
      }
    ]
  ]
}
```

### 返回值

返回 1 个元素：

```text
“批量图片预览”Gallery 组件所需的数据
```

---

## 4.8 刷新模型选择列表

### API 名称

```text
/refresh_model_choices
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/refresh_model_choices
```

### 功能说明

刷新以下两个模型下拉框中的可用模型：

1. 自定义文生/图生主模型
2. 自定义全能参考主模型

### 请求参数

接受 2 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | 当前选择的“自定义文生/图生主模型” |
| `[1]` | string | 是 | 当前选择的“自定义全能参考主模型” |

### 请求示例

```json
{
  "data": [
    "__follow_model_mode__",
    "__follow_model_mode__"
  ]
}
```

其中：

```text
__follow_model_mode__
```

表示不单独指定模型，而是跟随当前“模型模式”的默认模型。

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/refresh_model_choices \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "__follow_model_mode__",
      "__follow_model_mode__"
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/refresh_model_choices/$EVENT_ID
```

### 返回值

返回包含 3 个元素的列表：

| 索引 | 类型 | 说明 |
|---:|---|---|
| `[0]` | string 或组件更新对象 | 更新后的“自定义文生/图生主模型”下拉框 |
| `[1]` | string 或组件更新对象 | 更新后的“自定义全能参考主模型”下拉框 |
| `[2]` | string | 显示在“📡 当前任务状态”文本框中的内容 |

---

## 4.9 交换视频宽度和高度

### API 名称

```text
/swap_width_height
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/swap_width_height
```

### 功能说明

交换视频宽度和高度，可用于快速切换横屏和竖屏。

例如：

```text
832 × 480
```

交换后变成：

```text
480 × 832
```

### 请求参数

接受 2 个参数。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | any | 是 | 当前视频宽度 |
| `[1]` | any | 是 | 当前视频高度 |

### 请求示例

```json
{
  "data": [
    832,
    480
  ]
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/swap_width_height \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      832,
      480
    ]
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/swap_width_height/$EVENT_ID
```

### 返回值

返回包含 2 个元素的列表：

| 索引 | 说明 |
|---:|---|
| `[0]` | 交换后的视频宽度 |
| `[1]` | 交换后的视频高度 |

返回示例：

```json
[
  480,
  832
]
```

---

## 4.10 生成或重置 Seed

### API 名称

```text
/lambda9
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/lambda9
```

### 功能说明

生成、随机化或重置视频生成所使用的 Seed。

`lambda9` 是 Gradio 为匿名函数自动生成的接口名称。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/lambda9 \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/lambda9/$EVENT_ID
```

### 返回值

返回 1 个数值，并显示在：

```text
“Seed”数字输入框
```

常见约定：

```text
Seed = -1
```

表示每次生成时随机选择 Seed。

固定 Seed 可提高结果的可复现性，但视频生成通常不能保证完全一致。

---

## 4.11 保存界面配置

### API 名称

```text
/save_ui_config
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/save_ui_config
```

### 功能说明

保存当前视频生成界面中的配置。

该接口不会直接开始生成视频，主要用于持久化当前参数。

### 请求参数

接受 14 个参数，顺序必须严格保持一致。

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | 生成模式 |
| `[1]` | string | 是 | 视频与音频提示词 |
| `[2]` | string | 是 | 批量图生视频模式 |
| `[3]` | string | 是 | 批量提示词，每行一个 |
| `[4]` | string | 是 | 参考图处理尺寸 |
| `[5]` | string | 是 | 模型模式 |
| `[6]` | string | 是 | 自定义文生/图生主模型 |
| `[7]` | string | 是 | 自定义全能参考主模型 |
| `[8]` | boolean | 是 | 是否启用 EasyCache 加速 |
| `[9]` | any | 是 | 视频宽度 |
| `[10]` | any | 是 | 视频高度 |
| `[11]` | number | 是 | 视频时长，单位为秒 |
| `[12]` | number | 是 | 采样步数 |
| `[13]` | any | 是 | Seed |

### 请求示例

```json
{
  "data": [
    "文生视频",
    "一支庞大的宇宙舰队缓慢穿越银河。",
    "批量单图视频",
    "",
    "match",
    "high_quality",
    "__follow_model_mode__",
    "__follow_model_mode__",
    true,
    832,
    480,
    5,
    20,
    -1
  ]
}
```

### 参数解释

#### 生成模式

```text
文生视频
```

表示只使用文字提示词生成视频。

#### 批量图生视频模式

```text
批量单图视频
```

表示批量上传图片时，每张图片单独生成一个视频。

#### 参考图处理尺寸

```text
match
```

通常表示让参考图尺寸匹配目标视频尺寸，具体行为取决于程序实现。

#### 模型模式

```text
high_quality
```

表示高质量模式。

#### 自定义模型

```text
__follow_model_mode__
```

表示跟随当前模型模式，不单独指定模型。

#### EasyCache

```json
true
```

表示启用 EasyCache 加速。

#### 视频尺寸

```text
832 × 480
```

#### 视频时长

```text
5 秒
```

#### 采样步数

```text
20
```

#### Seed

```text
-1
```

表示随机 Seed。

### 返回值

返回 1 个字符串：

```text
配置保存状态
```

显示在：

```text
“📡 当前任务状态”文本框
```

---

# 5. 核心视频生成接口

## 5.1 提交视频生成任务

### API 名称

```text
/submit_minimax_from_slots
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/submit_minimax_from_slots
```

### 功能说明

这是系统中最核心的视频生成接口。

它可以提交以下类型的数据：

- 文本提示词
- 批量图片
- 首帧图片
- 尾帧图片
- 最多 9 张参考图片
- 最多 3 个参考视频
- 最多 3 个独立参考音频
- 视频宽度和高度
- 视频时长
- 采样步数
- Seed
- 模型模式
- 自定义模型
- EasyCache 加速配置

该接口通常只负责提交任务和加入队列，不会直接返回最终生成的视频。

最终视频需要通过以下接口轮询获取：

```text
/check_and_get_video
```

或者：

```text
/check_and_get_video_1
```

---

## 5.2 请求参数

接口接受 32 个参数。

参数顺序必须严格保持一致，不能随意增删或调整位置。

### 基础参数

| 索引 | 类型 | 必填 | 说明 |
|---:|---|---|---|
| `[0]` | string | 是 | 生成模式 |
| `[1]` | string | 是 | 视频与音频提示词 |
| `[2]` | string | 是 | 批量图生视频模式 |
| `[3]` | any | 是 | 上传批量图片 |
| `[4]` | string | 是 | 批量提示词，每行一个 |
| `[5]` | Blob / File / Buffer | 是 | 首帧图片，界面标记为必填 |
| `[6]` | Blob / File / Buffer | 是 | 尾帧图片，界面标记为选填 |
| `[7]` | string | 是 | 参考图处理尺寸 |
| `[8]` | string | 是 | 模型模式 |
| `[9]` | string | 是 | 自定义文生/图生主模型 |
| `[10]` | string | 是 | 自定义全能参考主模型 |
| `[11]` | boolean | 是 | 是否启用 EasyCache 加速 |
| `[12]` | any | 是 | 视频宽度 |
| `[13]` | any | 是 | 视频高度 |
| `[14]` | number | 是 | 视频时长，单位为秒 |
| `[15]` | number | 是 | 采样步数 |
| `[16]` | any | 是 | Seed |

### 参考图片参数

| 索引 | 类型 | 界面含义 |
|---:|---|---|
| `[17]` | Blob / File / Buffer | 参考图片 `<Picture 1>`，选填 |
| `[18]` | Blob / File / Buffer | 参考图片 `<Picture 2>`，选填 |
| `[19]` | Blob / File / Buffer | 参考图片 `<Picture 3>`，选填 |
| `[20]` | Blob / File / Buffer | 参考图片 `<Picture 4>`，选填 |
| `[21]` | Blob / File / Buffer | 参考图片 `<Picture 5>`，选填 |
| `[22]` | Blob / File / Buffer | 参考图片 `<Picture 6>`，选填 |
| `[23]` | Blob / File / Buffer | 参考图片 `<Picture 7>`，选填 |
| `[24]` | Blob / File / Buffer | 参考图片 `<Picture 8>`，选填 |
| `[25]` | Blob / File / Buffer | 参考图片 `<Picture 9>`，选填 |

图片输入时必须提供：

```text
本地路径或 URL
```

图片输出时始终会提供服务器路径。

### 参考视频参数

| 索引 | 类型 | 界面含义 |
|---:|---|---|
| `[26]` | any | 参考视频 `<Video 1>`，选填 |
| `[27]` | any | 参考视频 `<Video 2>`，选填 |
| `[28]` | any | 参考视频 `<Video 3>`，选填 |

### 参考音频参数

| 索引 | 类型 | 界面含义 |
|---:|---|---|
| `[29]` | any | 独立参考音频 `<Audio 1>`，选填 |
| `[30]` | any | 独立参考音频 `<Audio 2>`，选填 |
| `[31]` | any | 独立参考音频 `<Audio 3>`，选填 |

---

## 5.3 Gradio FileData 文件对象

Gradio 使用 `FileData` 对象表示上传的图片、视频和音频。

常见结构如下：

```json
{
  "path": "/tmp/gradio/example.png",
  "url": "http://192.168.1.200:7861/gradio_api/file=/tmp/gradio/example.png",
  "size": 123456,
  "orig_name": "example.png",
  "mime_type": "image/png",
  "is_stream": false,
  "meta": {
    "_type": "gradio.FileData"
  }
}
```

字段说明：

| 字段 | 说明 |
|---|---|
| `path` | 文件在服务器上的路径 |
| `url` | 文件对应的标准化访问 URL |
| `size` | 文件大小，单位为字节 |
| `orig_name` | 文件上传前的原始文件名 |
| `mime_type` | 文件 MIME 类型 |
| `is_stream` | 文件是否为流式数据 |
| `meta` | Gradio 内部元数据，不应自行修改 |

最简文件对象通常可以写成：

```json
{
  "path": "https://example.com/image.png",
  "meta": {
    "_type": "gradio.FileData"
  }
}
```

服务器必须能够访问该 URL。

---

## 5.4 “选填参数”为什么仍显示 Required

自动文档中，参考图片、参考视频和参考音频虽然在界面上标记为“选填”，但 API 文档仍然显示：

```text
Required
```

这通常表示：

```text
参数位置必须存在，但参数内容可以为空。
```

由于 `/submit_minimax_from_slots` 接受固定的 32 个位置参数，即使某些文件不使用，也应该保留对应参数位置。

未使用的可选文件一般可尝试传：

```json
null
```

例如：

```json
[
  null,
  null,
  null
]
```

具体是否支持 `null`，取决于程序后端对空文件参数的处理方式。

---

## 5.5 文生视频请求示例

以下示例不上传任何图片、视频或音频。

```json
{
  "data": [
    "文生视频",
    "一支庞大的宇宙舰队缓慢穿越银河，紫蓝色星云在背景中流动，电影级运镜，史诗感音乐。",
    "批量单图视频",
    null,
    "",
    null,
    null,
    "match",
    "high_quality",
    "__follow_model_mode__",
    "__follow_model_mode__",
    true,
    832,
    480,
    5,
    20,
    -1,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null
  ]
}
```

对应 cURL：

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/submit_minimax_from_slots \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "文生视频",
      "一支庞大的宇宙舰队缓慢穿越银河，紫蓝色星云在背景中流动，电影级运镜，史诗感音乐。",
      "批量单图视频",
      null,
      "",
      null,
      null,
      "match",
      "high_quality",
      "__follow_model_mode__",
      "__follow_model_mode__",
      true,
      832,
      480,
      5,
      20,
      -1,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null
    ]
  }'
```

---

## 5.6 图生视频请求示例

以下示例使用一张首帧图片。

```json
{
  "data": [
    "图生视频",
    "镜头缓慢向前推进，画面中的宇宙舰队向银河深处航行，星云缓慢流动。",
    "批量单图视频",
    null,
    "",
    {
      "path": "https://example.com/start-frame.png",
      "meta": {
        "_type": "gradio.FileData"
      }
    },
    null,
    "match",
    "high_quality",
    "__follow_model_mode__",
    "__follow_model_mode__",
    true,
    832,
    480,
    5,
    20,
    -1,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null,
    null
  ]
}
```

---

## 5.7 使用参考图片、视频和音频

提示词中可通过以下标签引用素材：

```text
<Picture 1>
<Picture 2>
...
<Picture 9>

<Video 1>
<Video 2>
<Video 3>

<Audio 1>
<Audio 2>
<Audio 3>
```

例如：

```text
使用 <Picture 1> 作为角色和战舰外观参考。

镜头运动参考 <Video 1> 的推进速度和运镜方式。

背景音乐风格参考 <Audio 1>，但保留新的战舰引擎音效。
```

标签与参数位置必须对应。

例如：

```text
<Picture 1>
```

对应参数：

```text
data[17]
```

```text
<Video 1>
```

对应参数：

```text
data[26]
```

```text
<Audio 1>
```

对应参数：

```text
data[29]
```

---

## 5.8 返回值

接口返回包含 2 个元素的列表：

| 索引 | 类型 | 说明 |
|---:|---|---|
| `[0]` | string | 显示在“📡 当前任务状态”文本框中的内容 |
| `[1]` | string | 显示在“📋 队列状态”文本框中的内容 |

可能的返回内容包括：

```text
任务已提交
```

```text
任务已加入队列
```

```text
当前排队位置：1
```

具体文本取决于程序内部实现。

---

# 6. 任务控制接口

## 6.1 中断当前任务

### API 名称

```text
/interrupt_current_task
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/interrupt_current_task
```

### 功能说明

停止或中断当前正在执行的视频生成任务。

该接口通常用于停止正在运行的任务。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/interrupt_current_task \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/interrupt_current_task/$EVENT_ID
```

### 返回值

返回 1 个字符串：

```text
当前任务中断状态
```

该结果显示在：

```text
“📡 当前任务状态”文本框
```

---

## 6.2 清空等待队列

### API 名称

```text
/clear_pending_queue
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/clear_pending_queue
```

### 功能说明

清除当前尚未开始执行的排队任务。

通常：

- 已经开始执行的任务不会自动停止。
- 等待执行的任务会被移出队列。
- 如需停止当前任务，应调用 `/interrupt_current_task`。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/clear_pending_queue \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/clear_pending_queue/$EVENT_ID
```

### 返回值

返回 1 个字符串：

```text
队列清理结果
```

该结果显示在：

```text
“📡 当前任务状态”文本框
```

---

# 7. 查询任务和结果接口

## 7.1 查询视频结果

### API 名称

```text
/check_and_get_video
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/check_and_get_video
```

### 功能说明

查询当前任务执行情况，并尝试获取已经生成的视频结果。

该接口适合在提交生成任务后进行定时轮询。

例如：

```text
每隔 2～5 秒调用一次
```

直到任务完成或返回视频结果。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/check_and_get_video \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/check_and_get_video/$EVENT_ID
```

### 返回值

返回包含 5 个元素的列表：

| 索引 | 类型 | 说明 |
|---:|---|---|
| `[0]` | Gallery 数据 | 显示在“🎬 生成结果”组件中的视频 |
| `[1]` | string | 当前任务状态 |
| `[2]` | string | 当前队列状态 |
| `[3]` | string | 系统资源占用 |
| `[4]` | string | 实时日志 |

---

## 7.2 `/check_and_get_video` 与 `/check_and_get_video_1` 的区别

两个接口的参数和返回值完全相同。

它们都：

- 不需要输入参数
- 返回 5 个结果
- 查询生成结果
- 查询任务状态
- 查询队列状态
- 查询系统资源
- 查询实时日志

名称中的 `_1` 很可能是因为同一个后端函数在 Gradio 界面中绑定了多个事件，Gradio 自动为重复 API 名称添加了编号。

它通常不表示“第一个视频”。

实际调用时可选择其中一个测试，优先使用程序界面当前实际触发的接口。

---

# 8. 输出结果管理接口

## 8.1 打开输出文件夹

### API 名称

```text
/open_output_folder
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/open_output_folder
```

### 功能说明

在运行 Gradio 服务的服务器电脑上打开视频输出文件夹。

注意：

- 该接口不是把文件夹下载到调用方。
- 它是在服务器本机执行“打开目录”操作。
- 如果服务器是无桌面的 Linux、Docker 容器或纯命令行环境，该功能可能无效。
- 如果服务器是 Windows 桌面环境，可能会直接打开资源管理器。

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/open_output_folder \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/open_output_folder/$EVENT_ID
```

### 返回值

返回 1 个字符串：

```text
打开输出文件夹的执行状态
```

该结果显示在：

```text
“📡 当前任务状态”文本框
```

---

## 8.2 构建视频结果列表

### API 名称

```text
/build_video_gallery
```

### 接口地址

```text
POST http://192.168.1.200:7861/gradio_api/call/build_video_gallery
```

### 功能说明

读取视频输出目录中的文件，并重新构建“🎬 生成结果”Gallery。

该接口通常不会开始生成新视频，只会刷新现有结果列表。

### 调用统计

```text
请求次数：3
成功率：100%
p50：2 ms
p90：180 ms
p99：221 ms
```

### 请求参数

无参数。

```json
{
  "data": []
}
```

### cURL 示例

```bash
curl -X POST \
  http://192.168.1.200:7861/gradio_api/call/build_video_gallery \
  -s \
  -H "Content-Type: application/json" \
  -d '{
    "data": []
  }' \
  | awk -F'"' '{ print $4}' \
  | read EVENT_ID; \
  curl -N \
  http://192.168.1.200:7861/gradio_api/call/build_video_gallery/$EVENT_ID
```

### 返回值

返回 1 个元素：

```text
“🎬 生成结果”Gallery 组件的数据
```

---

# 9. 原始英文视频提示词中文翻译

原始 API 示例中的长文本不是接口固定参数，而是当时“视频与音频提示词”输入框中的示例内容。

完整中文意思如下。

## 镜头视角

镜头采用与眼睛高度一致的第一人称视角，并使用真实的手持玩家运动效果，仿佛画面直接录制自一款现代 AAA 级军事射击游戏。

玩家手持一把高细节突击步枪，画面中可以看到：

- 玩家双手
- 战术手套
- 动态换弹动作
- 武器自然晃动
- 真实的武器动画

## 开场动作

视频一开始，玩家已经在现代军事基地的一条道路上举枪瞄准。

远处的沙袋、路障和军用车辆附近可以看到多名敌方士兵。

玩家谨慎锁定其中一个目标，在保持开镜瞄准状态的同时进行小幅度准星修正。

随后立即向可见敌人进行多次可控短点射，并表现出：

- 真实枪口火焰
- 弹壳抛出
- 枪口烟雾
- 武器后坐力
- 敌人中弹反应
- 子弹击中目标附近时产生的尘土和碎屑

玩家继续进行多次短点射，并在不同敌人之间调整瞄准方向。

整体感觉应该像真实 FPS 玩家操作，而不是预先编排好的动画。

## 移动过程

完成开场交火后，玩家稍微解除开镜状态，并开始沿道路谨慎前进。

道路两侧包括：

- 混凝土掩体
- Hesco 防爆墙
- 停放的军用车辆

玩家会频繁查看左右角落，短暂停下来重新寻找目标。

当前方再次出现敌人时，玩家重新抬起武器并进行短点射。

玩家继续有节奏地向前推进，自然利用掩体，并保持可信的战术行动节奏。

## 环境

场景为大型现代军事基地，包括：

- 警戒塔
- 装甲车辆
- 运输集装箱
- 防爆路障
- 受损建筑
- 烟柱
- 燃烧的残骸
- 散落的弹壳
- 尘土云
- 战场烟霾

冷色调自然日光与烟雾中的橙色火光混合，形成电影化的战场氛围。

## 摄像机运动

画面应表现真实的玩家控制，包括：

- 轻微头部起伏
- 武器自然晃动
- 自然的鼠标视角调整
- 瞄准时的小幅左右修正
- 真实后坐力
- 平滑追踪移动目标
- 开枪前的短暂停顿
- 流畅向前推进

避免使用电影化的独立运镜。

所有画面都应像一名熟练玩家真实录制的游戏过程。

## 视觉质量

画面要求达到超写实 AAA 游戏品质，包括：

- 真实 PBR 材质
- 高细节武器模型
- 符合物理规律的光照
- 体积烟雾
- 动态粒子效果
- 清晰纹理
- 真实子弹撞击效果
- 枪口火焰照明
- 仅在快速移动时出现运动模糊
- 高品质军事射击游戏画面表现

## 游戏界面

显示现代 FPS 游戏风格 HUD。

可以参考 PUBG、《战地》或《使命召唤》的整体体验，但不得直接复制其受版权保护的界面资源。

HUD 应包括：

- 中央动态准星或瞄准标记
- 当前弹匣和备用弹药数量
- 射击模式指示器
- 顶部指南针
- 小队或团队状态面板
- 屏幕角落的小地图
- 生命值
- 战术装备图标，例如手雷和医疗包
- 子弹命中敌人时的命中标记
- 受到攻击时的方向提示
- 击杀通知列表
- 远处的任务目标标记
- 轻量交互提示
- 真实流畅的 HUD 动画

整个 HUD 应精致、现代，并自然融合进游戏画面，从而增强现代军事 FPS 实机录像的真实感。

---

# 10. 推荐调用流程

## 10.1 提交生成任务

调用：

```text
/submit_minimax_from_slots
```

提交：

- 生成模式
- 提示词
- 图片、视频或音频素材
- 视频尺寸
- 视频时长
- 模型配置
- Seed

接口返回：

- 当前任务状态
- 当前队列状态

## 10.2 定时查询结果

每隔 2～5 秒调用：

```text
/check_and_get_video
```

或者：

```text
/check_and_get_video_1
```

获取：

- 视频结果
- 当前任务状态
- 队列状态
- 系统资源占用
- 实时日志

## 10.3 任务完成

当返回的 Gallery 数据中出现视频文件后，即表示生成结果已经可用。

具体视频数据可能包含：

- 服务器本地路径
- Gradio 文件 URL
- 视频标题
- Gallery 展示数据

## 10.4 停止任务

停止当前正在生成的任务：

```text
/interrupt_current_task
```

## 10.5 清除排队任务

清除尚未开始的任务：

```text
/clear_pending_queue
```

## 10.6 刷新历史视频

重新读取输出目录：

```text
/build_video_gallery
```

---

# 11. 接口调用注意事项

## 11.1 局域网地址限制

服务地址为：

```text
192.168.1.200
```

这是局域网 IP。

通常只有满足以下条件的设备才能访问：

- 与服务器处于同一局域网
- 网络路由可以访问该 IP
- 服务器防火墙允许访问 7861 端口
- Gradio 监听地址不是仅限于 `127.0.0.1`

如果需要公网访问，应配置：

- 公网 IP
- 端口映射
- Nginx 反向代理
- HTTPS
- 身份认证
- 防火墙规则

## 11.2 参数顺序不能改变

Gradio 的 `data` 是位置参数数组。

例如：

```json
{
  "data": [
    参数0,
    参数1,
    参数2
  ]
}
```

后端会按照数组位置读取参数。

因此不能只根据参数名随意排列，也不能直接省略中间参数。

不使用的可选参数应保留位置，并根据后端要求传入：

```json
null
```

或空字符串：

```json
""
```

## 11.3 文件 URL 必须能够被服务器访问

如果传入：

```json
{
  "path": "https://example.com/image.png"
}
```

真正下载该图片的是运行在：

```text
192.168.1.200
```

上的服务器。

因此服务器必须能够正常访问该 URL。

## 11.4 接口目前未体现任务 ID

从当前自动文档来看，提交任务接口只返回：

- 当前任务状态
- 队列状态

没有明确返回独立业务任务 ID。

而查询接口也不需要传入任务 ID。

这意味着当前实现可能是：

- 全局单任务
- 全局队列
- 按 Gradio 会话维护任务
- 仅适合单用户或少量并发调用

如果要封装成正式的多用户 API，建议增加：

```text
task_id
user_id
status
progress
result_url
created_at
finished_at
error
```

否则多个客户端并发提交任务时，可能难以准确区分各自的生成结果。

## 11.5 API 没有显示身份认证

当前接口示例没有包含：

```text
Authorization
API Key
Token
Cookie
```

如果直接暴露到公网，任何知道地址的人都可能：

- 提交视频任务
- 消耗 GPU 资源
- 中断当前任务
- 清除任务队列
- 查看日志和资源占用
- 查看生成结果

因此不建议直接将该 Gradio API 暴露到公网。

推荐在外层增加自己的后端服务，并实现：

- API Key 验证
- 用户权限
- 速率限制
- 任务隔离
- 文件安全检查
- 日志脱敏
- 结果访问权限
- 队列控制
- 费用和额度控制

---

# 12. 接口功能总结

真正负责提交视频生成任务的接口：

```text
/submit_minimax_from_slots
```

真正负责查询视频生成结果的接口：

```text
/check_and_get_video
```

或：

```text
/check_and_get_video_1
```

停止当前任务：

```text
/interrupt_current_task
```

清空等待队列：

```text
/clear_pending_queue
```

刷新已有视频列表：

```text
/build_video_gallery
```

保存当前界面配置：

```text
/save_ui_config
```

交换视频横竖尺寸：

```text
/swap_width_height
```

保存和删除快捷提示词：

```text
/save_quick_prompt
/delete_quick_prompt
```