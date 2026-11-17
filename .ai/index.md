# .ai/index.md —— 机器可读文档索引

用途：给 AI 代理一份**低 token 成本**的仓库上下文入口。人读正文在 `docs/`，本文件只做导航与元数据。
**本文件是派生索引，不是事实来源**；与 `docs/**` 冲突时以正文为准，并立即修正本文件。

## 1. 元数据字段约定

每份文档在 `docs/README.md` 中登记：`id`（稳定编号）、`path`、`owner`（默认 `unassigned`）、`status`、`depends_on`、`audience`。

状态取值：`draft` | `proposed` | `accepted` | `idea` | `superseded`。

## 2. 文档清单

| id | path | status | 摘要 |
| --- | --- | --- | --- |
| DOC-00 | docs/00-overview.md | draft | 项目定位、范围、非目标、成功标准 |
| DOC-01 | docs/01-glossary.md | draft | 领域概念与术语唯一释义 |
| DOC-02 | docs/02-foundation-capabilities.md | draft | 底座能力清单与边界 |
| DOC-03 | docs/03-plugin-model.md | draft | 插件模型、两种形态契约、能力声明 |
| DOC-04 | docs/04-plugin-lifecycle.md | draft | 插件状态机与生命周期操作 |
| DOC-05 | docs/05-extension-points-and-compatibility.md | draft | 扩展点稳定性等级与版本兼容策略 |
| DOC-06 | docs/06-non-functional-requirements.md | draft | 打包、部署、日志、配置、安全、性能 |
| DOC-07 | docs/07-open-questions.md | draft | 开放问题与待决策清单（按优先级） |
| DOC-08 | docs/08-storage-and-data-directory.md | draft | 存储分区、数据目录、权限、写入/备份/迁移规则 |
| DOC-09 | docs/09-host-management-and-plugin-distribution.md | draft | 主机清单、SSH 认证、下发安装、回滚与审计 |
| DOC-10 | docs/10-scheduled-tasks.md | draft | 定时任务定义、触发、执行、错过执行与可靠性 |
| DOC-11 | docs/11-implementation-baseline-m0.md | draft | M0 基础设施骨架实现基线（配置键、数据目录、存储、HTTP、构建） |
| DOC-12 | docs/12-m1-plugin-runtime.md | draft | M1 插件运行时实现基线（清单校验、发现注册、stdio 调用、执行记录、授权与接口） |
| DOC-13 | docs/13-m2-identity-and-authz.md | draft | M2 身份实现基线（口令派生、会话与 CSRF、角色矩阵、审计） |
| DOC-14 | docs/14-host-management-m3.md | draft | M3 主机实现基线（凭据加密、TOFU 指纹、下发状态机与接口） |
| DOC-15 | docs/15-m4-scheduled-tasks.md | draft | M4 定时任务实现基线（cron 语义、认领调度、错过执行与接口） |
| DOC-16 | docs/16-web-ui-m5.md | draft | M5 Web 控制台实现基线（页面能力、总览口径、执行摘要与权限可见性） |
| DOC-17 | docs/17-plugin-registry-and-configuration.md | draft | 插件注册方向、类型/实例、三层状态、逻辑存储、实例配置、使用路径与界面 |
| DOC-18 | docs/18-m6-remote-plugins-and-instances.md | draft | M6 实现基线（内置 CA、mTLS、schema v7、三态与漂移、下发扩展、实例授权） |
| DOC-19 | docs/19-usage-and-configuration.md | draft | 使用与配置手册（部署启动、配置全集与默认值、首次使用、三态与下发、FAQ） |
| DOC-ADR | docs/adr/README.md | draft | ADR 索引与模板（ADR-0003/0004/0005/0006/0007/0013/0018/0019/0020/0021/0022/0023 为 accepted） |
| PS | plugin-spec/README.md | draft | 插件开发规范模板入口（可整目录复制） |
| PS-11 | plugin-spec/11-instance-and-config.md | draft | 实例与配置（一个包多实例、能力只声明、`config[]`、自报边界） |
| PS-12 | plugin-spec/12-plugin-ui.md | proposed | **草案，未生效**：一档声明式界面（v1 方向）；二档自带前端包挂起（OQ-011） |
| AGENTS | AGENTS.md | draft | AI 代理工作规则 |

## 3. 稳定编号

- 文档：`DOC-NN`
- 架构决策：`ADR-NNNN`
- 开放问题：`OQ-NNN`
- 扩展点：`EP-NN`
- 底座能力：`CAP-NN`
- 插件规范条目：`PS-NN`

编号一旦分配**不复用**；废弃条目保留编号并标 `superseded`。

## 4. 关键约束速查（写代码/写文档前必看）

1. 底座不含具体运维业务功能（主机资产/CMDB、业务发布部署、监控采集、工单等一律由插件提供）。**例外**：供插件 SSH 下发使用的轻量主机清单属于 CAP-13（DOC-09）。
2. 命令式插件 = 一次性进程 + stdio **NDJSON**（ADR-0004）；常驻插件 = 常驻进程 + **gRPC，只暴露通用 Invoke**（ADR-0005）。常驻插件有**两种托管**：本机由**底座 spawn**（`--ops-*` 注入 + 一次性凭据文件，读后即删），远端由**目标机 systemd**（底座下发 unit、`enable/start` 并探测）。两形态都禁止 daemonize / pid 文件；同一主机 + 同一插件只能一种托管（ADR-0018）。
3. 清单固定为包根目录 `plugin.yaml`（YAML），走**内置 JSON Schema 校验**；插件标识小写短横线，能力标识强制 `<插件标识>.<能力名>`；v1 禁止插件间依赖（ADR-0003）。权限以「资源类别 + 动作」声明，**未声明即无权**（ADR-0006）。
4. 契约版本 `MAJOR.MINOR`，插件声明**支持范围**（`>=1.0 <2.0`），底座同时支持同一 MAJOR 下多个 MINOR（当前 `1.0`、`1.1` 与 `1.2`）；兼容判定在安装/启用时完成（ADR-0012）。
5. 存储：嵌入式 SQLite（纯 Go、免 cgo），**单节点**、单写者；插件不得直连底座库（ADR-0007 / DOC-08）。
6. 插件外部凭据：AES-256-GCM 密文存库，主密钥不入库、不写日志；不防主机被完全控制（ADR-0013）。
7. 数据目录由配置文件指定，必须绝对路径、必须本地文件系统；默认回退待定（OQ-022）。
8. 远端调用：**本机 UDS** 与**远端 TCP + 双向 TLS** 两种；底座内置 CA 在**下发安装时**签发服务端证书并配置 CA 证书，私钥 `0600`、CA 私钥**永不下发**；明文 TCP 与单向 TLS 一律禁止，无「跳过校验」开关（ADR-0005、ADR-0022）。端口默认值来自清单 `runtime.listen.port`，可按主机覆盖；证书不绑定端口。
9. 主机凭据（SSH 私钥/密码）加密落库、保存后不可回显不可导出；**SSH 只用于下发安装、启停与探测，不承担业务调用传输**；底座持有生产机凭据即高权限跳板，授权模型待定（OQ-028）。
10. 定时任务执行体 = 调用插件能力（经 CAP-02），默认本机；不做编排/DAG、分布式调度、任意 shell；重启不丢、不重复触发、不静默漏跑。
11. M0 已落地基础设施骨架（配置、数据目录、SQLite、内嵌 UI、健康/版本、日志、构建）；其中配置键名、HTTP 路径、库文件名等属可运行基线，待 OQ-036 确认（DOC-11）。
12. M1 已落地插件运行时最小闭环（YAML 配置与清单、发现注册、NDJSON stdio 调用、执行记录批写、能力级授权与 JSON 接口）；默认值登记为 OQ-039（DOC-12）。配置格式自 M1 起为 YAML（ADR-0016 proposed）。
13. M2 已落地本地账号与会话（PBKDF2-HMAC-SHA256、Cookie + CSRF、三级角色矩阵、审计批写）；参数登记为 OQ-040（DOC-13）。未登录访问 `/api/*` 一律 401。
14. M3 已落地主机与下发（AES-256-GCM 凭据、TOFU 指纹、tar.gz + sha256、状态机与批写审计）；默认值登记为 OQ-041（DOC-14）。M6 已补齐证书签发、systemd unit、启动与 TCP+mTLS 调用（ADR-0018 / ADR-0022 / DOC-18）。
15. M4 已落地定时任务（5 段 cron 分钟级、固定间隔、条件更新认领、重叠 skip、错过不补跑只留痕、执行记录复用 `plugin_executions.task_id`）；默认值登记为 OQ-042（DOC-15）。M6 起任务目标为实例（含远端实例），仍不做 shell / DAG / 分布式调度。
16. M5 已把控制台接到真实接口（原生单文件、Go embed、Cookie 会话 + `X-Ops-Request` CSRF、空/错状态、凭据不回显）；新增 `GET /api/overview` 与执行记录 `input_summary`/`output_summary`（各 ≤2048B，v6 迁移），口径登记 OQ-043、OQ-046（DOC-16）。未做用户管理页面。
17. 契约 1.1（历史增量）：新增错误类别 `not_found` / `validation_failed` / `precondition_failed`、`InvocationContext.credential`、清单 `risk_level`；底座遇未知错误类别归一为 `internal_error` 并保留原码；使用新类别的插件 `contract` 下限须 ≥ `1.1`（ADR-0019）。
18. 能力调度**一律串行**（每插件单队列，`concurrency` 仅为承受能力声明）；禁止 `setup` / `version` 能力；禁止依赖 TTY（含交互式初始化）；`delete` / `restore` / `gc` 必须携带确认字段，由插件在产生副作用前校验（ADR-0021）。
19. 契约唯一来源为 `sdk/plugin/v1/plugin.proto`，生成物 `plugin.pb.go` / `plugin_grpc.pb.go` 入库；工具链经 `scripts/gen-proto.*` 锁定（protoc 36.2.0 / protoc-gen-go v1.36.6 / protoc-gen-go-grpc v1.5.1，生成头记内部版本 v7.36.2 属预期）；契约常量在 `sdk/plugin/v1/contract.go`；插件只依赖 SDK，不 import 底座内部包（ADR-0020、ADR-0024）。
20. 退出码只对**命令式插件**有意义（0~8 固定）；常驻插件无退出码语义，异常退出按未就绪 / 在途崩溃折算错误类别（ADR-0019）。
21. M6 已落地内置 CA 与 mTLS 远端常驻插件、类型/实例模型、三态与漂移、实例级授权与下发扩展（schema v7）；实现基线见 DOC-18，未定数值为可配置项并登记 OQ-047～OQ-051。
22. 契约当前版本 **1.2**（MINOR）：在本机 UDS 之外新增远端 TCP + 双向 TLS、清单 `runtime.listen.port` 与 `config[]`、远端配置文件的 `deployment`/`listen`/`tls`/`data_dir` 段；本机条款不变（ADR-0005、ADR-0018、ADR-0022）。
23. 插件**被注册**而非自报：事实来源是底座掌握的包、清单、下发记录与证书绑定；自报信息仅供参考，不得作为健康或能力的事实依据。类型与实例分离，同一类型多主机安装产生多实例（ADR-0023 / DOC-17）。
24. 远端实例状态分「已安装 / 已启动 / 可调用」三个独立状态；配置作用域落到**实例**，调用、定时任务、批量操作与授权判定都落到实例（类型级仅作模板）。能力清单以类型为键独立存储，便于「按能力查实例」。
25. 所有未定项见 [docs/07-open-questions.md](../docs/07-open-questions.md)（当前 51 项）。
26. 首次部署与日常操作见 [docs/19-usage-and-configuration.md](../docs/19-usage-and-configuration.md)（DOC-19）：配置优先级、`credential_key`、主机与下发、`callable=false` 排查、CSRF 与会话问题。
27. 插件界面为 **proposed**（ADR-0025）：v1 只做**声明式界面**——清单声明入口、配置表单与结果展示，底座统一渲染，**不加载任何插件前端代码**；二档「插件自带前端包」（沙箱 iframe + Host API）**挂起为 idea，不进入 v1**。一档控件与展示边界待 OQ-011；`plugin-spec/12` 为草案，未生效，不得据此实现。

## 4.1 插件模板文件

`plugin-spec/` 可整目录复制，内部文件：`README.md`、`AGENTS.md`、`00-contract-checklist.md`、
`01-manifest.md`、`02-stdio-protocol.md`、`03-remote-protocol.md`、`04-io-conventions.md`、
`05-errors-and-exit-codes.md`、`06-logging-observability.md`、`07-versioning-compatibility.md`、
`08-directory-layout.md`、`09-example-plugin-spec.md`、`10-faq.md`、`11-instance-and-config.md`、`12-plugin-ui.md`（草案，未生效）、`manifest.template.yaml`。

写代码前必读：`01`（声明什么）、`02`/`03`（怎么通信）、`05`（怎么失败）、`11`（实例与配置）。
遇到规范未覆盖的问题先查 `10-faq.md`，仍未覆盖必须停下提问并按 `plugin-spec/AGENTS.md` §5 回流到正文，不得在单个插件里私定。

## 5. 机器可读清单

| 文件 | 内容 |
| --- | --- |
| [glossary.yaml](glossary.yaml) | 术语 → 定义 / 禁用别名 / 首次出现文档 |
| [open-questions.yaml](open-questions.yaml) | 开放问题 → 优先级 / 阻塞对象 / 关联 ADR |
| [conventions.md](conventions.md) | 文档命名、状态流转、写作规则、同步义务 |

## 6. M0–M6 代码入口

| 路径 | 职责 |
| --- | --- |
| `cmd/ops-platform/main.go` | 单二进制装配、启动自检、插件/身份/主机/任务服务与批写队列装配、信号处理、优雅关闭 |
| `internal/config/` | 配置加载与优先级合并（命令行 > 环境变量 > YAML 文件 > 默认值） |
| `internal/store/` | 数据目录、SQLite/WAL、schema 迁移（v1~v7）与备份、批量写队列 |
| `internal/api/` | 健康/版本、认证会话、插件类型/实例、执行记录、主机与下发、定时任务、总览、审计、内嵌 UI 托管、访问日志 |
| `internal/identity/` | 本地账号、口令派生、会话与 CSRF、角色矩阵、审计写入 |
| `internal/secret/` | AES-256-GCM 凭据加解密（唯一入口，缺钥显式失败） |
| `internal/pki/` | 内置 CA、服务端/客户端证书签发、序列号吊销与复用（ADR-0022） |
| `internal/host/` | 主机清单、SSH 连接与 TOFU 指纹、加密凭据、tar.gz 下发、证书/配置/unit 写入、启停三态、撤销回滚 |
| `internal/scheduler/` | 5 段 cron、任务存储、认领调度、错过/重叠/崩溃恢复处理，任务目标为实例 |
| `internal/plugin/manifest/` | `plugin.yaml` 解析、内置 JSON Schema 与语义校验、契约版本区间 |
| `internal/plugin/instances/` | 类型、能力、实例、实例配置、探测记录、证书绑定的存储与状态机 |
| `internal/plugin/remote/` | 远端 TCP + mTLS 客户端、阶段探测、错误分类与降级 |
| `internal/plugin/discovery/` | 插件目录扫描、冲突与拒绝报告 |
| `internal/plugin/registry/` | SQLite 注册表与执行记录读写 |
| `internal/plugin/runtime/` | NDJSON stdio 调用、超时/体积上限、退出码映射 |
| `internal/plugin/authz/` | 能力级授权接口与内置管理员主体（M2 接会话） |
| `internal/plugin/` | `Service`：扫描、列表、启用停用、调用编排、执行记录入队 |
| `internal/observability/` | slog JSON 日志与级别解析 |
| `sdk/plugin/v1/` | 契约唯一来源：plugin.proto + 入库生成物 + 契约常量 contract.go（ADR-0020/0024） |
| `web/embed.go` + `web/dist/index.html` | 前端资源内嵌（dist 由 prototype 同步） |
| `examples/plugins/demo-echo/` | 可运行示例插件（两个能力）与反例说明 |
| `examples/plugins/broken-demo/` | 故意违反契约的插件，用于验证拒绝路径 |
| `examples/starter/` | 插件开发启动包：command（命令式）与 daemon（远端常驻）可整目录复制骨架，各带本地自测（ADR-0024） |
| `scripts/build.ps1` / `scripts/build.sh` | 单二进制与示例插件构建，强制 `CGO_ENABLED=0` |
| `scripts/gen-proto.ps1` / `gen-proto.sh` | 锁定工具链重生成 sdk/plugin/v1 代码；版本不符即失败（ADR-0024） |

配置文件键名、HTTP 路径、库文件名等可运行基线见 [docs/11-implementation-baseline-m0.md](../docs/11-implementation-baseline-m0.md)（OQ-036）；
M1 插件运行时配置键、默认值与接口见 [docs/12-m1-plugin-runtime.md](../docs/12-m1-plugin-runtime.md)（OQ-039）。
