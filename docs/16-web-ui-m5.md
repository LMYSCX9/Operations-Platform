# DOC-16 M5 实现基线（Web 控制台接入真实接口）

> 状态：`draft`（统计口径与执行摘要属**实现基线，可回改**，登记 [OQ-043](07-open-questions.md) / [OQ-046](07-open-questions.md)）
> 读者：前端与底座开发 ｜ 上游：[DOC-06](06-non-functional-requirements.md)、[ADR-0001](adr/0001-single-binary-embedding.md)、[ADR-0002](adr/0002-frontend-stack.md)
> 下游：[DOC-07](07-open-questions.md)

M5 把 [prototype/index.html](../prototype/index.html) 的 mock 数据替换为真实接口调用。
约束不变：**单文件、原生 HTML/CSS/JS、Go embed 内嵌、单二进制交付**，不引入前端框架或 Node 构建链。

## 1. 交付形态

| 项 | 实现 |
| --- | --- |
| 源文件 | `prototype/index.html` |
| 内嵌产物 | `web/dist/index.html`（构建脚本从 prototype 复制） |
| 依赖 | 无外部 CDN / 字体 / JS 库；全部内联 |
| 会话 | Cookie（`ops_session`，HttpOnly、SameSite=Strict），`fetch` 使用 `credentials: same-origin` |
| CSRF | 所有非 GET 请求带 `X-Ops-Request: 1` |
| 路由 | 无前端路由，单页内切换视图 |
| 渲染安全 | 接口文本一律 `textContent` 或转义后插入，不拼 `innerHTML` |

修改 `prototype/index.html` 后必须重新执行构建脚本，否则二进制内嵌的仍是旧页面（AGENTS.md §6）。

## 2. 页面与能力

| 页面 | 能力 | 依赖接口 |
| --- | --- | --- |
| 登录 | 加载时校验会话；登录/登出；失败原因可见；会话过期回登录页并提示 | `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/session` |
| 总览 | 插件、主机、已下发、任务、近 24h 执行 KPI；待定项显式标注 | `GET /api/overview` |
| 插件管理 | 列表、详情（清单 YAML/能力/校验错误）、扫描、启用/停用、调用能力、按插件筛执行记录 | `/api/plugins*`、`/api/executions` |
| 主机管理 | 列表、详情、新增/编辑（密钥或密码）、删除、测试连接、下发插件、下发记录、撤销/重试 | `/api/hosts*`、`/api/distributions*` |
| 定时任务 | 列表（状态、下次触发、近 7 天错过数）、新建/编辑、启用/暂停、立即执行、历史 | `/api/tasks*` |
| 执行记录 | 按插件/能力/状态/来源/时间筛选；行内展开摘要与耗时 | `GET /api/executions` |
| 审计日志 | 仅管理员可见；按 action/result 筛选 | `GET /api/audit-events` |

统一交互要求：

- **空状态**：如「暂无插件，点击扫描插件目录」「暂无执行记录」；空数据不显示假数值。
- **错误态**：展示后端 `error.code` 与 `error.message`，保留重试入口；401 统一回登录页。
- **敏感信息**：凭据永不回显；编辑主机时凭据留空表示不修改。
- **边界表达**：下发结果固定显示「已下发不等于可调用」，不出现「可调用」暗示。
- **待定项**：无法从接口获得的指标标注「待定（OQ-xxx）」，不编造。

## 3. 总览统计口径（OQ-043）

`GET /api/overview` 返回：

```json
{
  "generated_at": "2026-11-16T14:40:00Z",
  "plugins": {"total": 2, "enabled": 1, "disabled": 1},
  "hosts": {"total": 1, "reachable": 0},
  "distributions": {"installed": 0},
  "tasks": {"total": 1, "enabled": 0, "paused": 1, "unavailable": 0, "missed_7d": 0},
  "executions_24h": {"total": 3, "failed": 0, "ok": 3}
}
```

| 指标 | 口径 |
| --- | --- |
| 插件数 | 注册表全量；启用数按 `state=enabled` |
| 主机数 | `hosts` 全量；可达数按最近一次测试 `status=reachable` |
| 已下发 | `distributions` 中 `state=installed` 的数量（**不代表可调用**） |
| 任务数 | 全量并按 `enabled` / `paused` / `unavailable` 分组；`missed_7d` 为近 7 天 `missed` 执行记录数 |
| 执行 | 最近 24 小时；失败 = `status` 不属于 `ok` / `skipped` / `missed` |

统计只读聚合，不写审计；权限为 `overview.read`（三种角色均具备）。

## 4. 执行记录摘要（OQ-046）

- `plugin_executions` 新增 `input_summary`、`output_summary`（v6 迁移），各截断至 **2048 字节**；
  超出部分丢弃并附「…(已截断)」。
- 只保存摘要，**完整输入输出不落库**；空输入/输出存空串，界面显示「未记录」。
- 摘要可能包含业务数据，读取权限沿用 `execution.read`；如后续需要脱敏规则，在 OQ-046 定稿后收紧。

## 5. 权限与可见性

| 页面/动作 | admin | operator | viewer |
| --- | --- | --- | --- |
| 总览 | ✓ | ✓ | ✓ |
| 插件查看 / 调用 | ✓ / ✓ | ✓ / ✓ | ✓ / ✗ |
| 插件启用/停用 | ✓ | ✗ | ✗ |
| 主机查看 / 编辑 / 测试 / 下发 | ✓ / ✓ / ✓ / ✓ | ✓ / ✓ / ✓ / ✓ | ✓ / ✗ / ✗ / ✗ |
| 任务查看 / 编辑 / 执行 | ✓ / ✓ / ✓ | ✓ / ✓ / ✓ | ✓ / ✗ / ✗ |
| 执行记录 | ✓ | ✓ | ✓ |
| 审计日志 | ✓ | ✗ | ✗ |

前端按角色隐藏不可用操作只是体验优化，**真正的拒绝在服务端**；越权请求的错误必须展示。

## 6. 偏差与未实现

| 项 | 处理 | 关联 |
| --- | --- | --- |
| 用户与角色管理页面 | 未实现（M2 只提供本地账号与 API） | OQ-037 |
| 总览趋势图 | 未实现，仅当前快照 | OQ-043 |
| 执行记录全文/日志查看 | 仅摘要 | OQ-046 |
| 跨站点防护 | Cookie SameSite=Strict + `X-Ops-Request` 头，未引入 CSP/Token | ADR-0006 |
| 告警通道 | 无外部告警，界面展示 missed 与失败数 | OQ-033 |
| 插件实例视图 | 未实现。当前插件页是**类型级**视图；按 [DOC-17](17-plugin-registry-and-configuration.md) §9 需要的实例列表、按能力反查、三态与漂移提示、实例配置编辑、探测历史均未落地 | ADR-0023 / OQ-049 |
| 远端三态展示 | 未实现。远端正处于「已安装但不可调用」阶段，界面不得据此暗示可调用 | DOC-09 §6.3 |

> **R0 偏差**：M5 的插件页与 `/api/plugins*` 只覆盖本机命令式插件；类型/实例分离后的接口族（`/api/plugin-types`、`/api/plugin-instances`、`/api/capabilities/{capability}/instances` 等）尚未实现，见 [DOC-17](17-plugin-registry-and-configuration.md) §8。

## 7. 验证

```powershell
# 1. 构建（同时同步 prototype → web/dist）
.\scripts\build.ps1
# 2. 启动
.\bin\operations-platform.exe --config .\smoke.yaml
# 3. 浏览器打开 http://127.0.0.1:18080 ，用内置管理员登录
```

| 场景 | 预期 |
| --- | --- |
| 未登录访问控制台 | 显示登录页，不发业务请求 |
| 错误密码 | 提示「用户名或密码错误」；连续失败提示稍后再试 |
| 会话过期后操作 | 回登录页并提示「会话已过期，请重新登录」 |
| 空插件目录 | 插件页显示空状态与扫描入口 |
| 调用能力 | 展示 invocation_id、耗时、输出或错误码 |
| 编辑主机 | 凭据字段为空且有「留空表示不修改」提示 |
| 下发记录 | 显示状态机阶段与「已下发不等于可调用」 |
| 任务执行历史 | 来源区分 manual / scheduled，missed 有提示 |
| 只读账号 | 无启用/编辑/执行按钮，直接调用接口返回 403 |
