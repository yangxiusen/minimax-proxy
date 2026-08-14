# Node 单 Key API 增量

## 1. 变更概览

| 接口范围 | 路径 | 方法 | 变更类型 |
| --- | --- | --- | --- |
| Node 受保护 API | 现有 `/api/*` 路由 | 各现有方法 | 鉴权调整 |
| Proxy Manager 节点管理 | `/manager/api/nodes`、`/manager/api/nodes/{id}` | POST/PUT/GET | 删除请求/响应字段、校验调整 |
| Proxy Manager 连接测试 | 现有节点测试路由 | POST | 凭据输入调整 |

## 2. Node 鉴权变更

### 变更前

```http
Authorization: Bearer <key_id>.<secret>
```

Token 可关联 scope，不同路由可能要求不同权限。

### 变更后

```http
Authorization: Bearer Abcdefghijklmnopqrstuvwx12345678
```

- Bearer 值必须与 `conf.yml` 的 `api.key` 完全一致。
- 一个 Key 可访问全部受保护 Node 路由。
- 缺少、格式错误或值不匹配统一返回 401；不再返回 scope 相关 403。
- 复合 Token 与 Key ID 不再兼容。

## 3. Proxy Manager DTO 变更

### 变更前

```json
{
  "id": "node-1",
  "service_url": "http://private.example:7860",
  "protocol_version": "h3-node-v1",
  "api_key_id": "proxy",
  "api_key": "secret"
}
```

### 变更后

```json
{
  "id": "node-1",
  "service_url": "http://private.example:7860",
  "protocol_version": "h3-node-v1",
  "api_key": "Abcdefghijklmnopqrstuvwx12345678"
}
```

- `api_key` 在提供时必须匹配 `^[A-Za-z0-9]{32}$`。
- 新建 H3 节点和 Legacy 升级必须提供 Key。
- 编辑已有 H3 节点可省略或留空 Key，表示复用已保存密钥。
- `api_key_id` 是废弃的未知字段，提交时返回现有标准 `400 bad_request_error`。
- GET 响应不返回 `api_key_id` 或 Key 明文；保留表示是否已有存储 Key 的既有安全状态字段。

## 4. 兼容性说明

- Node 调用契约为有意的不兼容调整，Proxy 与 Node 需要成对升级。
- Manager 页面与服务端同批发布，不提供旧 Key ID 字段兼容窗口。
- 数据库历史值保持兼容，但不会继续暴露到 API。
- Proxy 对外 V2 API 的客户 Bearer Key 和 API Key 管理接口不变。

## 5. 人工联调准备关注点

- [ ] Node 全部受保护路由接受同一单 Key。
- [ ] 旧复合 Token 被拒绝。
- [ ] Manager 创建、编辑留空复用和 Legacy 升级请求符合新 DTO。
- [ ] API 响应、错误和日志均不泄露 Key。
- [ ] Proxy 实际连接测试使用 `conf.yml` Key 成功。
