# 对外 API Key 管理接口增量

## 1. 概览

新增 4 个仅供已登录管理员使用的接口。公共 `/v2/*` 路径、Bearer Header 与响应结构保持不变，鉴权实现改为共享内存快照。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/manager/api/api-keys` | 列出 Key 元数据 |
| POST | `/manager/api/api-keys` | 按名称创建 Key |
| PUT | `/manager/api/api-keys/{api_key_id}` | 重命名或启停 |
| DELETE | `/manager/api/api-keys/{api_key_id}?version=N` | 删除无引用 Key |

所有响应设置 `Cache-Control: no-store`，使用现有 Manager Session Cookie。未登录返回 `401 authentication_error`。

## 2. 列表

`GET /manager/api/api-keys`

```json
{
  "items": [
    {
      "id": "key_a1b2c3d4e5f60123456789abcdef0123",
      "name": "生产调用方",
      "key": "mmx_<64位随机十六进制>",
      "masked_key": "mmx_ab12...89ef",
      "enabled": true,
      "version": 1,
      "created_at": 1786525200000,
      "updated_at": 1786525200000
    }
  ],
  "enabled_count": 1
}
```

- 固定按 `created_at DESC, id` 排序；v0.0.1 不分页。
- 不返回 `key_digest`；返回数据库保存的完整 `key`，供已登录管理员复制。

## 3. 创建

`POST /manager/api/api-keys`

```json
{
  "name": "生产调用方"
}
```

成功：`201 Created`，`Location: /manager/api/api-keys/{id}`。

```json
{
  "id": "key_a1b2c3d4e5f60123456789abcdef0123",
  "name": "生产调用方",
  "key": "mmx_<64位随机十六进制>",
  "masked_key": "mmx_ab12...89ef",
  "enabled": true,
  "version": 1,
  "created_at": 1786525200000,
  "updated_at": 1786525200000
}
```

`key` 会在创建响应及后续列表、更新响应中返回。相同请求重试会因名称冲突返回 `409`。

## 4. 更新

`PUT /manager/api/api-keys/{api_key_id}`

```json
{
  "name": "生产调用方 A",
  "enabled": false,
  "version": 1
}
```

成功：`200 OK`，返回与列表项相同的元数据，`version` 增加 1。请求是全量元数据更新，不允许修改摘要、掩码或内部 ID。

## 5. 删除

`DELETE /manager/api/api-keys/{api_key_id}?version=2`

- 成功：`204 No Content`。
- `version` 必须为正整数且只能出现一次。
- 有任务或幂等引用时返回 `409 key_in_use`，不级联删除。

## 6. 校验

- `Content-Type` 必须为 `application/json`（DELETE 无 Body）。
- POST/PUT 请求体上限 4 KiB，必须是单一 JSON 对象。
- 拒绝未知字段、重复字段、多余 JSON 和类型不匹配。
- `name` 去除首尾空格后长度为 1 至 128；名称大小写不敏感唯一。
- `api_key_id` 长度为 1 至 128，不得包含 `/` 或 `\`；服务端仍按完整 ID 查询。

## 7. 错误码

| HTTP | `error.type` | 场景 |
| --- | --- | --- |
| 400 | `bad_request_error` | JSON、名称、ID 或 version 无效 |
| 401 | `authentication_error` | 未登录或会话过期 |
| 404 | `api_key_not_found` | 记录不存在 |
| 409 | `api_key_name_conflict` | 名称大小写不敏感冲突 |
| 409 | `api_key_version_conflict` | 乐观锁版本过期 |
| 409 | `key_in_use` | 存在任务或幂等引用 |
| 503 | `cache_refresh_failed` | DB 已写入但当前鉴权快照刷新失败 |
| 500 | `server_error` | 其他内部错误 |

错误结构沿用 Manager API：

```json
{
  "error": {
    "type": "api_key_name_conflict",
    "message": "密钥名称已存在"
  }
}
```

## 8. 公共鉴权兼容

- `Authorization: Bearer <key>` 格式不变。
- 禁用、删除或未知 Key 对 `/v2/*` 继续返回现有 `401` 鉴权错误。
- `/v2/files/{artifact_id}/content` 的 Bearer 所有权校验改用同一 Authenticator；签名 URL 逻辑不变。
- 空启用集合合法，所有 Bearer 请求失败，不影响 Manager Session。

## 9. 人工联调准备

- [ ] 确认创建响应完整 Key 只出现一次。
- [ ] 使用新 Key 调用 V2 创建/查询和 Bearer 文件下载。
- [ ] 停用后验证当前进程立即拒绝，重新启用后恢复。
- [ ] 模拟旧版本页面提交，验证版本冲突提示。
- [ ] 验证有历史任务的 Key 删除返回 `key_in_use`。
