# 模型请求配置 API 增量

## 1. Manager 接口

| 接口 | 方法 | 说明 |
| --- | --- | --- |
| `/manager/api/request-profiles` | GET | 返回按 resolution 排序的即时配置 |
| `/manager/api/request-profiles` | POST | 创建尚不存在的 resolution 配置并立即生效 |
| `/manager/api/request-profiles/{id}` | GET | 查询配置 |
| `/manager/api/request-profiles/{id}` | PUT | 覆盖配置并立即生效 |
| `/manager/api/request-profiles/{id}` | DELETE | 立即删除配置 |

删除接口：

```http
DELETE /manager/api/request-profiles/{id}
Content-Type: application/json

{"row_version":3}
```

成功返回 HTTP 204。行版本冲突返回 409；不存在返回 404。删除不检查任务状态。

移除接口：

- `POST /manager/api/request-profiles/{id}/tests`
- `GET /manager/api/profile-tests/{id}`
- `POST /manager/api/request-profiles/{id}/publish`
- `POST /manager/api/request-profiles/{id}/clone`
- `GET /manager/api/request-profiles/{id}/compatible-nodes`

## 2. Profile 请求结构

```json
{
  "resolution": "2K",
  "generation": {
    "model_mode": "high_quality",
    "steps": 8,
    "sage_attention": "auto",
    "cache_mode": "easycache"
  },
  "ratios": {},
  "loras": [],
  "interpolation": {"enabled": true, "engine": "rife", "scale": 2},
  "restoration": {"enabled": true, "engine": "seedvr2", "scale": 3}
}
```

PUT 额外要求 `row_version`，但 `resolution` 必须与资源当前值相同。删除字段包括 model、scenario、默认/允许比例、main_model、fps、cfg、watermark 和 av_sync_tolerance_ms；严格 JSON 写接口收到这些旧字段时返回 400。音画同步校验由 Node 使用固定默认值 50ms，不属于模型请求配置。

## 3. 公开水印参数

`POST /v2/video_generation` 的 `aigc_watermark`：boolean、可选、默认 `false`。true 添加水印，false 或省略不添加。

## 4. 数据与事务

- POST/PUT/DELETE 均使用单个 SQLite 写事务。
- PUT 的提交时刻即生效时刻。
- DELETE 同事务清空历史任务 `profile_id` 后删除配置。
