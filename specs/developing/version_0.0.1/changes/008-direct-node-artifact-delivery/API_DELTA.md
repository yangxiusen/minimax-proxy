# 节点直出视频接口增量

## 1. 变更概览

| 接口 | 路径 | 方法 | 变更类型 |
| --- | --- | --- | --- |
| V2 单任务查询 | `/v2/query/video_generation/{task_id}` | GET | `content.url` 来源变化 |
| V2 任务列表 | `/v2/query/video_generation` | GET | `items[].content.url` 来源变化 |
| Manager 任务列表 | `/manager/api/tasks` | GET | `video_url` 来源变化 |
| Node 公开视频内容 | `/public/v1/artifacts/{artifact_id}/content` | GET | 新增接口 / 签名鉴权 |

## 2. Proxy 查询接口变化

请求路径、请求参数、外部 Bearer 鉴权、响应字段和错误包均不变。

变更前：

```json
{
  "content": {
    "url": "https://proxy.example.com/v2/files/art_x/content?expires=1786618995&signature=..."
  }
}
```

变更后：

```json
{
  "content": {
    "url": "https://node-1.example.com:7860/public/v1/artifacts/art_node_x/content?expires=1786791795&signature=..."
  }
}
```

- URL 根地址取 artifact 活动位置对应节点的 `service_url`。
- URL 使用节点 artifact ID，不暴露 Proxy 逻辑 artifact ID 与 Node API Key 的映射关系。
- 每次查询重新签发 48 小时链接。
- 无活动位置、节点记录、可解密 Key 或合法根 `service_url` 时，沿用查询接口的 `500 server_error`；日志只记录 request/task/artifact/node ID 和稳定错误类型。
- 历史 `legacy-gradio-v1` 任务继续返回已持久化并校验过的 `result_public_url`，不套用新签名。

## 3. Node 公开视频内容接口

### 3.1 请求

```http
GET /public/v1/artifacts/{artifact_id}/content?expires=<unix_seconds>&signature=<base64url>
Range: bytes=0-1048575
```

| 参数 | 位置 | 必填 | 规则 |
| --- | --- | --- | --- |
| `artifact_id` | path | 是 | Node artifact ID，必须能解析为活动产物 |
| `expires` | query | 是 | 十进制 Unix 秒，晚于节点当前时间且不超过当前时间加 48 小时和 5 分钟时钟余量 |
| `signature` | query | 是 | HMAC-SHA256 的 URL-safe Base64，无 `=` 填充 |
| `Range` | header | 否 | 仅支持一个 `bytes=start-end` 范围，规则与内部 content 接口一致 |

该接口不接受也不需要 `Authorization`。完整 URL 本身是临时访问凭证。

### 3.2 签名契约

双方从 32 位 Node API Key 派生专用签名 Key：

```text
signing_key = HMAC-SHA256(
  key = UTF8(node_api_key),
  message = UTF8("minimax-h3-node-artifact-download-v1")
)
```

规范串必须逐字节一致：

```text
v1\nGET\n<artifact_id>\n<expires>
```

最终签名：

```text
signature = base64url_without_padding(
  HMAC-SHA256(key = signing_key, message = UTF8(canonical_string))
)
```

Node 使用常量时间比较验证签名。签名绑定方法、artifact ID 和过期时间；修改任意部分均无效。

### 3.3 响应

| HTTP | 场景 | 响应要求 |
| --- | --- | --- |
| 200 | 无 Range 的有效下载 | 文件正文；`Content-Type/Length/ETag/Accept-Ranges` |
| 206 | 合法单 Range | 范围正文；增加 `Content-Range` |
| 403 | 缺少、非法、篡改、过期或超出最大未来窗口的签名 | 统一 Node 错误包，不区分具体签名失败原因 |
| 404 | 签名有效但 artifact 不存在、已删除或非活动 | 统一 Node 错误包 |
| 416 | Range 格式或范围无效 | 沿用 `invalid_range` |

文件响应使用 `Cache-Control: private, no-store`。错误和访问日志不得记录 `signature` 或完整 query string。

## 4. 旧 Proxy 文件接口兼容

- `GET /v2/files/{artifact_id}/content` 暂时保留，不再作为新查询结果的 URL 来源。
- 升级前已经签发的未过期 URL 继续按旧 HMAC、owner 和 Range 规则处理。
- 兼容路由移除不属于本变更范围，后续需单独确认观测窗口和下线版本。

## 5. 配置契约变化

删除以下 Proxy 配置：

```yaml
server:
  public_base_url: "${MINIMAX_PUBLIC_BASE_URL}"
```

删除 Docker 环境变量 `MINIMAX_PUBLIC_BASE_URL`。`server.address` 仍只控制 Proxy 监听地址。

## 6. 兼容性说明

- JSON 字段兼容，URL host/path 变化；遵循响应 URL 的客户端无需改代码。
- 把 Proxy host 加入固定白名单、假定 `/v2/files` 路径或通过 Proxy 网络策略放行视频的客户端/部署需要同步调整。
- Node API Key 轮换后旧签名失效，调用方通过重新查询任务恢复。

## 7. 人工联调准备关注点

- [ ] 外部网络可以访问每个节点数据库 `service_url` 的 `7860` 入口。
- [ ] 反向代理、防火墙和安全组仅按需开放 `/public/v1/artifacts/*/content`，不放宽 `/internal/v1/*`。
- [ ] 真实浏览器播放、拖动、重复下载和 48 小时过期行为已验证。
- [ ] 通过流量监控确认视频正文不经过 Proxy。
