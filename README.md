# Operations Platform（运维平台底座）

面向运维人员的**平台底座（Foundation）**。底座自身只提供基础设施能力（插件管理、统一调用、配置、审计、可观测性等），
所有具体运维业务功能由**插件**提供。

- 语言：Go
- 交付形态：前端静态资源内嵌，前后端编译为**单个二进制文件**
- 插件形态：命令式插件（一次性执行，stdio 通信）与常驻服务式插件（常驻进程，远程调用）
- 开发方式：规格驱动开发（Spec-Driven / Docs-as-Code），**规格文档是唯一事实来源**

## 当前状态

**M0 基础设施骨架、M1 插件运行时（stdio）、M2 本地身份与会话、M3 主机 SSH 下发已落地**：Go 单二进制可构建、可启动、可打开内嵌界面。插件运行时仅覆盖**命令式插件**；**M6 常驻插件运行时尚未实现**，定时任务（M4）亦未实现。

M0 的配置键名、环境变量、HTTP 路径与库文件名属于可运行基线，待 [OQ-036](docs/07-open-questions.md) 确认，完整说明见 [DOC-11](docs/11-implementation-baseline-m0.md)。
人读规格均为 `draft`；ADR 中插件发现与清单、stdio、远程调用、鉴权、存储、凭据加密、常驻插件托管、错误类别、契约来源、能力执行、远端证书体系、插件注册与实例模型十二条已确认（`accepted`），其余等待评审与决策。
已确认：单节点部署、嵌入式 SQLite（纯 Go、免 cgo）、数据目录由配置文件指定、插件外部凭据 AES-256-GCM 加密。
已定契约（P0 六项）：清单为包根目录 `plugin.yaml`（YAML，内置 JSON Schema 校验，标识小写短横线，v1 禁止插件间依赖）；命令式插件走 NDJSON stdio（stdin 关闭即结束，stdout 只放结果）；常驻插件走 gRPC 且只暴露通用 `Invoke`；发现为目录扫描 + 注册表持久化，安装后默认禁用、热加载默认关闭；鉴权为短期凭据 + 能力级授权 + 默认拒绝，权限按「资源类别 + 动作」声明；契约版本 `MAJOR.MINOR`，插件声明支持范围。详见 [ADR-0003](docs/adr/0003-plugin-discovery-registration.md)~[ADR-0006](docs/adr/0006-plugin-authz.md) 与 [plugin-spec/](plugin-spec/README.md)。
已定契约（P0 后补齐，契约 **1.1**）：常驻插件本机形态由底座 spawn 并托管（禁 daemonize/pid 文件），启动注入固定 `--ops-*` 参数，凭据经一次性文件（读后即删）+ `context.credential` 续期；socket 位于 `<data-dir>/run/plugins/<plugin-id>/`（`0600`）；新增 `not_found` / `validation_failed` / `precondition_failed` 错误类别，退出码只适用于命令式插件；契约唯一来源 `sdk/plugin/v1/plugin.proto` 且生成物入库；所有能力一律串行、禁 `setup`/`version` 能力、高危操作必须确认。详见 [ADR-0018](docs/adr/0018-daemon-process-custody-and-injection.md)~[ADR-0021](docs/adr/0021-capability-execution-and-destructive-confirmation.md)。
已定契约（契约 **1.2**，2026-11-16）：远端常驻插件由目标机 **systemd** 托管（底座下发 unit 并启停），直接监听 **TCP + 双向 TLS**；证书由底座内置 CA 在**下发安装时**签发并配置到目标机，私钥 `0600`、CA 私钥永不下发；SSH 只用于下发安装，不承担调用传输；本机仍为底座 spawn + UDS；清单新增 `runtime.listen.port`（端口默认值，可按主机覆盖）与 `config[]`（实例配置声明）。详见 [ADR-0005](docs/adr/0005-daemon-rpc-transport.md)、[ADR-0018](docs/adr/0018-daemon-process-custody-and-injection.md)、[ADR-0022](docs/adr/0022-remote-plugin-pki-and-mtls-certificates.md)。
已定模型：插件**被注册**而非自报，类型与实例分离，静态事实/运行状态/自报三层，配置作用域落到实例，调用与授权判定落到实例（类型级仅作模板）；远端实例分「已安装 / 已启动 / 可调用」三态。详见 [ADR-0023](docs/adr/0023-plugin-registry-instances-and-configuration.md) 与 [DOC-17](docs/17-plugin-registry-and-configuration.md)。
v1 范围已包含：Web UI 配置主机（SSH 密钥或账号密码）并将插件包下发安装到目标主机（CAP-13 / DOC-09 / ADR-0014 `proposed`）；远端运行与调用方式已定（OQ-010、OQ-027 `decided`），实现待里程碑补齐（证书与 systemd 相关数值见 OQ-047、OQ-048）。
v1 范围已包含：定时任务（CAP-14 / DOC-10 / ADR-0015 `proposed`），执行体限定为「调用某个插件的某个能力」；执行目标应落到插件实例，当前默认本机实例，面向远端主机的默认值待定（OQ-030）。

## 快速开始（M0）

要求 Go 1.26+。构建脚本内部强制 `CGO_ENABLED=0`：

```powershell
.\scripts\build.ps1
.\bin\operations-platform.exe -data-dir C:\ops-platform-data -listen 127.0.0.1:8080
```

```sh
./scripts/build.sh
./bin/operations-platform -data-dir /var/lib/operations-platform -listen 127.0.0.1:8080
```

启动后访问 `http://127.0.0.1:8080/`，健康检查 `GET /healthz`，版本信息 `GET /api/version`。
跨平台构建命令、配置优先级与启动自检规则见 [DOC-11](docs/11-implementation-baseline-m0.md)。

## 从哪里开始

| 角色 | 入口 |
| --- | --- |
| 人（评审 / 开发） | [docs/README.md](docs/README.md) —— 文档索引与阅读顺序 |
| AI 编码代理 | [AGENTS.md](AGENTS.md) 与 [.ai/index.md](.ai/index.md) |
| 插件开发者 | [plugin-spec/README.md](plugin-spec/README.md) —— 可直接整目录复制的插件规范模板 |

## 目录约定

```text
Operations-Platform/
├── AGENTS.md              # AI 编码代理的项目规则与导航
├── README.md              # 本文件：项目门面
├── go.mod                 # Go 模块（module operations-platform）
├── cmd/ops-platform/      # 单二进制入口：装配、信号处理、优雅关闭
├── internal/              # 底座内部实现（config / store / api / observability）
├── web/                   # 内嵌前端资源包（embed）与同步后的 dist/
├── scripts/               # 单二进制构建脚本（PowerShell / sh）
├── prototype/             # 原型页面（页面事实来源，构建时同步到 web/dist）
├── .ai/                   # 机器可读上下文：索引、约定、机器可读清单
├── docs/                  # 人读规格：范围、术语、能力、插件模型、生命周期、扩展点、NFR、存储、主机下发、定时任务、注册与配置、M0 基线
│   ├── adr/               # 架构决策记录（ADR-0003/0004/0005/0006/0007/0013/0018~0023 已 accepted，其余 proposed）
│   └── ...
└── plugin-spec/           # 插件开发规范目录模板（整目录复制到新插件仓库）
```

## 硬性约束（摘要）

1. 规格先行：功能需求、接口契约、验收标准先写入 `docs/`，再写代码。
2. 文档即代码：代码变更若影响规格，必须**同批次**更新对应 Markdown。
3. 不编造：未确定的字段、协议、配置项一律写「待定」并登记到 [docs/07-open-questions.md](docs/07-open-questions.md)。
4. 远期想法标记 `idea`，不得混入正式规格。
5. 架构取舍必须走 [ADR](docs/adr/README.md)，不得在代码中默默定死。

## 文档状态图例

`draft` 草稿 · `proposed` 待确认（ADR 用） · `accepted` 已确认 · `idea` 远期想法 · `superseded` 已废弃
