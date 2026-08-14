# Node 单 Key 与 conf.yml PRD 增量

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `006-node-single-key-conf` |
| 文档类型 | `PRD_DELTA.md` |

## 2. 变更背景

当前 Node 与 Proxy 需要管理员理解 Key ID、Secret、scope、复合 Token 和多处环境变量。凭据口径分散，容易产生节点保存失败、鉴权不匹配和部署时误覆盖。本变更将节点身份简化为单个 Key，并由 Node 首次启动生成同一份 WebUI 凭据文件。

## 3. 本次目标

- 降低 Node 首次部署与 Proxy 节点录入成本。
- 消除 Key ID、scope 与复合 Token 在两个项目之间的契约歧义。
- 保证凭据只生成一次，配置错误可见且不会被程序静默覆盖。

## 4. 变更范围

### 4.1 本次包含

- Node 首次启动凭据生成、后续严格读取和 WebUI 登录。
- Node 全部受保护路由的单 Key Bearer 鉴权。
- Proxy 管理后台 H3 节点创建、编辑、升级、保存和连接测试。
- 历史节点数据库的无迁移兼容。

### 4.2 本次不包含

- Proxy 对外客户 API Key 管理。
- Node 凭据轮换、多用户、多 Key、scope 或在线配置页面。
- 管理后台整体信息架构调整。

## 5. 需求增量说明

### 5.1 变更前

- Node API 需要 Key ID 与 Secret 组成 Token，并可能受 scope 限制。
- WebUI 凭据通过独立环境变量维护。
- Proxy 管理员必须同时填写 Key ID 和 Key。

### 5.2 变更后

- Node 首次启动在根目录创建 `conf.yml`；Key 为 32 位字母数字，用户名固定为 `admin`，密码为 8 位字母数字且包含字母和数字。
- 文件存在后永不自动生成、修复或覆盖；不合法时直接启动失败。
- 管理员只把一个 Key 录入 Proxy；编辑时留空沿用旧 Key。
- 所有 Node API 权限等价，不再展示或配置 scope。

### 5.3 角色与场景

| 角色 | 场景 | 目标行为 |
| --- | --- | --- |
| Node 部署人员 | 首次启动 | 从根目录 `conf.yml` 获取一次性生成的 Key 和 WebUI 密码 |
| Proxy 管理员 | 创建或升级节点 | 只填写一个 32 位 Key并完成连接测试 |
| Proxy 管理员 | 编辑节点 | Key 留空时安全复用已保存凭据 |
| API 调用模块 | 访问 Node | 统一发送 `Bearer <key>` |

## 6. 验收标准

- Node 凭据来源、格式、首次生成和重启不变性符合 `CHANGE_SPEC.md`。
- 管理后台不存在 Key ID 输入、展示或提交字段。
- 旧数据库无需人工迁移即可继续启动和管理节点。
- 日志、页面响应和截图不暴露 Key 或 WebUI 密码。

## 7. 关联文档

- `CHANGE_SPEC.md`
- `PROTOTYPE_DELTA.md`
- `TECH_SOLUTION.md`
- `API_DELTA.md`
- `DATABASE_DELTA.md`
- `task.md`
- `TEST_ACCEPTANCE.md`
