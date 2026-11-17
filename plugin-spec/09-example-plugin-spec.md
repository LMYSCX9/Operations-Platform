# PS-09 最小示例插件规格（仅规格，不含代码）

> 用途：验证契约可实现、可校验、可测试。**本文只描述行为与验收，不提供实现代码。**

## 1. 示例定位

| 项 | 值 |
| --- | --- |
| 名称 | `demo-echo` |
| 定位 | 契约验证用最小插件；**不提供任何运维业务价值** |
| 形态 | 命令式插件（stdio） |
| 能力数 | 2（一个成功路径，一个失败路径） |
| 依赖 | 无外部依赖、无网络访问 |

选它作为示例的理由：行为完全确定、可断言、无副作用，适合作为契约一致性测试的基线。

## 2. 清单要素（语义）

| 信息组 | 取值 |
| --- | --- |
| 身份 | 标识 `demo-echo`；显示名「示例回声插件」；描述「回显输入内容，用于契约验证；不访问任何外部资源」 |
| 版本 | 插件 `0.1.0`；契约范围 `">=1.0 <2.0"` |
| 形态 | `command` |
| 入口 | `bin/demo-echo` |
| 能力 | `demo-echo.echo`（回显）、`demo-echo.fail`（受控失败） |
| 权限 | `data_dir: [read, write]`（示例实现可不使用，但需与声明自洽） |
| 运行假设 | 幂等；实际调度**一律串行**（清单 `concurrency` 仅为承受能力声明，不改变串行语义）；不支持取消（拒绝即退出）、建议超时 3000 ms |
| 元信息 | 作者/许可/文档链接（可选） |

完整清单示例见 [01-manifest.md](01-manifest.md) §6。

## 3. 能力 `demo-echo.echo`

**语义**：接收 `text`，原样返回；不读写外部资源，不产生副作用。

**输入**（JSON Schema 语义）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `text` | 字符串 | 是 | 长度 0–1024 字符；超出返回 `input_invalid` |

**输出**：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `text` | 字符串 | 与输入一致 |
| `length` | 整数 | 字符数（用于验证结果结构） |

**实际报文**（请求，单行）：

```json
{"contract_version":"1.0","capability":"demo-echo.echo","input":{"text":"hello"},"context":{"invocation_id":"01J8Z0Q3K5V9M2X7","deadline_ms":3000}}
```

**实际报文**（成功响应，单行）：

```json
{"status":"ok","output":{"text":"hello","length":5},"plugin_version":"0.1.0"}
```

**执行特征**：幂等、无外部依赖、无敏感数据。

## 4. 能力 `demo-echo.fail`

**语义**：根据输入 `mode` 返回指定类别的错误，用于验证错误映射。

**输入**：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `mode` | 枚举 | 是 | `input` \| `internal` \| `dependency` |

**行为**：

| `mode` | 结果 |
| --- | --- |
| `input` | 返回 `input_invalid`，退出码 2，且不产生输出数据 |
| `internal` | 返回 `internal_error`，退出码 1 |
| `dependency` | 返回 `dependency_unavailable`，退出码 7 |
| 其他值 | 返回 `input_invalid`（枚举越界），退出码 2 |

**实际报文**（失败响应，单行）：

```json
{"status":"error","error":{"code":"dependency_unavailable","message":"示例：外部依赖不可用","retryable":true}}
```

## 5. 日志要求

- 每次调用输出不超过 2 行日志到 stderr：一行开始、一行结束（含结果类别）。
- 日志不得包含完整输入原文（示例中 `text` 视为可能敏感）。

## 6. 验收用例

| # | 输入 | 期望 |
| --- | --- | --- |
| T1 | `demo-echo.echo`，`text="hello"` | 成功；输出 `text="hello"`、`length=5`；退出码 0 |
| T2 | `demo-echo.echo`，`text=""` | 成功；`length=0` |
| T3 | `demo-echo.echo`，缺少 `text` | 失败 `input_invalid`；退出码 2 |
| T4 | `demo-echo.echo`，`text` 长度 1025 | 失败 `input_invalid`；退出码 2 |
| T5 | `demo-echo.fail`，`mode=input` | 失败 `input_invalid`；无业务输出；退出码 2 |
| T6 | `demo-echo.fail`，`mode=internal` | 失败 `internal_error`；退出码 1 |
| T7 | `demo-echo.fail`，`mode=dependency` | 失败 `dependency_unavailable`；退出码 7 |
| T8 | 未声明的能力标识 | 失败 `capability_unknown` |
| T9 | 插件向 stdout 额外打印一行日志 | 底座判定协议违规 |
| T10 | 调用超时（模拟长时间执行） | 底座终止进程并返回 `timeout` |

## 7. 非目标

- 不实现任何业务逻辑。
- 不访问网络、不访问系统目录。
- 不提供界面、不持久化数据。
- 不作为生产插件发布。

## 8. 契约验证价值

该示例必须能同时验证：

1. 清单校验（信息组齐全、版本范围合法）。
2. stdio 通道纪律（stdout 纯净、stderr 日志）。
3. 错误类别与退出码一致性。
4. 结果结构校验与体积边界。
5. 超时终止与进程回收。

## 9. 待定项

| 主题 | 编号 |
| --- | --- |
| 结果体积上限、超时默认值 | OQ-017 |
| 字段命名风格、错误堆栈是否返回 | OQ-038 |
