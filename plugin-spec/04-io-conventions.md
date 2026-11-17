# PS-04 输入输出约定与校验

> 对应底座规格：`docs/03-plugin-model.md` §4、§5.3 ｜ 依据：底座 ADR-0004、ADR-0005、ADR-0021（accepted）｜ 契约版本：`1.2`

## 1. 校验顺序

```mermaid
flowchart LR
    A[接收请求] --> B[信封校验<br/>contract_version/capability/context]
    B --> C[结构校验<br/>按输入 JSON Schema]
    C --> D[业务校验<br/>语义/取值范围/前置条件]
    D --> E[执行]
    E --> F[输出结构校验<br/>按输出 JSON Schema]
    F --> G[序列化返回]
```

| 阶段 | 责任方 | 失败错误类别 |
| --- | --- | --- |
| 信封校验 | 底座（网关） | `protocol_invalid` |
| 结构校验 | 底座（内置 JSON Schema）与插件（二次确认） | `input_invalid` |
| 业务校验 | 插件 | `validation_failed`（语义冲突）、`not_found`（目标不存在）、`precondition_failed`（状态不允许） |
| 输出结构校验 | 插件（自检）与底座（校验） | `invalid_response` |

1.1 起业务校验必须区分「结构问题」与「语义问题」：结构问题用 `input_invalid`，语义问题用 `validation_failed`，
目标不存在用 `not_found`，状态不允许用 `precondition_failed`（见 [05](05-errors-and-exit-codes.md) §2.2）。

## 1.1 高危操作确认字段

`delete` / `restore` / `gc` 的确认字段为**契约强制**，必须在任何副作用之前校验（细则见 [01](01-manifest.md) §3.5）：

| 能力 | 必填字段 | 校验 |
| --- | --- | --- |
| `delete` | `confirm_partition` | 必须严格等于同请求的目标分区标识 |
| `restore` | `confirm` + 目标字段 | `confirm` 必须为 `true`，目标字段必填 |
| `gc` | `confirm` + 目标字段 | 同上 |

- 缺失字段 → `input_invalid`；`confirm_partition` 不相等 → `validation_failed`；`confirm` 为 `false` → `precondition_failed`。
- 确认字段不得有默认值，不得由底座或 SDK 自动填充。
- 底座只做静态一致性校验与调用前审计，不校验确认值与目标是否一致。

## 2. 结构定义方式

- 输入/输出结构以 **JSON Schema** 声明，文件放在插件包内，由清单的 `input_schema_ref` / `output_schema_ref` 引用。
- 顶层必须是 `object`。禁止顶层为数组或标量（保证可向前加字段）。
- 每个字段必须给出：类型、是否必需、取值范围或枚举、说明、至少一个示例值。
- 未声明的字段：底座不保证透传语义；插件应忽略未知字段（向前兼容）。
- 具体 JSON Schema 草案版本由底座内置校验器固定，并在 SDK 中声明；规格不在此处另立版本号。

示例（`schemas/echo.input.json`）：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["text"],
  "properties": {
    "text": { "type": "string", "minLength": 1, "maxLength": 4096, "examples": ["hello"] }
  }
}
```

（`$schema` 声明可省略：底座以自身内置校验器支持的草案版本为准。）

## 3. 命名与格式

| 对象 | 约定 |
| --- | --- |
| 插件标识 | `^[a-z][a-z0-9-]*$`（小写短横线） |
| 能力标识 | `<插件标识>.<能力名>`，两段同规则 |
| 错误 `code` | 小写下划线，取自 [05](05-errors-and-exit-codes.md) §2 的枚举 |
| 时间 | RFC 3339（含时区偏移） |
| 布尔 | JSON `true` / `false`，不得用字符串表示 |
| 数值 | 明确单位（如 `_ms`、`_bytes` 后缀），不得隐含单位 |
| 敏感字段 | 清单中 `sensitive: true` 时，日志与审计只记录存在性或摘要 |

字段命名风格（下划线 / 小驼峰）**待定**（OQ-038）；在定稿前同一插件内必须保持一致。

## 4. 幂等

- 幂等能力：相同输入（忽略 `context.invocation_id` 与时间戳类字段）重复调用，结果与副作用等价。
- 非幂等能力必须在清单中声明 `idempotent: false`，且不得被配置为自动重试（见底座规格 DOC-10 的重试策略）。

## 5. 体积与流式

- 单次结果体积上限**待定**（OQ-017）；超限必须返回 `result_too_large`，禁止截断后返回成功。
- v1 不支持流式与分段结果；大数据应通过「返回句柄 + 后续查询能力」或写入自身数据目录再返回引用。
- 二进制数据用 base64 字符串承载。

## 6. 敏感数据

- 请求/响应中不得出现明文口令、私钥、令牌。
- 需要凭据时必须通过 `credential.use` 权限引用凭据条目，由底座在授权后提供。
- 错误 `message` 与 `details` 不得回显敏感值；底座在日志中按 `sensitive` 标记脱敏。

## 7. 向后兼容

| 变更 | 允许 | 版本影响 |
| --- | --- | --- |
| 新增可选字段 | 是 | MINOR |
| 新增能力 | 是 | MINOR |
| 新增枚举取值 | 是（下游必须有默认分支） | MINOR |
| 删除字段、改类型、改必填性、改语义 | 否 | MAJOR |
| 字段改名 | 否 | MAJOR |

## 8. 待定项

| 主题 | 编号 |
| --- | --- |
| 字段命名风格、是否统一分页约定 | OQ-038 |
| 结果体积上限、超时默认值 | OQ-017 |
| 是否向插件注入配置/数据目录环境变量 | OQ-008 |
