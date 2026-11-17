# 任务清单：使用手册 + plugin-spec 核对

> 依赖：spec.md 经用户确认后执行。

## T1 编写 `docs/19-usage-and-configuration.md`

- 按 spec §3.1 的 5 个章节落文。
- 配置表使用 spec §2.2 的真实默认值；未定项标 OQ。
- 首次使用部分给出可直接照抄的 curl 示例：
  - 登录：`POST /api/auth/login`，带 `X-Ops-Request: 1` 与 Cookie。
  - 添加主机：`POST /api/hosts`，`auth_method: key` 与 `password` 两组示例。
  - 测试：`POST /api/hosts/{id}/test`。
  - 下发：`POST /api/hosts/{id}/distributions`。
  - 启用实例：`POST /api/plugin-instances/{id}/enable`。
  - 定时任务：`POST /api/tasks`。
- FAQ 覆盖：`credential_key_missing`、`package_missing`/`version_mismatch`/`unit_missing`/`cert_mismatch`、`callable=false` 排查顺序、`session_invalid`、`csrf_rejected`。
- 不重复 DOC-11~18 细节，引用对应文档。

## T2 更新 `docs/README.md`

- 文档清单新增 `DOC-19 | 19-usage-and-configuration.md | 部署、配置、首次使用与 FAQ | 运维/部署 | draft`。
- 依赖图新增 `19 使用手册`，箭头指向 11/13/14/15/18。
- 文件清单（§7 附近）新增该文件。
- 当前基线补充一句：DOC-19 为使用手册。

## T3 更新 `.ai/index.md`

- 新增 DOC-19 条目：路径、用途、关键词（部署、配置、credential_key、主机、下发、三态、FAQ）。
- 更新「最近更新」或等价区块（若存在）。

## T4 `plugin-spec/` 修订

按 spec §3.3 逐文件执行：

- README：实现状态块、去死链、补骨架入口。
- 00：增加 daemon 形态检查项、去 `docs/` 死链。
- 01：§3.6 实现状态、去 ADR 链接。
- 03：实现状态块、去 ADR 链接、明确生成物/常量/两套骨架入口。
- 04/05/06/07：去死链；05 明确命令式 vs daemon 错误语义对照。
- 08：实现状态注解、去死链。
- 09：补骨架入口示例（如缺）。
- 10：修正远端 daemon 与本机 daemon 条目、去 ADR 链接。
- 11：去 `../docs` 链接。
- manifest.template.yaml / manifest.schema.json：核对 1.2 字段集，不新增字段。
- AGENTS.md：去 `docs/` 相对链接。

## T5 链接与一致性校验

- 在 `plugin-spec/` 搜索 `](../docs` 与 `](../examples`，应为 0。
- 检查 `docs/README.md` 与 `.ai/index.md` 的 DOC-19 链接可达。
- 检查手册中的默认值与 `internal/config/config.go` 一致。
- 不改 `docs/18:45`。

## T6 汇报

- 手册路径与章节清单。
- plugin-spec 修补点清单（哪些补了）。
- 待用户决定项：
  1. `docs/18:45` 与代码懒校验矛盾，是否改。
  2. `identity.Bootstrap` 告警文案 `bootstrap_admin_password` 与实际键 `admin_password` 不一致，是否修。
