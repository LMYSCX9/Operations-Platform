# SPEC：M1–M5 运维平台底座实现规格

- 状态：`draft`（待用户批准后进入实现）
- 依据：`AGENTS.md`、`docs/00~11`、`docs/adr/0001~0015`、`plugin-spec/01~09`
- 适用范围：M1 插件运行时、M2 登录与权限、M3 主机与下发、M4 定时任务、M5 前端接真实接口
- 不在范围：M5 之后的新功能、多节点高可用、跨主机运行与调用插件（OQ-010/OQ-027）、LDAP/SSO 实现（仅留接口）

## 1. 目标与验收总纲

| 里程碑 | 可运行节点 | 验收方式 |
| --- | --- | --- |
| M1 | 示例插件被扫描登记、显式启用、HTTP 调用成功，执行记录可查 | `go test ./...` + 启动后实测 5 个接口 |
| M2 | 登录/登出/会话校验生效，未登录 401，角色越权 403，审计可查 | 三种角色 curl 实测 |
| M3 | 主机 CRUD、加密凭据不可回显、测试连接、下发安装到真实/本地 SSH 目标 | 对本机 sshd 或容器实测 |
| M4 | cron 与固定间隔任务到点执行，重启不丢不重复，执行历史可查 | 创建 1 分钟任务观察两次触发 |
| M5 | 原型页面全部走真实接口，无 mock 数据 | 浏览器实测每个页面 |

每个里程碑结束：`gofmt -l .` 无输出、`go vet ./...` 通过、`go test ./...` 通过、构建脚本成功，并立刻
`send_message_to_session` 汇报。

## 2. 已定契约与不得编造的边界

直接实现（不改契约）：清单 25 字段、能力标识前缀、stdin/stdout 单行 NDJSON、错误码枚举、退出码 0~8、
契约版本范围 `MAJOR.MINOR` + `>= < =`、发现/校验顺序、安装默认禁用、能力级授权默认拒绝。

契约未定、本次必须给「可运行基线」的项，一律登记为新的开放问题并在文档标注，不视为最终规格：

| 新编号 | 内容 |
| --- | --- |
| OQ-039 | M1 基线：配置键与默认值、SQLite v2 表结构、插件 HTTP 路径、清单 schema 文件位置、批写队列参数 |
| OQ-040 | M2 基线：口令派生算法与参数、会话 Cookie 属性与 TTL、登录限速阈值、用户/会话/审计表结构、HTTP 路径 |
| OQ-041 | M3 基线：SSH 库选型、主机/凭据/下发表结构、远端安装根目录、连接/传输超时与并发默认值、指纹存储方式 |
| OQ-042 | M4 基线：任务与触发表结构、cron 解析范围、最小间隔/超时/重试/扫描周期默认值、HTTP 路径 |

## 3. 依赖与 ADR

| 依赖 | 用途 | ADR |
| --- | --- | --- |
| `gopkg.in/yaml.v3` v3.0.1（已加入 go.mod） | 配置与清单 YAML | ADR-0016（proposed，随本批实现） |
| `github.com/santhosh-tekuri/jsonschema/v5` v5.3.1（已加入 go.mod） | 清单 JSON Schema 校验 | ADR-0016 |
| `golang.org/x/crypto`（ssh、pbkdf2） | M2 口令派生、M3 SSH | ADR-0017（proposed） |

ADR-0016：YAML + 内置 JSON Schema 校验依赖，理由为 OQ-005 已定「清单 YAML + 内置 JSON Schema」，配置同格式复用同一依赖；替代方案为自写解析器（否决：易错）与外部校验服务（否决：破坏单二进制）。
ADR-0017：SSH 传输使用 `golang.org/x/crypto/ssh`（纯 Go），理由为 ADR-0014 已推荐「Go 内置实现、纯 Go」；替代方案为调用系统 `ssh` 进程（否决：凭据经进程参数泄露、无指纹锁存）。
ADR-0009 追加 YAML 决策段：配置格式改 YAML，与清单统一；JSON 兼容读取**不做**（避免双格式歧义）。

## 4. 目录与文件级改动

### 4.1 新增/修改文件清单

```
go.mod / go.sum                               修改：新增 3 个依赖
internal/config/config.go                     重写：YAML + 新增 20 个配置项
internal/config/config_test.go                修改：YAML 用例 + 新键默认值/覆盖用例
internal/store/store.go                       修改：CurrentSchemaVersion=5，注册 v2~v5 迁移
internal/store/queue.go                       重写：真实批量写队列
internal/store/queue_test.go                  新增
internal/store/datadir.go                     修改：新增 backups 之外的 distributions 临时目录？否，复用 tmp
internal/observability/log.go                 不变
internal/observability/audit.go               新增：审计事件写入（批量队列）
internal/plugin/manifest/types.go             新增：25 字段结构体
internal/plugin/manifest/schema.json          新增：清单 JSON Schema（与 plugin-spec 副本一致）
internal/plugin/manifest/validate.go          新增：解析 + Schema + 语义校验
internal/plugin/manifest/contract.go          新增：契约版本与范围
internal/plugin/manifest/validate_test.go     新增：正反例、版本区间
internal/plugin/registry/registry.go          新增：plugins 表 CRUD 与状态流转
internal/plugin/runtime/stdio.go              新增：NDJSON 子进程调用
internal/plugin/runtime/errors.go             新增：错误类别与退出码映射
internal/plugin/runtime/stdio_test.go         新增：超时、体积、协议违规
internal/plugin/discovery/discovery.go        新增：扫描与幂等登记
internal/plugin/authz/authz.go                新增：Authorizer 接口 + StaticAdmin
internal/plugin/service.go                    新增：编排（发现/启用/停用/调用/列表/执行记录）
internal/identity/password.go                 M2 新增：PBKDF2-SHA256
internal/identity/store.go                    M2 新增：users/sessions 读写
internal/identity/service.go                  M2 新增：登录/登出/校验/限速
internal/identity/authz.go                    M2 新增：角色 → 动作判定
internal/identity/*_test.go                   M2 新增
internal/secret/box.go                        M3 新增：AES-256-GCM
internal/secret/box_test.go                   M3 新增
internal/host/model.go                        M3 新增：主机/凭据/下发任务类型
internal/host/store.go                        M3 新增：hosts/host_credentials/distributions CRUD
internal/host/ssh.go                          M3 新增：连接、指纹校验、测试连接
internal/host/distributor.go                  M3 新增：串行队列 + 打包上传 + 校验 + 安装
internal/host/service.go                      M3 新增：编排与审计
internal/host/*_test.go                       M3 新增
internal/scheduler/cron.go                    M4 新增：5 段 cron 解析与 Next()
internal/scheduler/store.go                   M4 新增：tasks 表
internal/scheduler/scheduler.go               M4 新增：扫描、认领、执行、重试、错过记录
internal/scheduler/cron_test.go               M4 新增
internal/scheduler/scheduler_test.go          M4 新增
internal/api/server.go                        修改：路由注册、中间件、统一错误响应
internal/api/errors.go                        新增
internal/api/auth_handlers.go                 M2 新增
internal/api/plugin_handlers.go               M1 新增（M2 加授权）
internal/api/execution_handlers.go            M1 新增
internal/api/host_handlers.go                 M3 新增
internal/api/task_handlers.go                 M4 新增
internal/api/router_test.go                   新增：鉴权/状态码
cmd/ops-platform/main.go                      修改：装配 identity/host/scheduler，启动调度器与队列
plugin-spec/manifest.schema.json              新增：契约级 schema 权威副本
plugin-spec/README.md                         修改：登记 schema 文件
examples/plugins/demo-echo/plugin.yaml        新增
examples/plugins/demo-echo/main.go            新增：echo / fail 两个能力
examples/plugins/demo-echo/schemas/*.json     新增：4 个 schema
examples/plugins/broken-demo/plugin.yaml      新增：故意违规（capabilities[0].id 前缀错误、声明 dependencies）
examples/plugins/broken-demo/README.md        新增：说明这是反例
scripts/build.ps1 / build.sh                  修改：构建 demo-echo 到 bin/
prototype/index.html                          M5 重写：真实接口 + 登录页
web/dist/index.html                           由构建脚本同步
docs/12-plugin-runtime-m1.md                  新增
docs/13-identity-and-authorization-m2.md      新增
docs/14-host-management-m3.md                 新增
docs/15-scheduled-tasks-m4.md                 新增
docs/16-web-ui-m5.md                          新增
docs/07-open-questions.md                     修改：新增 OQ-039~042
docs/11-implementation-baseline-m0.md         修改：配置表标注 M1 起 YAML
docs/06-non-functional-requirements.md        修改：§5.1 配置格式 YAML
docs/adr/0009-configuration-model.md          修改：追加 YAML 决策
docs/adr/0016-dependencies-yaml-and-jsonschema.md  新增（proposed）
docs/adr/0017-ssh-library.md                  新增（proposed）
docs/README.md / AGENTS.md / .ai/index.md     修改：登记新文件
.ai/open-questions.yaml                       修改：OQ-039~042
```

### 4.2 包边界

- `internal/api` 只做 HTTP 编解码、鉴权中间件、错误映射，不写业务 SQL。
- `internal/plugin`、`internal/identity`、`internal/host`、`internal/scheduler` 各自持有自己的 SQL，统一经 `store.Store.Write`/
  `store.Store.Read` 访问，禁止跨包直接持有 `*sql.DB`。
- `store` 增加 `Read(ctx, fn func(*sql.Tx) error) error`（只读事务，便于多语句一致性读）。

## 5. M1：插件运行时最小闭环

### 5.1 配置（YAML）

配置文件为单个 YAML 文档，`-config` 指定；未知键使用 `yaml.Decoder.KnownFields(true)` 拒绝。
键名即 YAML 键，环境变量为 `OPS_PLATFORM_` + 大写下划线，命令行参数同 M0 风格。

| YAML 键 | 环境变量 | 命令行 | 默认值 | 校验 |
| --- | --- | --- | --- | --- |
| `data_dir` | `OPS_PLATFORM_DATA_DIR` | `-data-dir` | 无（必填绝对路径） | 不变 |
| `listen_addr` | `OPS_PLATFORM_LISTEN_ADDR` | `-listen` | 无（必填） | 不变 |
| `log_level` | `OPS_PLATFORM_LOG_LEVEL` | `-log-level` | `info` | 不变 |
| `shutdown_timeout` | `OPS_PLATFORM_SHUTDOWN_TIMEOUT` | `-shutdown-timeout` | `10s` | >0 |
| `plugin_dir` | `OPS_PLATFORM_PLUGIN_DIR` | `-plugin-dir` | `<data_dir>/plugins` | 绝对路径；相对路径按 data_dir 解析后必须仍在 data_dir 下 |
| `plugin_hot_reload` | `OPS_PLATFORM_PLUGIN_HOT_RELOAD` | `-plugin-hot-reload` | `false` | bool |
| `plugin_scan_interval` | `OPS_PLATFORM_PLUGIN_SCAN_INTERVAL` | `-plugin-scan-interval` | `30s` | ≥1s |
| `plugin_invoke_timeout` | `OPS_PLATFORM_PLUGIN_INVOKE_TIMEOUT` | `-plugin-invoke-timeout` | `30s` | >0 |
| `plugin_max_result_bytes` | `OPS_PLATFORM_PLUGIN_MAX_RESULT_BYTES` | `-plugin-max-result-bytes` | `8388608` | 1KiB~1GiB |
| `plugin_max_concurrency` | `OPS_PLATFORM_PLUGIN_MAX_CONCURRENCY` | `-plugin-max-concurrency` | `20` | 1~1000 |
| `execution_queue_batch` | `OPS_PLATFORM_EXECUTION_QUEUE_BATCH` | — | `50` | 1~10000 |
| `execution_queue_interval` | `OPS_PLATFORM_EXECUTION_QUEUE_INTERVAL` | — | `200ms` | 10ms~10s |
| `session_ttl`（M2） | `OPS_PLATFORM_SESSION_TTL` | — | `12h` | >0 |
| `login_max_attempts`（M2） | `OPS_PLATFORM_LOGIN_MAX_ATTEMPTS` | — | `5` | 1~100 |
| `login_lockout`（M2） | `OPS_PLATFORM_LOGIN_LOCKOUT` | — | `5m` | ≥1s |
| `credential_key`（M3） | `OPS_PLATFORM_CREDENTIAL_KEY` | — | 无 | base64(32 字节)；缺失时所有凭据操作显式失败 |
| `ssh_connect_timeout`（M3） | `OPS_PLATFORM_SSH_CONNECT_TIMEOUT` | — | `10s` | >0 |
| `ssh_upload_timeout`（M3） | `OPS_PLATFORM_SSH_UPLOAD_TIMEOUT` | — | `5m` | >0 |
| `distribution_max_concurrency`（M3） | `OPS_PLATFORM_DISTRIBUTION_MAX_CONCURRENCY` | — | `4` | 1~64 |
| `distribution_remote_root`（M3） | `OPS_PLATFORM_DISTRIBUTION_REMOTE_ROOT` | — | `/opt/ops-platform/plugins` | 绝对路径 |
| `task_scan_interval`（M4） | `OPS_PLATFORM_TASK_SCAN_INTERVAL` | — | `30s` | ≥1s |
| `task_min_interval`（M4） | `OPS_PLATFORM_TASK_MIN_INTERVAL` | — | `1m` | ≥1m |
| `task_default_timeout`（M4） | `OPS_PLATFORM_TASK_DEFAULT_TIMEOUT` | — | `5m` | >0 |
| `task_default_retries`（M4） | `OPS_PLATFORM_TASK_DEFAULT_RETRIES` | — | `1` | 0~10 |

`Redacted()` 输出上述键；`credential_key` 只输出 `configured: true/false`。

### 5.2 清单 schema 与校验

`plugin-spec/manifest.schema.json`（Draft-07）与 `internal/plugin/manifest/schema.json` 内容一致，用 `//go:embed` 加载。
要点：
- `identity/version/form/capabilities` 必填；`entry` 必填。
- `identity.id`、能力名段 `pattern: "^[a-z][a-z0-9-]*$"`；`version.plugin` pattern 三段数字；`form` enum；`concurrency` enum。
- `entry` 用 `oneOf`：`form=command` 时须有 `executable` 且无 `launch`/`socket`；`form=daemon` 时须有 `launch`。
- `properties` 中显式写 `"dependencies": false`、`"password": false`、`"secret": false`、`"token": false`、`"credential": false`、`"credentials": false`、`"api_key": false`、`"private_key": false`、`"access_key": false`。
- `additionalProperties: true`（未知字段忽略）。

Go 侧语义校验（在 schema 之后），错误统一为 `[]FieldError{Path, Reason}`：
1. 能力数量 ≥1；`capabilities[i].id` 必须以 `<identity.id>.` 开头，剩余段匹配正则；能力 id 不得重复。
2. capability schema 引用文件存在、在包根内、可读；`suggested_timeout_ms` >0。
3. `entry` 路径存在、在包根内、为普通文件；`command` 形态额外要求可执行位（Windows 跳过可执行位检查）。
4. 递归扫描 YAML 键名，命中凭据正则 `(?i)^(password|passwd|secret|token|api[_-]?key|private[_-]?key|credential|credentials|access[_-]?key)$` → 报 `字段路径: 非法字段（v1 禁止凭据字段）`。
5. `version.contract` 解析为范围；与底座 `${ContractVersion}=1.0` 判定（§5.3）。

返回值：`*manifest.Manifest`（含原始 YAML 与 SHA-256）、`[]FieldError`、`error`；`FieldError.Error()` 形如
`capabilities[0].input_schema_ref: 文件 "schemas/x.json" 不存在`。

### 5.3 契约版本

`internal/plugin/manifest/contract.go`：

```go
type Version struct{ Major, Minor int }
func ParseVersion(s string) (Version, error)          // "1.0"
type Constraint struct{ Op string; V Version }        // >=, <, =
type Range struct{ Constraints []Constraint }
func ParseRange(s string) (Range, error)              // ">=1.0 <2.0"
func (r Range) Contains(v Version) bool
const ContractVersion = "1.0"                          // 底座当前契约
func CheckContract(r Range) error                      // 不含 ContractVersion → version_incompatible
```

规则：空格分隔；操作符仅 `>=`、`<`、`=`；比较按 `(Major, Minor)` 字典序；空范围、非法操作符、重复 `=` 报错。
`CheckContract` 失败原因：`version.contract: 声明范围 ">=2.0" 不包含底座支持的契约版本 1.0`。

### 5.4 注册表（SQLite v2）

```sql
CREATE TABLE plugins (
  plugin_id        TEXT PRIMARY KEY,
  plugin_version   TEXT NOT NULL,
  form             TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('disabled','enabled')),
  install_path     TEXT NOT NULL,
  manifest_yaml    TEXT NOT NULL,
  manifest_sha256  TEXT NOT NULL,
  contract_ok      INTEGER NOT NULL,
  contract_reason  TEXT NOT NULL DEFAULT '',
  discovered_at    TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);
CREATE TABLE plugin_executions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  invocation_id   TEXT NOT NULL,
  plugin_id       TEXT NOT NULL,
  capability_id   TEXT NOT NULL,
  source          TEXT NOT NULL,             -- manual | scheduled | api
  subject         TEXT NOT NULL,
  started_at      TEXT NOT NULL,             -- RFC3339Nano UTC
  finished_at     TEXT NOT NULL,
  duration_ms     INTEGER NOT NULL,
  status          TEXT NOT NULL,             -- ok | error | timeout | skipped | missed | interrupted
  error_category  TEXT NOT NULL DEFAULT '',
  task_id         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_plugin_executions_plugin ON plugin_executions(plugin_id, started_at DESC);
CREATE INDEX idx_plugin_executions_started ON plugin_executions(started_at DESC);
```

注册语义：
- 主键 `plugin_id`；发现同一 id 且 `manifest_sha256` 未变 → 不写库（幂等，仅更新内存视图）。
- 摘要变化 → 更新 `plugin_version/manifest_*/install_path/updated_at`，`state` **保持不变**（不因升级自动启用）。
- 已登记启用中插件若新发现版本 `contract_ok=0` → 保留旧清单与 `contract_ok=1`，并把冲突写入日志与 `GET /api/plugins` 的 `warnings`；不自动停用。此外新增 `rejected` 记录？**不做**：契约要求「不登记」。冲突通过返回体 `conflicts[]` 暴露。
- 状态流转只允许 `disabled ↔ enabled`；启用前复核 `contract_ok=1` 且清单仍可解析。

### 5.5 发现

`discovery.Scanner`：
- 扫描 `plugin_dir` 一级子目录（忽略隐藏目录与文件）；每个子目录必须含 `plugin.yaml`，否则记 `rejected`（日志 + `warnings`），不中断其他目录。
- 不执行任何插件代码；只读文件。
- 热加载：`plugin_hot_reload=true` 时起 ticker（`plugin_scan_interval`），否则仅启动时扫描一次；`POST /api/plugins/scan` 手动触发。
- 扫描串行化（互斥锁），并发触发时合并为一次。

### 5.6 stdio 运行时

请求（`invokeRequest`）：

```go
type InvokeRequest struct {
  ContractVersion string         `json:"contract_version"`
  Capability      string         `json:"capability"`
  Input           json.RawMessage `json:"input"`
  Context         InvokeContext  `json:"context"`
}
type InvokeContext struct {
  InvocationID string `json:"invocation_id"`
  DeadlineMS   int64  `json:"deadline_ms"`
  Subject      string `json:"subject,omitempty"`
}
```

响应（`invokeResponse`）：`status`、`output`、`plugin_version`、`error{code,message,details,retryable}`。

执行器 `runtime.CommandInvoker.Invoke(ctx, spec)` 步骤：
1. 解析入口路径，Windows 下若文件不存在且 `<path>.exe` 存在则回退（记 `warnings`，OQ-039）。
2. `exec.CommandContext`；`cmd.Dir = 包根`；stdin 写入一行 JSON 后关闭；不继承宿主环境之外的额外变量。
3. stdout 用限量 writer（上限 `max_result_bytes+1`），stderr 用 64 KiB 环形截断 writer。
4. 超时：`context.WithTimeout(plugin_invoke_timeout)`；超时后先 `Process.Signal(os.Interrupt)`，2s 宽限后 `Kill`；Unix 用进程组终止（`syscall.Kill(-pgid, SIGKILL)`，`Setpgid: true`）。
5. `cmd.Wait()` 后判定，判定顺序固定：
   1. 超时 → `timeout`
   2. stdout 字节数 > 上限 → `result_too_large`
   3. stdout 为空 → `invalid_response`（"未输出结果"）
   4. 按 `\n` 切分，去掉末尾空行后非空行数 != 1 → `protocol_invalid`
   5. JSON 解析失败 → `invalid_response`
   6. `status` 非 `ok|error` → `protocol_invalid`
   7. `status=ok` 且退出码 != 0 → `protocol_invalid`
   8. `status=error`：`code` 不在枚举 → `invalid_response`；退出码 0 → `protocol_invalid`；退出码与 `code` 语义不一致 → `protocol_invalid`（message 指明两者）
   9. 未知退出码（≥9 或信号）→ `internal_error`，message 带实际退出码/信号
6. 结果只取 stdout；stderr 逐行以 `plugin_stderr` 字段写日志（最多 20 行，单行 1KiB）。

退出码映射（`runtime.ExitCodeCategory`）：`0→""`、`1→internal_error`、`2→input_invalid`、`3→capability_unknown`、
`4→permission_denied`、`5→timeout`、`6→result_too_large`、`7→dependency_unavailable`、`8→protocol_invalid`。

### 5.7 授权（M1 占位，M2 落地）

```go
type Subject struct{ ID string; Roles []string }
type Authorizer interface {
  Authorize(ctx context.Context, subj Subject, action Action, target Target) Decision
}
type Decision struct{ Allowed bool; Reason string }
```

M1 装配 `authz.AllowAdmin`（固定 `user:admin`，`Roles: ["admin"]`）；M2 换成 `identity.Authorizer`。
默认拒绝：`Authorize` 未命中规则即 `Allowed=false`，原因 `"no matching rule"`。

### 5.8 编排与并发

`plugin.Service` 持有：registry、scanner、invoker、authorizer、queue、并发信号量（`plugin_max_concurrency`）、
每能力串行锁（`map[capabilityID]*sync.Mutex`，仅 `concurrency=serial` 使用）。

`Invoke(ctx, subject, pluginID, capabilityID, input, source)`：
1. `Authorize(plugin.invoke, capabilityTarget)`；拒绝 → 写审计 + 返回 `permission_denied`（不写执行记录？**写**，status=error，error_category=permission_denied）。
2. 查注册表：不存在 → `capability_unknown`；`state != enabled` → `not_ready`；能力未声明 → `capability_unknown`。
3. 契约复核 `contract_ok`；`deadline_ms` = `plugin_invoke_timeout` 毫秒。
4. 生成 `invocation_id`（`crypto/rand` 16 字节 hex）。
5. 取信号量；必要时加能力串行锁；调 invoker。
6. 组装 `plugin_executions` 记录并 `queue.Enqueue`。
7. 返回 `InvokeResult{InvocationID, Status, Output, Error}`。

### 5.9 批写队列

`store.BatchQueue`：`chan Record`（容量 1024）+ ticker(`execution_queue_interval`)；每次取至多 `batch` 条，
单事务写入；`Close` 时排空并最后一次 flush；Enqueue 在队列满时**阻塞**而不是丢弃（不静默丢记录）。
`Record.Kind` ∈ `plugin_execution`、`audit_event`。写入失败：记录错误日志并重试一次，仍失败则写 stderr 并在关闭时返回聚合错误。

### 5.10 HTTP（M1）

统一错误响应：

```json
{"error":{"code":"permission_denied","message":"主体 user:x 无 plugin.invoke 权限","details":{"action":"plugin.invoke","target":"demo-echo.echo"}}}
```
状态码映射：`input_invalid|version_incompatible→400`、`permission_denied→403`、`capability_unknown|not_found→404`、
`not_ready→409`、`result_too_large→413`、`timeout→504`、`dependency_unavailable→502`、其他 `500`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/plugins` | 列表：id、版本、形态、状态、契约兼容、能力 id、warnings、conflicts |
| GET | `/api/plugins/{id}` | 清单原文、解析后能力、校验结果、状态 |
| POST | `/api/plugins/scan` | 手动扫描，返回新增/更新/拒绝数 |
| POST | `/api/plugins/{id}/enable` | 启用；`contract_ok=0` → 400 |
| POST | `/api/plugins/{id}/disable` | 停用 |
| POST | `/api/plugins/{id}/capabilities/{capability}/invoke` | 请求体 `{"input":{...}}`，返回 `invocation_id/status/output/error` |
| GET | `/api/executions` | 过滤：`plugin_id`、`capability_id`、`status`、`source`、`from`、`to`、`limit`（默认 100，上限 500） |

### 5.11 示例插件

`examples/plugins/demo-echo/`：
- `main.go`：从 stdin 读一行 JSON；`demo-echo.echo` 返回 `{"echo": <input.text>}`；`demo-echo.fail` 按 `input.code`
  返回对应错误并以对应退出码退出；未知能力返回 `capability_unknown`（退出码 3）；stdout 仅一行 JSON，日志走 stderr。
- `plugin.yaml` 严格按 25 字段，`entry.executable: bin/demo-echo`，`permissions: data_dir read/write`。
- 4 个 schema 文件。
- 构建：`go build -o <插件目录>/bin/demo-echo(.exe) ./examples/plugins/demo-echo`，由两个构建脚本执行。

`examples/plugins/broken-demo/`：`plugin.yaml` 同时含 `dependencies`、能力 id 无插件前缀、`identity.id` 含下划线；
无入口文件。扫描后必须被拒绝，`warnings` 中给出字段路径。

### 5.12 测试

- `manifest/validate_test.go`：合法清单通过；id 非法、能力前缀错、缺必填、`dependencies`、凭据字段、schema 文件缺失、路径越界、form 与 entry 不匹配 → 均报出正确字段路径。
- `manifest/contract_test.go`：`>=1.0 <2.0` 通过；`>=2.0`、`<1.0`、`=1.1` 失败；非法语法报错。
- `runtime/stdio_test.go`：用 `go test` 编译的测试二进制扮演插件（`TestHelperProcess` 模式）覆盖 ok、错误、静默退出、多行输出、非 JSON、退出码不一致、体积超限、超时、stderr 不进结果。
- `store/queue_test.go`：批量写入条数与内容一致；关闭不丢。
- `plugin/discovery` + registry：同一目录扫描两次不产生重复行；摘要变化被更新；变更不改变 enabled。

## 6. M2：登录与权限

### 6.1 数据表（v3）

```sql
CREATE TABLE users (
  user_id       TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
  password_hash TEXT NOT NULL,
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE TABLE sessions (
  session_id  TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES users(user_id),
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE TABLE audit_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  occurred_at TEXT NOT NULL,
  subject     TEXT NOT NULL,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  result      TEXT NOT NULL,      -- allowed | denied | success | failure
  reason      TEXT NOT NULL DEFAULT '',
  details     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_occurred ON audit_events(occurred_at DESC);
```

### 6.2 口令与引导管理员

- 派生：PBKDF2-HMAC-SHA256，`iter=210000`、salt 16 字节随机、key 32 字节；存储格式
  `pbkdf2-sha256$210000$<salt_b64>$<key_b64>`；比较用 `subtle.ConstantTimeCompare`。
- 首次启动且 `users` 为空：
  - `OPS_PLATFORM_ADMIN_PASSWORD` 已设置 → 创建 `admin`（role=admin）并立即清除该环境变量值不再使用；
  - 未设置 → 生成 20 字符随机口令，写入 `<data_dir>/bootstrap-admin.txt`（0600），日志只输出文件路径与「请登录后修改」，**不输出口令**。
- 不提供默认口令、不允许空口令；口令长度 ≥12。

### 6.3 会话

- 登录 `POST /api/login` `{username,password}` → `Set-Cookie: ops_session=<32 字节 hex>; Path=/; HttpOnly; SameSite=Strict; Max-Age=<ttl>`。
- 校验：每请求读 Cookie，查会话且未过期；命中则滑动续期（`last_seen_at` 更新，`expires_at` 不延长超出 `session_ttl`，即固定 TTL）。
- 登出 `POST /api/logout` 删除会话并清 Cookie。
- `GET /api/session` 返回 `{subject, username, display_name, role, expires_at}`；未登录 401。
- 失效/过期统一 401 + `{"error":{"code":"unauthenticated"}}`；前端据此跳登录。
- CSRF：Cookie `SameSite=Strict`；对 `POST/PUT/DELETE` 额外校验 `Origin`（存在则必须与 `Host` 同源）与自定义头 `X-Ops-Request: 1`，缺失返回 403 `csrf_rejected`。
- 登录限速：内存滑动窗口，按 `username + 客户端 IP` 计；`login_max_attempts` 次失败后锁 `login_lockout`；锁定期返回 429 与剩余秒数；成功清零。

### 6.4 角色 → 动作

| 动作 | admin | operator | viewer |
| --- | --- | --- | --- |
| `plugin.read`、`execution.read`、`host.read`、`task.read`、`audit.read` | ✓ | ✓（不含 audit） | ✓（不含 audit） |
| `plugin.enable` / `plugin.disable` / `plugin.invoke` | ✓ | ✓ | ✗ |
| `host.test`、`host.distribute` | ✓ | ✓ | ✗ |
| `host.write`（增删改、凭据） | ✓ | ✗ | ✗ |
| `task.write`、`task.run` | ✓ | ✓ | ✗ |
| `audit.read` | ✓ | ✗ | ✗ |

默认拒绝。未登录与已登录无权限必须区分：401 vs 403。

### 6.5 审计

`observability.Auditor` 经批写队列写 `audit_events`。必须记录：登录成功/失败、登出、授权拒绝、插件启用/停用/调用、
主机增删改/测试连接/凭据替换（只记「已替换」）、下发创建/各阶段/完成/失败/重试/撤销、任务增删改/启停/手动执行/错过。
`details` 为 JSON，禁止写凭据与主密钥。

### 6.6 对外身份源

`identity.Provider` 接口（`Authenticate(ctx, credentials) (Subject, error)`）只定义与文档说明；本地账号为其唯一实现；
LDAP/SSO 在 DOC-13 记为未实现（引用 OQ-037）。

## 7. M3：主机与下发

### 7.1 数据表（v4）

```sql
CREATE TABLE hosts (
  host_id        TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  address        TEXT NOT NULL,
  port           INTEGER NOT NULL,
  username       TEXT NOT NULL,
  auth_method    TEXT NOT NULL CHECK (auth_method IN ('key','password')),
  host_key_fp    TEXT NOT NULL DEFAULT '',   -- SHA256 指纹，首次信任后锁存
  host_key_trusted INTEGER NOT NULL DEFAULT 0,
  status         TEXT NOT NULL DEFAULT 'unknown', -- unknown | reachable | unreachable | fingerprint_mismatch
  last_test_at   TEXT NOT NULL DEFAULT '',
  last_test_error TEXT NOT NULL DEFAULT '',
  created_by     TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE TABLE host_credentials (
  host_id     TEXT PRIMARY KEY REFERENCES hosts(host_id) ON DELETE CASCADE,
  ciphertext  BLOB NOT NULL,   -- AES-256-GCM(nonce||ciphertext)
  updated_at  TEXT NOT NULL
);
CREATE TABLE distributions (
  distribution_id TEXT PRIMARY KEY,
  host_id         TEXT NOT NULL,
  plugin_id       TEXT NOT NULL,
  plugin_version  TEXT NOT NULL,
  state           TEXT NOT NULL, -- queued|connecting|uploading|verifying|installing|installed|failed|interrupted|cancelled
  stage           TEXT NOT NULL DEFAULT '',
  error_category  TEXT NOT NULL DEFAULT '',
  error_message   TEXT NOT NULL DEFAULT '',
  remote_path     TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL,
  created_by      TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  started_at      TEXT NOT NULL DEFAULT '',
  finished_at     TEXT NOT NULL DEFAULT '',
  UNIQUE (host_id, plugin_id, plugin_version, idempotency_key)
);
CREATE INDEX idx_distributions_host ON distributions(host_id, created_at DESC);
```

`port` 无默认值，必填；`address` 只接受主机名/IP，不含协议前缀。

### 7.2 凭据加密

`internal/secret.Box`：AES-256-GCM，key 来自 `credential_key`（base64，必须 32 字节）；每次加密随机 12 字节 nonce，
输出 `nonce||ciphertext||tag`。密钥缺失 → `ErrKeyNotConfigured`，所有写/读凭据与连接操作显式失败（500 + 明确原因，不降级）。
读接口只返回 `credential_configured: true`，永不返回明文或密文。

### 7.3 SSH

- 使用 `golang.org/x/crypto/ssh`；`HostKeyCallback` 自实现：首次连接记录并信任（TOFU），此后指纹不一致
  返回 `ErrFingerprintMismatch` 并置主机 `status=fingerprint_mismatch`，**无忽略开关**。
- 禁止使用 `ssh.InsecureIgnoreHostKey`。
- 连接超时 `ssh_connect_timeout`；命令/传输超时 `ssh_upload_timeout`。
- 支持 `key`（私钥 + 可选 passphrase，均加密存储）与 `password` 两种认证。
- 测试连接：仅执行 `uname -s` 判定系统；非 Linux 返回 `unsupported_os`（OQ-024）；成功记录 `reachable` 与时间，失败记录分类原因（`unreachable|auth_failed|fingerprint_mismatch|unsupported_os|timeout`）。

### 7.4 下发

- 打包：把 `<plugin_dir>/<plugin-id>` 目录打成内存 tar.gz；计算 SHA-256。
- 上传：`ssh` 会话执行 `mkdir -p <root>/<plugin-id>/<version> && tar -xzf - -C <dir>`，stdin 写 tar.gz。
- 校验：执行 `sha256sum <dir>/plugin.yaml` 与本地摘要比对；不一致 → `verify_failed`，不发安装步骤
  （安装即解包到目标目录；解包失败按 install 失败处理，并尽力删除半装目录）。
- 状态机严格按 DOC-09 §6；每个阶段更新 `distributions` 并写审计。
- 并发：每主机一个串行队列（goroutine + channel），全局信号量 `distribution_max_concurrency`；同主机同插件同版本
  重复提交返回既有任务（幂等）。
- 撤销：仅允许 `queued|failed|installed` 撤销记录；`installed` 撤销会尝试删除远端目录并把状态置 `cancelled`，
  失败则保留 `installed` 并报错。
- 重启恢复：启动时把 `connecting|uploading|verifying|installing` 置 `interrupted`，允许显式重试。
- 明确边界：不启动、不调用远端插件；接口字段用 `installed`，响应附 `callable:false` 与说明。

### 7.5 HTTP（M3）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/hosts` | 列表（不含凭据） |
| POST | `/api/hosts` | 新增；body 含 `credential:{method, private_key?, passphrase?, password?}` |
| GET | `/api/hosts/{id}` | 详情（`credential_configured`） |
| PUT | `/api/hosts/{id}` | 编辑；`credential` 缺省表示不替换 |
| DELETE | `/api/hosts/{id}` | 删除；存在下发记录时需 `?force=true` |
| POST | `/api/hosts/{id}/test` | 测试连接 |
| POST | `/api/hosts/{id}/distributions` | `{plugin_id, plugin_version}` |
| GET | `/api/hosts/{id}/distributions` | 列表 |
| GET | `/api/distributions/{id}` | 详情（各阶段时间与错误） |
| POST | `/api/distributions/{id}/retry` | 重试（failed/interrupted） |
| DELETE | `/api/distributions/{id}` | 撤销 |

## 8. M4：定时任务

### 8.1 数据表（v5）

```sql
CREATE TABLE tasks (
  task_id        TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  description    TEXT NOT NULL DEFAULT '',
  plugin_id      TEXT NOT NULL,
  capability_id  TEXT NOT NULL,
  input_json     TEXT NOT NULL DEFAULT '{}',
  trigger_type   TEXT NOT NULL CHECK (trigger_type IN ('cron','interval')),
  cron_expr      TEXT NOT NULL DEFAULT '',
  interval_seconds INTEGER NOT NULL DEFAULT 0,
  timezone       TEXT NOT NULL,
  timeout_seconds INTEGER NOT NULL,
  retries        INTEGER NOT NULL,
  overlap_policy TEXT NOT NULL DEFAULT 'skip',   -- v1 固定 skip
  state          TEXT NOT NULL CHECK (state IN ('enabled','paused','unavailable')),
  next_run_at    TEXT NOT NULL DEFAULT '',
  last_run_at    TEXT NOT NULL DEFAULT '',
  claimed_at     TEXT NOT NULL DEFAULT '',
  claim_token    TEXT NOT NULL DEFAULT '',
  created_by     TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX idx_tasks_next_run ON tasks(state, next_run_at);
ALTER TABLE plugin_executions ADD COLUMN task_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_plugin_executions_task ON plugin_executions(task_id, started_at DESC);
```

（`plugin_executions.task_id` 若 v2 已建则可直接省略 `ALTER`，实现时以实际 v2 为准。）

### 8.2 cron

`internal/scheduler/cron.go`：5 段（分 时 日 月 周），支持 `*`、`*/n`、`a-b`、`a-b/n`、逗号列表、数字；
周 0-6（0=周日）；不支持秒、`@reboot`、`L/W/#`（返回 `unsupported_syntax`，OQ-031/OQ-042）。
`Next(t time.Time, loc *time.Location) (time.Time, error)` 从 `t+1min` 起逐分钟匹配，最多搜索 5 年。

`interval`：`interval_seconds ≥ task_min_interval`，`next = last + interval`。

### 8.3 调度循环

- ticker `task_scan_interval`；每次：
  1. `SELECT ... WHERE state='enabled' AND next_run_at <= now AND claimed_at=''`。
  2. 逐条在单写事务中认领：`UPDATE tasks SET claimed_at=now, claim_token=? WHERE task_id=? AND claimed_at='' AND next_run_at=?`；
     影响行数 0 表示已被认领，跳过。
  3. 认领后异步执行（受全局 `plugin_max_concurrency` 限制），执行走 `plugin.Service.Invoke(..., source="scheduled")`。
  4. 执行前计算 `next_run_at`（按触发规则从**计划时点**推进，而不是从完成时刻），写回并清空 `claimed_at`。
  5. 写 `plugin_executions`（`task_id`、`source=scheduled`）。
- 重叠：任务在途集合（内存 `map[taskID]bool` + `claimed_at` 非空）→ 本次触发跳过，写 `status=skipped` 执行记录。
- 错过：启动时对每个 enabled 任务比较 `next_run_at` 与 now；若已过期，写 `status=missed` 记录并把 `next_run_at` 推进到
  下一个未来时点（**不补跑**，OQ-032 默认）。同时 `GET /api/tasks` 返回 `missed_count`（近 7 天）。
- 崩溃恢复：启动时 `claimed_at` 非空且超过 `timeout+1min` → 写 `status=interrupted` 记录，清空认领，重算 `next_run_at`。
- 重试：仅 `timeout|dependency_unavailable` 可重试，最多 `retries` 次，间隔 5s 固定（OQ-042 基线）。
- 关闭：停止认领，等待在途至多 `shutdown_timeout`。

### 8.4 任务状态与插件联动

- 保存/执行时校验插件存在、`state=enabled`、能力已声明；不满足 → 保存返回 400；已存在任务在插件停用/卸载后
  扫描时置 `state=unavailable` 并停止触发（不删任务）。
- 任务参数禁止明文凭据：保存时用与 M1 相同的凭据键正则递归检查（含键名），命中拒绝 400。

### 8.5 HTTP（M4）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/tasks` | 列表含 `next_run_at`、`state`、`last_run_at` |
| POST | `/api/tasks` | 创建 |
| GET | `/api/tasks/{id}` | 详情 |
| PUT | `/api/tasks/{id}` | 编辑（重算 next_run_at） |
| DELETE | `/api/tasks/{id}` | 删除 |
| POST | `/api/tasks/{id}/enable` | 启用 |
| POST | `/api/tasks/{id}/pause` | 暂停 |
| POST | `/api/tasks/{id}/run` | 手动立即执行（source=scheduled 但 `trigger=manual`？→ 用 `manual` 写入 `source`，`task_id` 保留） |
| GET | `/api/tasks/{id}/executions` | 执行历史 |

## 9. M5：前端接真实接口

- 仍为单个 `prototype/index.html`（内联 CSS/JS），由 `web/embed.go` 内嵌；不引入框架、Node、构建链。
- 新增登录视图：`POST /api/login` 成功后进入控制台；401 时清空本地状态并显示登录页与原因；不做静默重试。
- 所有 `fetch` 统一封装：`credentials:'same-origin'`、`X-Ops-Request: 1`、非 2xx 时读取 `error.code/message` 并在界面提示。
- 页面：总览（KPI 取真实统计：插件总数/启用数、主机数/可达数、任务数/启用数、近 24h 执行数与失败数）、
  插件管理、主机管理、定时任务、执行记录、审计日志（admin）。
- 空状态与错误态必须显式；待定项显示「待定（OQ-xxx）」；不写死任何示例数据。
- 前端不展示凭据明文；编辑主机时凭据字段留空表示不替换。

## 10. 验证步骤（实现完成后执行）

1. `gofmt -l .`（无输出）、`go vet ./...`、`go test ./...`。
2. `.\scripts\build.ps1`；确认 `bin\operations-platform.exe`、`bin\demo-echo.exe` 生成。
3. 启动（示例，数据目录与监听地址按需改）：
   ```
   $env:OPS_PLATFORM_DATA_DIR="E:\tmp\ops-platform"
   $env:OPS_PLATFORM_ADMIN_PASSWORD="ChangeMe-123456"
   .\bin\operations-platform.exe -listen 127.0.0.1:8080 -plugin-dir E:\tmp\ops-platform\plugins
   ```
   把 `examples/plugins/demo-echo` 复制到插件目录后 `POST /api/plugins/scan`。
4. M1：`GET /api/plugins` 出现 `demo-echo`（disabled）→ `POST .../enable` → `POST .../demo-echo.echo/invoke`
   `{"input":{"text":"hello"}}` 返回 `status:"ok"` → `GET /api/executions?plugin_id=demo-echo` 有记录；
   `broken-demo` 不出现在列表且 `warnings` 给出字段路径。
5. M2：未带 Cookie 调 `/api/plugins` → 401；登录 → 200；viewer 调 invoke → 403；错误口令连续 5 次 → 429；
   `GET /api/audit`（admin）可见登录与拒绝事件。
6. M3：新增主机（本机 sshd）→ 测试连接成功 → 下发 demo-echo → 状态 `installed`、响应 `callable:false`；
   `GET /api/hosts/{id}` 无凭据明文；指纹变更后测试连接被阻断。
7. M4：创建固定间隔 1 分钟任务与 cron `*/1 * * * *` 任务各一，观察两次执行记录；重启后任务仍在、
   不重复触发；暂停后不再触发。
8. M5：浏览器打开 `/`，逐页操作，确认无 mock 数据、错误可见、退出登录后受保护接口不再返回数据。

## 11. 需要用户确认的点

1. 接受新增依赖（yaml.v3、jsonschema/v5、x/crypto）与 ADR-0016/0017 的 `proposed` 状态。
2. 接受 OQ-039~OQ-042 的「可运行基线」取值（尤其远端安装根目录 `/opt/ops-platform/plugins`、
   PBKDF2 210000 次、默认端口无默认值、M3 仅 Linux）。
3. M2 首启管理员口令策略：环境变量优先，否则生成到 `bootstrap-admin.txt`（0600）——是否接受该回退方式。
4. M3 允许在未确认 OQ-028（分组授权/二次确认）的情况下，先以「角色 + 全量主机 + 审计」实现；
   若要求二次确认，需在实现前给出交互形式。
5. M4 任务视图：`state` 增加 `unavailable`（插件不可用）是否可接受。

