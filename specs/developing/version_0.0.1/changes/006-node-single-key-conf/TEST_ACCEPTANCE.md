# Node 单 Key 与 conf.yml 测试与验收

## 1. 基本信息

| 项目 | 内容 |
| --- | --- |
| 目标版本 | `version_0.0.1` |
| 变更编号 | `006-node-single-key-conf` |
| 测试负责人 | 自动化部分由开发执行；现场与业务部分由部署人员/管理员确认 |
| 当前状态 | 开发与本地自动验证完成；现场人工验收待执行 |

## 2. 测试范围

- 变更模块：Node 配置加载、API/WebUI 鉴权；Proxy 节点领域、Store、Manager API/UI、Node Client。
- 回归模块：Node 全部受保护路由；Proxy 节点创建/编辑/升级/测试、任务执行、产物、清理和测试任务。
- 自动结论之外：真实进程重启、GPU/模型执行、部署目录 ACL、业务验收、发布审批和回滚决策。

## 3. 自动化用例

| ID | 场景 | 预期结果 |
| --- | --- | --- |
| AUTO-001 | 无 `conf.yml` 首次加载 | 生成合法 Key、admin 和合法 8 位密码 |
| AUTO-002 | 已有合法文件重复加载 | 内容、摘要和 mtime 不变 |
| AUTO-003 | 非项目 CWD 启动 | 仍定位到 `start.py` 所在根目录 |
| AUTO-004 | 并发首次创建 | 只有一个文件成功创建，所有进程读取相同值 |
| AUTO-005 | YAML 缺失/额外字段或错误类型 | 启动失败，文件不变 |
| AUTO-006 | Key/用户名/密码规则不合法 | 启动失败并指出配置字段 |
| AUTO-007 | 旧凭据环境变量存在 | 不覆盖也不影响 `conf.yml` |
| AUTO-008 | Node 正确单 Key Bearer | 全部受保护路由通过鉴权 |
| AUTO-009 | 缺 Header、错误 Key、复合 Token | 统一返回 401 |
| AUTO-010 | WebUI 正确/错误凭据 | admin + 配置密码成功，其余失败 |
| AUTO-011 | Proxy Key 长度和字符校验 | 仅 32 位字母数字通过 |
| AUTO-012 | Manager 提交 `api_key_id` | 严格 JSON 返回 400 |
| AUTO-013 | 创建和 Legacy 升级缺 Key | 返回 400，不触发 SQLite 500 |
| AUTO-014 | 编辑已有 H3 Key 留空 | 复用密文、Nonce 和指纹，不回显明文 |
| AUTO-015 | 新建/更新 H3 数据库占位 | `api_key_id` 写空字符串，满足旧约束 |
| AUTO-016 | 历史非空 Key ID 数据 | 可读取但不进入领域对象或接口响应 |
| AUTO-017 | Legacy 数据库行 | NULL/既有约束保持正常 |
| AUTO-018 | Proxy Node Client 全路径 | 每条请求精确发送 `Bearer <key>` |
| AUTO-019 | 外部客户 Key 回归 | V2 客户鉴权和任务所有权未被模型节点改动误伤 |
| AUTO-020 | 敏感信息搜索 | 日志、响应和仓库不含现场 Key/密码 |

自动验证命令：

```powershell
# Minimax-H3
walkingwithai\python.exe -m pytest tests\h3_service -q

# MiniMax-H3-Proxy
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/healthcheck
node --check internal/httpapi/manager/web/manager.js
git diff --check
```

### 3.1 执行结果（2026-08-13）

| 范围 | 结果 | 说明 |
| --- | --- | --- |
| Node `tests/h3_service` | Passed | `226 passed` |
| Proxy 006 定向 Store、Node Client、Registry | Passed | 单 Key Header、SQLite 空占位与历史 Key ID 忽略均通过 |
| Proxy 全量测试 | Passed 后被外部改动阻断复跑 | 006 实现完成时 `go test ./... -count=1` 通过；审查修复后的复跑被同工作区未完成 007 类型/API 改动阻断编译 |
| Proxy Manager 审查回归 | Passed（文件级隔离） | 空 Key 复用、保留草稿参数、Legacy 无存储 Key 拒绝三条回归通过；正常包级复跑仍被 007 的 Profile 编译错误阻断 |
| Go build/vet | Passed 后被外部改动阻断复跑 | 006 初次验证通过；最新构建同样被 007 编译错误阻断 |
| Go race | Environment limited | 当前环境报 `-race requires cgo` |
| Manager JavaScript 语法 | Passed | `node --check internal/httpapi/manager/web/manager.js` |
| 006 触及文件空白检查 | Passed | 无 whitespace error；仅有 Git 行尾转换提示 |

本表只记录自动化证据，不替代下方现场、业务和发布验收。

## 4. 本地人工验收

| ID | 前置条件 | 操作 | 预期结果 | 负责人 | 状态 |
| --- | --- | --- | --- | --- | --- |
| MAN-001 | 备份或确认 Node 根目录无现场配置 | 正常启动 Node | 根目录生成 `conf.yml`，终端不打印秘密 | 部署人员 | Pending |
| MAN-002 | MAN-001 完成 | 记录摘要/mtime 后重启 Node | 摘要和 mtime 不变 | 部署人员 | Pending |
| MAN-003 | 合法配置存在 | 使用配置账号登录 WebUI | `admin` + 8 位密码可登录 | 管理员 | Pending |
| MAN-004 | Proxy Manager 已登录 | 打开节点表单 | 仅一个 API Key 字段，无 Key ID | 管理员 | Pending |
| MAN-005 | 从本机配置安全读取 Key | 创建 H3 节点并测试连接 | 保存和健康/能力检查成功，无 500 | 管理员 | Pending |
| MAN-006 | 已有 H3 节点 | 编辑时 Key 留空保存并再次测试 | 继续使用已保存 Key | 管理员 | Pending |
| MAN-007 | Legacy 节点存在 | 升级为 H3 并只填写一个 Key | 升级成功，无 Key ID 要求 | 管理员 | Pending |
| MAN-008 | 故意输入坏 Key | 提交 31/33 位或特殊字符 | 页面和服务端均明确拒绝 | 管理员 | Pending |
| MAN-009 | 桌面与移动视口 | 检查表单最长文案和错误提示 | 无重叠、遮挡或横向溢出 | 管理员 | Pending |

## 5. 真实联调与部署验收

以下项目必须由人工执行，AI 不得标记为自动通过。

| ID | 操作 | 预期结果 | 负责人 | 状态 |
| --- | --- | --- | --- | --- |
| OPS-001 | 备份两个仓库的运行配置、SQLite 和现场启动参数 | 可恢复且不在记录中暴露秘密 | 部署人员 | Pending |
| OPS-002 | 同一窗口升级 Node 与 Proxy | 旧复合 Token 停用，新单 Key 可用 | 部署人员 | Pending |
| OPS-003 | 使用真实 Node 执行健康、能力、提交、查询、取消、产物与清理调用 | 所有接口契约完整且只需一个 Key | 部署人员 | Pending |
| OPS-004 | 在模型/GPU 就绪时提交一个轻量真实任务 | 任务可执行、查询并按预期完成或取消 | 业务验收人 | Pending |
| OPS-005 | 检查 `conf.yml` 本机 ACL/权限 | 仅运行账号和授权管理员可读 | 部署人员 | Pending |
| OPS-006 | 验证无效现有 `conf.yml` 启动 | 明确失败且文件不被修复或覆盖 | 部署人员 | Pending |
| OPS-007 | 执行 Node/Proxy 成对回滚演练 | 备份配置和数据库可恢复旧服务 | 部署人员 | Pending |

## 6. 安全检查

- [ ] `conf.yml` 已被 Git 忽略，`conf.example.yml` 不含现场秘密。
- [ ] 终端、应用日志、HTTP 错误、Manager 响应和浏览器截图不出现 Key 或密码。
- [ ] 旧 `H3_API_KEYS`、`H3_UI_PASSWORD_HASH` 等环境变量无法改变实际凭据。
- [ ] API Key 和 WebUI 凭据使用常量时间比较。
- [ ] Proxy 数据库只保存现有加密 Key材料，不新增明文副本。
- [ ] Manager 编辑页面不回显已保存 Key。

## 7. 回归与残余风险

- [ ] Proxy 对外客户 API Key、V2 请求和文件下载鉴权未变化。
- [ ] 任务执行、产物拉取、清理和节点测试均可访问 Node。
- [ ] 旧 v7 数据库无需迁移可启动。
- [ ] 管理后台其他入口、会话和节点列表行为正常。

残余风险：Node 与 Proxy 只升级一侧会导致 401；`conf.yml` 是明文敏感文件，安全性依赖主机文件权限和部署纪律；8 位密码长度由已确认需求固定，不应被描述为高强度长期互联网口令。

## 8. 验收结论

- [ ] 自动化验证通过。
- [ ] 本地人工验收通过。
- [ ] 真实联调与部署验收通过。
- [ ] 管理员和发布负责人批准。

只有适用项均由对应负责人确认后，变更才可进入归档。
