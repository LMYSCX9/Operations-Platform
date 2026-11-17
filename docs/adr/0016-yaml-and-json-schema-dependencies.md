# ADR-0016 引入 YAML 与 JSON Schema 依赖（M1）

- 状态：`proposed`
- 日期：2026-11-16
- 决策者：待定（用户已批准引入方向，状态保持 `proposed`）
- 关联：OQ-008、OQ-039、[ADR-0003](0003-plugin-discovery-registration.md)、[ADR-0009](0009-configuration-model.md)、[DOC-03](../03-plugin-model.md)、[DOC-12](../12-m1-plugin-runtime.md)

## 背景

M1 需要同时完成两件事：

1. 插件清单 `plugin.yaml` 必须是 YAML，并按契约做**内置 JSON Schema 校验**（ADR-0003、DOC-03 §4）。
2. 配置文件格式从 JSON 改为 YAML，与清单复用同一套解析依赖（用户已批准该方向，见 OQ-039）。

标准库没有 YAML 解析器，也没有 JSON Schema 校验器。自行实现会带来解析边界、锚点/多文档、Draft-07 语义等
大量难以自证正确的工作，且与「规格是唯一事实来源、不得编造行为」的要求冲突。

## 决策

引入两个纯 Go 依赖：

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| `gopkg.in/yaml.v3` | `v3.0.1` | 配置文件与插件清单解析（`KnownFields(true)` 拒绝未知键） |
| `github.com/santhosh-tekuri/jsonschema/v5` | `v5.3.1` | 内置 Draft-07 schema 校验，输出可定位的字段路径 |

约束：

- 两者均为纯 Go，不引入 cgo，不破坏「单二进制、`CGO_ENABLED=0`」交付（ADR-0001、ADR-0007）。
- schema 文件随二进制内嵌（`//go:embed`），运行时不联网、不读取外部 schema。
- 配置文件中仍然禁止明文凭据；新增依赖不改变 ADR-0009 的密钥分离规则。
- 依赖只用于解析与结构校验；契约语义（标识前缀、版本区间、路径不越界）由底座自己实现。

## 理由

- **统一格式**：配置文件与插件清单都是 YAML，使用者只需要掌握一种语法；也避免同时维护 JSON 与 YAML 两套示例。
- **可诊断**：`jsonschema/v5` 能给出 `instanceLocation`，可转换为 `capabilities[0].id` 这类字段路径，满足 DOC-03 §4「指出字段路径与原因」。
- **纯 Go**：两个库都不依赖 cgo，交叉编译与单二进制交付不受影响。
- **维护活跃**：`yaml.v3` 是 Go 生态事实标准；`jsonschema/v5` 支持 Draft-07 且无外部依赖。

## 备选方案

| 方案 | 说明 | 未采用原因 |
| --- | --- | --- |
| A. 继续 JSON 配置 | 零新增依赖，M0 现状 | 与插件清单格式分裂；JSON 无注释，运维配置可读性差；用户已批准改 YAML |
| B. 自写 YAML 子集解析 | 不引入依赖 | 无法覆盖多行字符串、锚点、引号规则；解析歧义会直接变成契约行为 |
| C. 自写 schema 校验 | 只支持 25 个字段的固定结构 | 契约要求「内置 JSON Schema 校验」；自写校验器无法证明与 Draft-07 一致 |
| D. `gojsonschema` | 另一个 Draft-07 实现 | 依赖链更重、错误定位不便；`santhosh-tekuri/jsonschema/v5` 更轻且纯 Go |
| E. 用 `encoding/json` + YAML→JSON 转换 | 不引入 schema 库 | 仍需 YAML 解析器，且转换过程会丢失字段路径信息 |

## 影响

**正面**：配置与清单格式统一；校验错误可定位；运行时零外部依赖。

**负面 / 代价**：新增两个第三方依赖，需要纳入供应链审查；`yaml.v3` 的 `KnownFields(true)` 要求配置文件字段与结构体严格对应，升级配置项时必须同步结构体。

**迁移成本**：低。M0 的示例配置需要从 JSON 改为 YAML；不提供双格式兼容，避免出现两种事实来源。

## 验证方式

1. `go build ./...` 在 `CGO_ENABLED=0` 下通过。
2. YAML 配置文件含未知键时启动失败，错误信息包含键名。
3. 非法清单返回多个字段路径错误（`internal/plugin/manifest/validate_test.go`）。
4. `go test ./...` 覆盖配置优先级与清单正反例。
