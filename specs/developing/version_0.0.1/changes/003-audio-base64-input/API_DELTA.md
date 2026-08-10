# 音频 Base64 输入接口增量

## 一、接口范围

| 接口名称 | 方法 | URL | 变化 |
| --- | --- | --- | --- |
| 创建视频生成任务 | POST | `/v2/videos/generations` | 扩展 `audio_url.url` 可接受的媒体来源 |

## 二、请求契约

`content` 中 `type=audio_url` 且 `role=reference_audio` 的元素支持以下两种 `audio_url.url`：

```json
{
  "type": "audio_url",
  "role": "reference_audio",
  "audio_url": {
    "url": "data:audio/wav;base64,UklGRg..."
  }
}
```

- HTTP/HTTPS URL：保持原行为。
- Data URI：支持 `audio/wav`、`audio/mpeg`、`audio/mp3`，单段解码后最大 15 MiB。
- 只写 `data:audio/wav;base64` 而没有 `,<Base64内容>` 属于无效格式。
- 裸 Base64、本地路径和其他 MIME 返回参数错误。

图片仍支持 HTTP/HTTPS URL 和图片 Data URI；视频仍只支持无凭据 HTTP/HTTPS URL。

## 三、兼容性

接口路径、鉴权、任务响应、幂等语义及已有 URL 输入均不变。任务的规范化请求仍保存原始 Data URI，以便前置服务重启恢复或唯一一次自动重试时重新上传音频。
