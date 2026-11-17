# PS-05 错误模型与退出码

> 对应底座规格：`docs/03-plugin-model.md` §5.3 ｜ 依据：底座 ADR-0004、ADR-0019（accepted）｜ 契约版本：`1.2`

## 1. 错误模型

```json
{
  "status": "error",
  "error": {
    "code": "input_invalid",
    "message": "字段 text 缺失（必填）",
    "details": { "field": "text", "reason": "required" },
    "retryable": false
  }
}
```

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `code` | 是 | 错误类别，取自 §2 枚举；插件不得自造未登记的类别 |
| `message` | 是 | 面向人的说明，一句话说清「什么不对、怎么改」；不得含敏感信息 |
| `details` | 否 | 可定位的结构化附加信息（字段名、位置、上游错误摘要） |
| `retryable` | 否 | 是否建议重试；缺省 `false`。仅表达建议，不构成自动重试授权 |

约束：

- 一个响应只表达**一个**主要错误，不得用数组堆叠多个互不相关的错误。
- 禁止仅返回字符串或无结构日志代替错误对象。
- 内部异常细节（堆栈、SQL、绝对路径）不得返回给调用方，只写插件自身日志。
- 错误堆栈是否允许以 `details` 返回仍需确认（OQ-038）；当前默认**不返回**。

## 2. 错误类别（枚举）

### 2.1 基础类别（契约 1.0 起）

| `code` | 含义 | 典型场景 | `retryable` 建议 |
| --- | --- | --- | --- |
| `input_invalid` | 输入**结构**不合法 | 缺必填字段、类型错误、枚举越界 | false |
| `capability_unknown` | 能力不存在 | 调用了清单未声明的能力 | false |
| `permission_denied` | 无权限 | 访问未声明的资源类别/动作 | false |
| `not_ready` | 未就绪 | 常驻插件尚未完成初始化 | true |
| `timeout` | 执行超时 | 超过 `deadline_ms` | true（视场景） |
| `cancelled` | 被取消 | 调用方取消 | false |
| `result_too_large` | 结果超限 | 超过体积上限，**禁止截断后返回成功** | false |
| `dependency_unavailable` | 外部依赖不可用 | 上游 API/数据库/凭据不可用 | true |
| `internal_error` | 内部错误 | 未预期异常 | 视场景 |
| `protocol_invalid` | 协议违规 | stdout 混日志、响应缺字段 | false |
| `invalid_response` | 响应结构不合法 | 输出不符合输出 schema | false |
| `version_incompatible` | 契约版本不兼容 | 声明范围与底座无交集 | false |

### 2.2 契约 1.1 新增类别

| `code` | 含义 | 与既有类别的边界 | `retryable` 建议 |
| --- | --- | --- | --- |
| `not_found` | 目标对象不存在 | 不是 `capability_unknown`（能力存在）；也不是 `input_invalid`（结构合法） | false |
| `validation_failed` | 结构合法但业务校验不通过 | `input_invalid` 只管结构/类型/必填/枚举 | false |
| `precondition_failed` | 前置条件不满足（状态、顺序、锁） | 不是 `dependency_unavailable`（外部依赖不可达） | false |

归类规则（按顺序判定）：

1. 字段缺失、类型错、枚举越界 → `input_invalid`。
2. 字段齐全但语义冲突（如 `confirm_partition` ≠ 目标分区）→ `validation_failed`。
3. 目标不存在 → `not_found`。
4. 目标存在但当前状态不允许 → `precondition_failed`。
5. 外部系统不可达 → `dependency_unavailable`。

**使用前提**：使用 1.1 新增类别时，清单 `version.contract` 的下限必须 **≥ 1.1**（如 `">=1.1 <2.0"`）。
声明 `<1.1` 的插件返回这些类别会被底座归一为 `internal_error`，并作为契约漂移告警。

新增类别属契约 MINOR（须登记到 SDK 常量与本文档；SDK 常量文件：底座仓库 `sdk/plugin/v1/contract.go`，
daemon 骨架 `examples/starter/daemon/gen/pluginv1/contract.go` 携带副本）；删除或改变既有含义属 MAJOR。

### 2.3 底座对未登记类别的处理

- 不崩溃、不拒绝响应、不自动重试。
- 对外归一为 `internal_error`；执行记录保留原始 `code`（`error_category_original`）。
- 记「未登记错误类别」告警计数（按插件聚合），用于发现私造类别。

## 3. 退出码（命令式插件）

本节**仅适用于命令式插件**；daemon 不使用退出码表达业务结果，差异见 §3.1。

| 退出码 | 含义 | 与错误类别的关系 |
| --- | --- | --- |
| 0 | 成功 | 响应必须 `status=ok` |
| 1 | 内部错误 | `internal_error` |
| 2 | 输入/目标相关 | `input_invalid`、`validation_failed`、`not_found` |
| 3 | 能力不支持 | `capability_unknown` |
| 4 | 权限/凭据问题 | `permission_denied` |
| 5 | 超时 | `timeout` |
| 6 | 结果过大 | `result_too_large` |
| 7 | 依赖不可用**或前置条件不满足** | `dependency_unavailable`、`precondition_failed` |
| 8 | 协议违规自检 | `protocol_invalid` / `invalid_response` |

规则：

1. 退出码与响应 `error.code` **必须语义一致（同桶）**；矛盾时底座按失败处理并记录「协议不一致」。
2. 退出码不得替代错误详情；`message`/`details` 仍必须给出。
3. 退出码固定为 **0~8 共 9 个值**，不新增 9 及以上；未列出的退出码底座按 `internal_error` 处理并记录实际值。
4. 被信号终止（无退出码）：底座按 `internal_error` 处理，并记录终止信号。
5. 退出码是**粗分类**：精确原因以响应 `code` 为准，调用方不得仅凭退出码做业务判断。

### 3.1 常驻插件：无退出码语义

命令式与 daemon 的错误语义差异：

| 维度 | 命令式 | daemon |
| --- | --- | --- |
| 业务错误表达 | 响应 `status=error` + `error.code`，**同时**用退出码粗分类 | **只**经 `InvokeResponse.error` |
| 退出码 | 固定 0~8，且必须与 `error.code` 同桶 | **无业务退出码语义**；§3 的退出码规则（含同桶校验）**不适用** |
| 进程退出/信号 | 参与结果判定与协议一致性记录 | 仅用于诊断与审计 |
| 异常退出折算 | 按退出码/信号处理 | 未就绪 → `not_ready`；在途调用崩溃 → `internal_error`；无在途 → 不产生调用错误 |

- daemon 的错误**只**经 `InvokeResponse.error` 表达；进程退出码不表达业务结果。
- 进程退出码/信号仅用于诊断与审计，不参与协议一致性校验。
- 进程异常退出时对调用方的折算：

| 场景 | 折算类别 |
| --- | --- |
| 启动阶段退出（未就绪） | `not_ready` |
| 有在途调用时崩溃/被杀 | `internal_error`（`retryable` 由底座按 `idempotent` 判定） |
| 底座 `Shutdown` 后正常退出 | 不产生新错误；未完成调用为 `cancelled` |
| 无在途调用时崩溃 | 不产生调用错误，仅转 `unhealthy` 并审计 |

折算出的类别**不得**冒用业务类别（如 `not_found`）。

## 4. 消息质量

| 反例 | 正例 |
| --- | --- |
| `出错了` | `字段 text 缺失（必填）` |
| `Error: invalid input` | `字段 window 取值 "2h" 不在允许集合 [1h, 6h, 24h] 内` |
| `failed` | `上游 API 返回 503，已重试 2 次仍失败`（不含 URL 中的 token） |

要求：说明**发生了什么**、**在哪**、**调用方可以怎么改**；不暴露实现细节与敏感信息。

## 5. 待定项

| 主题 | 编号 |
| --- | --- |
| 是否允许返回错误堆栈 | OQ-038 |
| 错误类别的告警映射（哪些类别触发告警） | OQ-033 |
