# PS-01 元数据与能力声明

> 对应底座规格：`docs/03-plugin-model.md` §3-§4（纯文本引用，便于本目录独立复制）｜ 依据：底座 ADR-0003、ADR-0021、ADR-0023（accepted）、OQ-005（decided）｜ 契约版本：`1.2`

## 1. 清单文件

| 项 | 约定 |
| --- | --- |
| 位置 | 插件包根目录 |
| 文件名 | `plugin.yaml`（固定） |
| 格式 | YAML |
| 编码 | UTF-8，无 BOM |
| 数量 | 一个插件包**只能有一个**清单，只能描述**一个**插件 |
| 校验 | 底座内置 JSON Schema 校验：解析 YAML → 按 schema 校验结构 → 再做语义校验 |
| 敏感信息 | **禁止**出现密钥、口令、令牌 |
| 未知字段 | 底座忽略、不报错（向前兼容）；插件不得依赖底座忽略未知字段这一行为传递配置 |

## 2. 字段总表

「必需」列：`是` = 缺失即校验失败；`否` = 可省略，省略即取默认语义。

| # | 字段 | 类型 | 必需 | 说明 / 取值 |
| --- | --- | --- | --- | --- |
| 1 | `identity.id` | string | 是 | 插件唯一标识；**小写短横线**，正则 `^[a-z][a-z0-9-]*$`；全局唯一、不得复用 |
| 2 | `identity.display_name` | string | 是 | 人类可读名称，建议 ≤ 64 字符 |
| 3 | `identity.description` | string | 是 | 用途与边界，须说明「做什么」与「不做什么」 |
| 4 | `version.plugin` | string | 是 | 插件自身版本，`MAJOR.MINOR.PATCH` |
| 5 | `version.contract` | string | 是 | 支持的契约版本**范围**，空格分隔约束，操作符 `>=`、`<`、`=` |
| 6 | `form` | string | 是 | `command`（命令式）或 `daemon`（常驻），二选一 |
| 7 | `entry.executable` | string | `command` 时必需 | 包内可执行文件相对路径，不得越出包根目录 |
| 8 | `entry.launch` | string | `daemon` 时必需 | 包内可执行文件相对路径 |
| 9 | `entry.socket` | string | 否 | **本机专用**：常驻插件期望的 socket 文件名；最终路径由底座生成，插件不得硬编码绝对路径。本机 daemon 托管尚未实现（OQ-044） |
| 10 | `capabilities[].id` | string | 是 | `<identity.id>.<能力名>`，两段均为 `^[a-z][a-z0-9-]*$` |
| 11 | `capabilities[].description` | string | 是 | 做什么、不做什么、边界 |
| 12 | `capabilities[].input_schema_ref` | string | 是 | 包内 JSON Schema 文件相对路径；无输入时指向描述空对象的 schema |
| 13 | `capabilities[].output_schema_ref` | string | 是 | 包内 JSON Schema 文件相对路径 |
| 14 | `capabilities[].idempotent` | boolean | 是 | 相同输入重复调用是否等价 |
| 15 | `capabilities[].long_running` | boolean | 是 | 是否可能长时间执行 |
| 16 | `capabilities[].suggested_timeout_ms` | integer | 否 | 建议超时（毫秒）；底座可覆盖 |
| 17 | `capabilities[].supports_cancel` | boolean | 是 | 是否响应取消 |
| 18 | `capabilities[].concurrency` | string | 是 | `parallel` 或 `serial`；只声明插件自身承受能力，**底座一律串行调度**（§3.3） |
| 19 | `capabilities[].sensitive` | boolean | 是 | 输入/输出是否可能含敏感信息 |
| 20 | `capabilities[].risk_level` | string | 否 | `normal`（默认）或 `destructive`；见 §3.5，与 `sensitive` 正交 |
| 21 | `permissions[].category` | string | 否 | `config` / `data_dir` / `network` / `filesystem` / `credential` / `invoke_plugin`；无权限需求时整个数组可省略 |
| 22 | `permissions[].actions` | string[] | 是（有 `category` 时） | `read` / `write` / `connect` / `use` / `call`；须与类别语义匹配 |
| 23 | `permissions[].scope` | string | 否 | 进一步收窄范围（路径/域名）；是否被强制校验待定（OQ-037） |
| 24 | `runtime.max_result_bytes` | integer | 否 | 插件能产出的最大结果体积；**底座侧上限数值待定（OQ-017）** |
| 25 | `runtime.resource_expectation` | string | 否 | CPU/内存的定性描述，不参与调度判定 |
| 26 | `runtime.listen.port` | integer | 否 | **远端部署默认端口**；出现即表示支持远端部署；下发时可按主机覆盖；范围与默认值待 OQ-048。**注意：本机 daemon 托管尚未实现（OQ-044），该字段只表示可被远端部署** |
| 27 | `config[].key` | string | 否 | 配置键，`^[a-z][a-z0-9_]*$`，类型内唯一 |
| 28 | `config[].type` | string | 是（有 `config` 时） | `string` / `number` / `integer` / `boolean` / `enum` |
| 29 | `config[].required` | boolean | 否 | 是否必填，缺省 `false` |
| 30 | `config[].secret` | boolean | 否 | 是否敏感，缺省 `false`；`true` 时不得有 `default`，不得出现在日志/错误详情 |
| 31 | `config[].default` | 依 `type` | 否 | 默认值；`secret: true` 时禁止 |
| 32 | `config[].enum` | string[] | 否 | `type: enum` 时的候选值 |
| 33 | `config[].description` | string | 否 | 面向用户的说明 |
| 34 | `metadata` | object | 否 | `author`、`license`、`homepage`、`docs`；不参与运行决策 |

**禁止字段**：`dependencies`（插件间依赖，v1 禁止）、任何凭据/密钥类字段。

## 3. 能力声明的语义规则

1. 一个能力对应一个明确动作，禁止「万能能力」（如单一 `execute` 承载全部语义）。
2. 输入/输出结构必须是可被 JSON Schema 校验的对象定义，包含类型、必填性、取值范围与至少一个合法示例。
3. 能力标识一经发布不得改变语义；语义变更必须新增能力或提升契约 MAJOR。
4. `sensitive: true` 时，底座在日志与审计中只记录摘要或存在性，不回显原值。

### 3.1 能力命名禁令

| 禁止 | 原因 | 替代 |
| --- | --- | --- |
| 能力名末段为 `setup` | 初始化不应是「能力」，更不能依赖交互输入 | 拆成幂等的普通能力，或由 `--ops-config` 完成 |
| 能力名末段为 `version` | 与 `GetVersion`（常驻）/ 清单 `version.plugin` 重复 | 由底座探测接口或清单承担 |

底座在语义校验阶段拒绝上述命名，错误定位到 `capabilities[i].id`。

### 3.2 禁止交互

- 不得要求 TTY、不得从 stdin 读取额外输入（命令式插件 stdin 只有一行请求）。
- 初始化与所有能力都必须在无交互环境下可重复执行；需要确认的操作按 §3.5 用显式字段表达，而不是等待输入。

### 3.3 串行执行

**底座为每个插件维护单一执行队列：同一插件的任意两个调用不并发（跨能力亦串行）。**

- `concurrency` 只表达「插件自身是否线程安全」；声明 `parallel` **不会**获得并发调度。
- 插件不得假设会同时收到多个调用，也不得依赖调用顺序作为业务前提。
- 不同插件之间可并发，受底座全局并发上限约束。

### 3.4 高危操作标记

- 能力名末段为 `delete` / `restore` / `gc` 时，`risk_level` 必须为 `destructive`；缺失或为 `normal` → 校验失败。
- `sensitive`（数据敏感性）与 `risk_level`（操作后果）正交，两者可同时为真。

### 3.5 高危操作确认（契约强制）

| 能力 | 必填确认字段 | 校验规则 | 违反时错误类别 |
| --- | --- | --- | --- |
| `delete` | `confirm_partition` | 必须**严格等于**同请求中的目标分区标识（区分大小写、不做模糊匹配） | 缺失 → `input_invalid`；不相等 → `validation_failed` |
| `restore` | `confirm` + 目标字段（`target` 或 `partition`） | `confirm` 必须为 `true` | 目标缺失 → `input_invalid`；`confirm` 缺失或 `false` → `precondition_failed` |
| `gc` | `confirm` + 目标字段（`target` 或 `partition`） | 同上 | 同上 |

约束：

- 校验必须在**任何副作用之前**执行，且由**插件**执行。
- 底座只做两件事：静态校验（能力名与 `risk_level` 一致、schema `required` 含约定字段）、调用前审计（确认字段是否出现）；底座**不**校验确认值是否等于目标。
- API 直调与定时任务同样必须携带确认字段，不得依赖 UI 二次确认兜底。
- 缺失确认字段不得由底座或 SDK 自动填充默认值。

### 3.6 端口与远端可用性

- `form: daemon` 且**未**声明 `runtime.listen.port` → 只能本机部署；底座不得把它下发为远端实例。
- 声明默认端口只表示「可远端部署」，不代表端口已放通；防火墙与端口可达由用户负责。
- **该字段只表示可被远端部署**：当前远端部署**只能经底座下发**（SSH 上传安装包、生成配置/证书/systemd unit
  并 `systemctl enable --now`），不得手工在目标机部署或注册 unit；同时**本机 daemon 托管尚未实现**（OQ-044），
  本机实例请使用命令式形态。
- 实际生效端口 = 主机级覆盖值（若有）或默认端口；以底座按主机生成的配置文件为准。
- 证书不绑定端口：改端口不触发重签（ADR-0022）。
- 同一主机上多个远端插件端口冲突 → 底座在下发校验阶段拒绝。

## 4. 权限声明

| 语义类别 | 可用动作 | 说明 |
| --- | --- | --- |
| `config` | `read` | 读取自身配置，不可读其他插件配置 |
| `data_dir` | `read` / `write` | 读写自身数据目录 |
| `network` | `connect` | 访问外部服务；白名单是否强制待定 |
| `filesystem` | `read` / `write` | 数据目录以外的指定路径，高风险，须逐项声明并填 `scope` |
| `credential` | `use` | 引用凭据库条目，**不得**取回明文 |
| `invoke_plugin` | `call` | 调用其他插件能力，**v1 恒拒绝** |

规则：

- 未声明即无权；运行期访问未声明资源必须失败。
- 声明了但从不使用，视为声明不准确，应在发布前清理。
- 不得「一把梭」声明全部类别。

### 4.1 配置声明（`config[]`）

配置项按**实例**填值：同一插件类型装在不同主机上可以有不同的端口、路径、账号。
清单只声明「有哪些配置项」，不声明值；值由底座在安装/下发时收集、校验、加密并落库。

规则：

- `key` 在类型内唯一；`type` 决定校验方式；`required: true` 缺失时安装/启动失败。
- `secret: true` 的值由底座加密存储，接口只返回掩码；插件不得把它写入日志、错误详情或数据目录。
- `secret: true` 时禁止 `default`，避免把密文默认值写进包内。
- `enum` 只能与 `type: enum` 搭配；候选值为字符串。
- 未识别的配置键必须被插件忽略；底座不得把未声明键写入配置文件。
- 配置项类型/枚举在升级中改变时，底座阻止升级并要求迁移（策略待 OQ-051）。
- 配置与证书无关：证书/私钥路径由底座生成到配置文件中，但不是用户可配置项，不得允许覆盖。

## 5. 校验失败的可诊断性

校验错误必须能定位到**字段路径**与具体项，例如：

- `identity.id`: 取值 `Demo_Echo` 不符合 `^[a-z][a-z0-9-]*$`
- `capabilities[0].input_schema_ref`: 文件 `schemas/echo.input.json` 不存在
- `version.contract`: 与底座支持的契约 MAJOR 无交集
- `entry.executable`: 路径越出插件包根目录
- `dependencies`: 非法字段（v1 不支持插件间依赖）

## 6. 完整示例

```yaml
identity:
  id: demo-echo
  display_name: 示例回声插件
  description: 回显输入内容，用于契约验证；不做任何外部访问

version:
  plugin: 0.1.0
  contract: ">=1.0 <2.0"

form: command

entry:
  executable: bin/demo-echo

capabilities:
  - id: demo-echo.echo
    description: 原样返回输入的 text 字段；无副作用，可重复调用
    input_schema_ref: schemas/echo.input.json
    output_schema_ref: schemas/echo.output.json
    idempotent: true
    long_running: false
    suggested_timeout_ms: 3000
    supports_cancel: false
    concurrency: parallel
    sensitive: false
    risk_level: normal

  - id: demo-echo.fail
    description: 按输入指定的错误类别返回失败，用于验证错误模型
    input_schema_ref: schemas/fail.input.json
    output_schema_ref: schemas/fail.output.json
    idempotent: true
    long_running: false
    supports_cancel: false
    concurrency: parallel
    sensitive: false
    risk_level: normal

permissions:
  - category: data_dir
    actions: [read, write]

runtime:
  max_result_bytes: 1048576
  resource_expectation: 单核、内存占用低于 50 MiB

metadata:
  author: 平台团队
  license: Apache-2.0
  homepage: https://example.invalid/demo-echo
  docs: README.md
```

常驻插件片段：

```yaml
form: daemon

entry:
  launch: bin/demo-agent
  socket: demo-agent.sock   # 本机专用；实际路径由底座生成并通过 --ops-socket 传入

runtime:
  listen:
    port: 29443             # 远端默认端口示例；实际生效值可按主机覆盖（范围待 OQ-048）

config:
  - key: log_path
    type: string
    required: true
    description: 目标机上的日志目录
  - key: upstream_url
    type: string
    required: false
    description: 上游服务地址
  - key: api_token
    type: string
    required: true
    secret: true
    description: 上游访问令牌；由底座加密存储并写入 0600 配置文件

capabilities:
  - id: demo-agent.report
    description: 汇总指定时间窗的指标；只读、无副作用
    input_schema_ref: schemas/report.input.json
    output_schema_ref: schemas/report.output.json
    idempotent: true
    long_running: false
    supports_cancel: true
    concurrency: serial
    sensitive: false
    risk_level: normal

  - id: demo-agent.delete
    description: 删除指定分区；不可逆，需 confirm_partition 严格等于目标分区
    input_schema_ref: schemas/delete.input.json
    output_schema_ref: schemas/delete.output.json
    idempotent: false
    long_running: true
    supports_cancel: true
    concurrency: serial
    sensitive: false
    risk_level: destructive   # 能力名末段为 delete，必须标记
```

对应的 `schemas/delete.input.json` 必须把确认字段列为必需：

```json
{
  "type": "object",
  "required": ["partition", "confirm_partition"],
  "properties": {
    "partition": { "type": "string" },
    "confirm_partition": { "type": "string" }
  }
}
```

## 7. 与其他文档的关系

- 输入输出结构细则：[04-io-conventions.md](04-io-conventions.md)
- 错误结构与退出码：[05-errors-and-exit-codes.md](05-errors-and-exit-codes.md)
- 版本声明规则：[07-versioning-compatibility.md](07-versioning-compatibility.md)
- 常驻插件启动与注入：[03-remote-protocol.md](03-remote-protocol.md)
- 常见问题与已定结论：[10-faq.md](10-faq.md)
- 清单模板：[manifest.template.yaml](manifest.template.yaml)
