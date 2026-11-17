# DOC-13 M2 实现基线（本地身份、会话与能力级授权）

> 状态：`draft`（口令参数、会话 TTL、限速与角色矩阵属于**实现基线，可回改**，登记为 [OQ-040](07-open-questions.md)）
> 上游：[DOC-06](06-non-functional-requirements.md) §7、[ADR-0006](adr/0006-plugin-authz.md)、[ADR-0017](adr/0017-golang-x-crypto.md)、[DOC-08](08-storage-and-data-directory.md)
> 下游：[DOC-12](12-m1-plugin-runtime.md)、[DOC-14](14-host-management-m3.md)、[DOC-15](15-m4-scheduled-tasks.md)

## 1. 已实现范围

| 项 | M2 实现 |
| --- | --- |
| 账号 | 本地账号，字段：`user_id`、`username`、`display_name`、`role`、`password_hash`、`disabled` |
| 口令 | PBKDF2-HMAC-SHA256，210000 次迭代、16 字节随机盐、32 字节输出；只存派生值 |
| 角色 | `admin`、`operator`、`viewer` 三级；一账号一角色 |
| 登录 | `POST /api/auth/login`，成功后下发会话 Cookie |
| 登出 | `POST /api/auth/logout`，删除会话并清 Cookie |
| 会话校验 | 所有 `/api/*`（登录与版本接口除外）强制校验；过期/非法分别返回 `session_expired` / `session_invalid` |
| 限速 | 同一账号连续 5 次失败锁定 5 分钟；锁定期间返回 429 + `Retry-After` |
| 授权 | 能力级「资源类别 + 动作」判定，默认拒绝；判定结果写审计 |
| 审计 | 登录成败、登出、授权拒绝、插件启停、能力调用 |
| 外部身份源 | 仅 `identity.ExternalProvider` 接口与本文说明，不实现、不装配 |

不在 M2：账号管理界面与增删改接口（角色矩阵固化在代码）、LDAP/SSO、密码找回、多因素认证。

## 2. 口令与会话

```text
password_hash = pbkdf2_sha256$<iterations>$<salt-base64>$<key-base64>
```

- 校验使用常量时间比较；对外统一返回「用户名或口令错误」，不区分账号是否存在。
- 会话令牌为 256 位随机值（base64url）；库内只保存 `SHA-256(token)`，库泄露不等价于会话被接管。
- 会话固定有效期 12 小时（`session_ttl`），不滑动续期；`last_seen_at` 每分钟最多更新一次。
- 登录成功时顺带清理过期会话。

初始管理员：

- 启动时若 `users` 表为空且配置了 `admin_password`，创建 `admin`（角色 `admin`）。
- 未配置 `admin_password` 时**不创建账号**，只记录告警；此时登录不可用，不会生成无法告知用户的随机口令。
- `admin_password` 只从配置文件或环境变量读取，不写库、不写日志。

## 3. 会话传输与 CSRF

| 项 | 取值 |
| --- | --- |
| Cookie 名 | `ops_session` |
| 属性 | `HttpOnly`、`SameSite=Strict`、`Path=/`；HTTPS 下追加 `Secure` |
| 失效 | 登出、过期、账号禁用时服务端删除会话 |
| CSRF | 非安全方法必须满足其一：`Origin`/`Referer` 与 `Host` 同源，或携带 `X-Ops-Request: 1` |

未满足 CSRF 条件时返回 403 `csrf_rejected`，不执行业务逻辑。登录接口豁免会话校验，但仍做同源/标识校验以外的输入校验。

## 4. 角色矩阵（实现基线，OQ-040）

| 动作 | admin | operator | viewer |
| --- | --- | --- | --- |
| `plugin.read` | ✓ | ✓ | ✓ |
| `plugin.invoke` | ✓ | ✓ | ✗ |
| `plugin.enable` / `plugin.disable` | ✓ | ✗ | ✗ |
| `execution.read` | ✓ | ✓ | ✓ |
| `audit.read` | ✓ | ✗ | ✗ |
| `host.read` | ✓ | ✓ | ✓ |
| `host.write` / `host.test` / `host.distribute` | ✓ | ✓ | ✗ |
| `task.read` | ✓ | ✓ | ✓ |
| `task.write` / `task.run` | ✓ | ✓ | ✗ |
| `overview.read` | ✓ | ✓ | ✓ |

原则：默认拒绝；只读角色不产生副作用；启停插件与查看审计是管理员保留动作。

## 5. 审计

`audit_events` 字段：`occurred_at`、`subject`、`action`、`target_type`、`target_id`、`result`、`reason`、`details`。

| 场景 | `action` | `result` |
| --- | --- | --- |
| 登录成功 | `auth.login` | `success` |
| 登录失败 / 锁定 | `auth.login` | `failure` / `denied` |
| 授权拒绝 | 对应动作名 | `denied` |
| 插件启用 / 停用 | `plugin.enable` / `plugin.disable` | `success` |
| 能力调用 | `plugin.invoke` | `success` / `failure` |
| 审计查询被拒 | `audit.read` | `denied` |

审计与执行记录共用批量写队列（默认 50 条 / 200ms），关闭时排空。`details` 禁止写入口令、令牌、密钥或凭据。

## 6. 对外接口

| 路径 | 方法 | 说明 |
| --- | --- | --- |
| `/api/auth/login` | POST | body `{username,password}`；成功下发 Cookie，返回 `user` 与 `expires_at` |
| `/api/auth/logout` | POST | 删除会话并清 Cookie |
| `/api/auth/session` | GET | 返回当前 `user`；未登录 401 |
| `/api/audit-events` | GET | 审计列表，`action`/`result`/`from`/`to`/`limit`；仅管理员 |

错误码：`invalid_credentials`(401)、`account_disabled`(403)、`rate_limited`(429)、`session_invalid`(401)、`session_expired`(401)、`csrf_rejected`(403)、`permission_denied`(403)。

## 7. 测试

| 测试 | 文件 |
| --- | --- |
| 派生值不可逆、随机盐、常量时间校验 | `internal/identity/password_test.go` |
| 登录、会话过期、登出、限速、引导管理员缺省行为、角色矩阵 | `internal/identity/service_test.go` |

## 8. 偏差与待确认

| 项 | 处理 |
| --- | --- |
| 角色矩阵 | 规格未定，按最小副作用原则实现，登记 OQ-040 |
| 口令复杂度 | 未定；只做非空与长度上限校验，不自行加复杂度规则 |
| 账号管理接口 | M2 未提供，账号由内置管理员与后续版本维护 |
| 外部身份源 | 仅接口 `identity.ExternalProvider`，不实现（OQ-037） |
| 会话续期 | 固定 TTL，不滑动续期；如需调整须改 DOC-13 与 OQ-040 |
