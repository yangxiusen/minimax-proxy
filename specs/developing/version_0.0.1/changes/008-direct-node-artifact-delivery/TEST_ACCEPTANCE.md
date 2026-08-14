# 节点直出视频测试与验收

## 1. 测试基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `008-direct-node-artifact-delivery` |
| 测试日期 | 2026-08-14 |
| 测试负责人 | 自动化：AI；真实环境与发布：待人工填写 |

## 2. 测试范围

- 变更模块：Node 签名验证与公共 artifact 路由、Proxy artifact URL signer、V2/Manager URL 映射、Proxy 配置加载。
- 回归模块：Node 内部 artifact Bearer 接口、Proxy 旧 `/v2/files`、任务归属、Manager 播放、Range、清理后的不可访问性。
- 不在自动化范围：公网 DNS/TLS、防火墙、安全组、真实大文件带宽、生产发布和真实等待 48 小时。

## 3. 自动化核心用例

| 用例ID | 场景 | 前置条件 | 操作 | 预期结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| DA-TC-001 | 跨语言固定向量 | 固定 Node Key、artifact、expires | Go/Python 分别签名 | 签名逐字节相同 | Pass |
| DA-TC-002 | 完整文件 | 活动视频与有效签名 | 无 Range 请求公开路由 | `200`、正文和受控响应头正确 | Pass |
| DA-TC-003 | Range 播放 | 活动视频与有效签名 | 请求合法单 Range | `206`、范围正文与 `Content-Range` 正确 | Pass |
| DA-TC-004 | 重复访问 | 同一有效 URL | 连续多次完整/Range 请求 | 均成功，不消耗一次性状态 | Pass |
| DA-TC-005 | 签名篡改 | 有效 URL | 修改 artifact/expires/signature 任一项 | `403 invalid_download_signature` | Pass |
| DA-TC-006 | 时间边界 | 可控时钟 | 测试已过期、恰好过期、未来超过 48h+5m | 均返回 `403` | Pass |
| DA-TC-007 | Artifact 状态 | 有效签名 | 请求不存在、删除或非活动 artifact | `404`，不泄露路径 | Pass |
| DA-TC-008 | Key 轮换 | 先用旧 Key 签发 | Node 改用新 Key 后访问旧链接 | `403`；新签名可访问 | Pass |
| DA-TC-009 | Proxy URL 生成 | 活动位置与外部可达根 URL | 查询成功任务 | 返回 Node public 路径、48h expires、无 Node Key | Pass |
| DA-TC-010 | 位置与 URL 错误 | 缺位置/节点/Key或非法 URL | 查询成功任务 | 稳定内部错误，不回退内部裸地址 | Pass |
| DA-TC-011 | V2 所有权 | 两个外部 API Key | 查询对方任务 | 原有隔离不变，不签发 URL | Pass |
| DA-TC-012 | Legacy 任务 | 仅有安全 `result_public_url` | 查询任务 | 保持原历史 URL | Pass |
| DA-TC-013 | 旧 Proxy 链接 | 升级前有效签名 | 请求 `/v2/files` 完整/Range | 原行为继续工作 | Pass |
| DA-TC-014 | 配置移除 | YAML 无 `public_base_url`，环境无旧变量 | 加载配置并启动测试应用 | 成功，不读取 `MINIMAX_PUBLIC_BASE_URL` | Pass |
| DA-TC-015 | 内部接口隔离 | Node 启动 | 无 Bearer 请求 `/internal/v1/artifacts/.../content` | 仍返回 `401` | Pass |

## 4. 自动化验证命令

Node：

```powershell
pytest tests/h3_service -q
```

Proxy：

```powershell
go test ./...
go vet ./...
go build ./cmd/server ./cmd/healthcheck
```

文档与配置扫描：

```powershell
rg -n "MINIMAX_PUBLIC_BASE_URL|server\.public_base_url|public_base_url:" README.md config*.yaml .env.docker.example internal cmd
```

预期仅允许历史迁移/兼容说明中的明确文字引用，不存在生效配置和启动校验。

## 5. 人工验收

| 用例ID | 场景 | 操作 | 预期结果 | 负责人 | 状态 |
| --- | --- | --- | --- | --- | --- |
| DA-MAN-001 | 外部可达 | 从真实调用方网络打开每个节点签名 URL | 可播放/下载，地址指向 Node | 部署负责人 | Pending |
| DA-MAN-002 | 流量路径 | 下载一个已知大小视频并观察 Proxy/Node 网卡 | Node 有出口正文流量，Proxy 无对应正文流量 | 运维 | Pending |
| DA-MAN-003 | 浏览器播放 | 播放、暂停、拖动、刷新和重复打开 | 均正常，Range 无异常 | 业务验收人 | Pending |
| DA-MAN-004 | 真实过期 | 保存链接并在 48 小时后访问，再查询任务 | 旧链接拒绝，新链接可用 | 测试负责人 | Pending |
| DA-MAN-005 | 网络边界 | 检查反向代理、防火墙和日志 | public content 可达，internal API 未放宽，日志无签名 | 安全/运维 | Pending |
| DA-MAN-006 | 发布回滚 | 演练恢复旧 Proxy URL 配置与版本 | 可按回滚说明恢复旧链路 | 发布负责人 | Pending |

## 6. 回归验证

- [x] Node 健康、能力、执行、导入、元数据、内部下载和删除接口未受影响。
- [x] V2 请求、查询、取消、删除、回调和外部 API Key 隔离未受影响。
- [x] Manager 登录、任务列表、播放弹窗和节点管理未受影响。
- [x] Artifact 清理后新旧下载路径均不可继续读取正文。
- [x] 应用结构化日志不包含 Node API Key、签名参数、完整下载 URL 或文件系统路径；反向代理访问日志仍需人工确认。

## 7. 缺陷记录

| 缺陷ID | 描述 | 严重级别 | 状态 | 备注 |
| --- | --- | --- | --- | --- |
| 暂无 | 暂无 | 暂无 | 暂无 | 执行后补充 |

## 8. 验收结论

- [x] 自动化验证通过。
- [ ] 真实节点与外部网络联调通过。
- [ ] 流量节省目标经监控确认。
- [ ] 安全与发布负责人确认上线。
