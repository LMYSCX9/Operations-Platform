# TASKS：M1–M5 实施任务清单

> 对应 [spec.md](spec.md)。按顺序执行，每项完成后立即验证；里程碑结束汇报一次。

## M1 插件运行时

- [x] T1.1 配置改 YAML：重写 `internal/config/config.go`（yaml tag、`KnownFields(true)`、20 个新键、相对 plugin_dir 校验），更新 `config_test.go`。
  - 验证：`go test ./internal/config/...`；未知键报错；`plugin_dir` 默认为 `<data_dir>/plugins`。
- [x] T1.2 新增 `plugin-spec/manifest.schema.json` 与 `internal/plugin/manifest/schema.json`（同内容，embed）。
- [x] T1.3 实现 `internal/plugin/manifest`：`types.go`、`validate.go`、`contract.go`；字段路径错误。
  - 验证：`go test ./internal/plugin/manifest/...`（正反例、契约区间）。
- [x] T1.4 `store` 升到 v5 迁移框架，先落 v2 两张表；新增 `Store.Read`；重写 `queue.go` 为 `BatchQueue`。
  - 验证：`go test ./internal/store/...`（迁移、队列批量与关闭不丢）。
- [x] T1.5 实现 `registry`（注册/查询/状态流转/摘要变更）与 `discovery`（扫描、幂等、拒绝清单）。
- [x] T1.6 实现 `runtime`（NDJSON、超时、进程组终止、体积上限、退出码映射、stderr 隔离）。
  - 验证：`go test ./internal/plugin/runtime/...`（helper process 覆盖 9 类场景）。
- [x] T1.7 实现 `authz` 占位与 `plugin.Service` 编排（信号量、serial 锁、执行记录入队）。
- [x] T1.8 `api` 增加插件/执行记录接口与统一错误响应；`main.go` 装配扫描与队列关闭。
- [x] T1.9 示例插件 `demo-echo`（echo/fail）与反例 `broken-demo`；构建脚本生成 `bin/demo-echo*`。
- [x] T1.10 文档：`docs/12-m1-plugin-runtime.md`、ADR-0016/0017、docs/07 OQ-039、ADR-0009 追加、docs/06 §5.1、docs/11、docs/README、.ai/index.md、.ai/open-questions.yaml。
- [x] T1.11 M1 验证：扫描 → 启用 → 调用 → 执行记录；汇报。

## M2 登录与权限

- [x] T2.1 迁移 v3：users/sessions/audit_events。
- [x] T2.2 `identity/password.go`（PBKDF2 210000/16/32，常量时间比较）+ 单测。
- [x] T2.3 `identity/store.go`、`service.go`：引导管理员、登录、登出、会话校验、固定 TTL、内存限速。
- [x] T2.4 `identity/authz.go`：角色→动作矩阵；未登录 401、无权限 403。
- [x] T2.5 `observability/audit.go` + 批写；在插件/主机/任务/登录路径接入审计。
- [x] T2.6 `api` 中间件：Cookie 解析、CSRF（Origin + `X-Ops-Request`）、错误映射；登录/登出/会话/审计接口。
- [x] T2.7 单测：口令派生、限速、会话过期、角色矩阵、401/403/429。
- [x] T2.8 文档：`docs/13-m2-identity-and-authz.md`、OQ-040、.ai 索引。
- [x] T2.9 M2 验证并汇报。

## M3 主机与下发

- [x] T3.1 迁移 v4：hosts/host_credentials/distributions。
- [x] T3.2 `secret.Box`（AES-256-GCM，nonce 随机，密钥缺失显式失败）+ 单测。
- [x] T3.3 `host` 模型与 store：主机 CRUD、凭据替换（永不回显）、状态字段。
- [x] T3.4 `host/ssh.go`：x/crypto/ssh、TOFU 指纹锁存、指纹变更阻断、超时、测试连接（uname 判定 Linux）。
- [x] T3.5 `host/distributor.go`：tar.gz、上传解包、sha256 校验、状态机、每主机串行 + 全局并发、幂等、撤销、重启置 interrupted。
- [x] T3.6 `api` 主机与下发接口 + 授权 + 审计。
- [x] T3.7 单测：加密往返与篡改失败、指纹处理、幂等、状态流转（用假 SSH 传输接口注入）。
- [x] T3.8 文档：`docs/14-host-management-m3.md`、OQ-041、.ai 索引；ADR-0017 标注 accepted/proposed 现状。
- [x] T3.9 M3 验证并汇报（如需真实主机，汇报中说明可选用本机 sshd）。

## M4 定时任务

- [x] T4.1 迁移 v5：tasks 表 + `plugin_executions.task_id`。
- [x] T4.2 `scheduler/cron.go`：5 段解析与 `Next`，含 `*/n`、区间、列表；单测覆盖边界与非法语法。
- [x] T4.3 `scheduler/store.go`：任务 CRUD、认领 SQL、next_run_at 计算。
- [x] T4.4 `scheduler/scheduler.go`：到期扫描、先认领后执行、重叠跳过、错过记录、崩溃恢复、重试、优雅关闭。
- [x] T4.5 `api` 任务接口 + 授权 + 审计；手动执行走同一调用路径。
- [x] T4.6 单测：认领并发唯一、重启恢复、跳过/错过记录、非法 cron、凭据参数拒绝。
- [x] T4.7 文档：`docs/15-scheduled-tasks-m4.md`、OQ-042、.ai 索引。
- [x] T4.8 M4 验证并汇报。

## M5 前端

- [x] T5.1 重写 `prototype/index.html`：登录页 + 会话处理 + fetch 封装（同上源、`X-Ops-Request`、错误提示）。
- [x] T5.2 总览真实统计接口 `GET /api/overview`（新增，M5 需要；归入 OQ-039 之外的实现基线并登记 OQ-043）。
- [x] T5.3 插件页：列表/详情/启用停用/调用/执行记录筛选。
- [x] T5.4 主机页：列表/详情/新增编辑/删除/测试/下发/撤销/记录；凭据留空不替换。
- [x] T5.5 任务页：列表/新建编辑/启停/立即执行/历史。
- [x] T5.6 执行记录页与审计页：真实筛选与行内展开。
- [x] T5.7 空状态、错误态、待定标注；构建脚本同步 `web/dist/index.html`。
- [x] T5.8 文档：`docs/16-web-ui-m5.md`、docs/README、.ai 索引；M5 验证并汇报。

## 收尾

- [x] T6.1 全量 `gofmt -l .`、`go vet ./...`、`go test ./...`、两平台构建脚本。
- [x] T6.2 汇总最终构建与启动方式、每页能力、遗留 OQ 与未实现项。
