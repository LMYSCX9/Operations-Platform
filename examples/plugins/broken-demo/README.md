# broken-demo（反例插件）

该目录**不是可运行插件**，只用于验证发现阶段的拒绝路径。它同时包含以下违规：

| 违规点 | 字段路径 | 预期错误 |
| --- | --- | --- |
| 插件标识含下划线 | `identity.id` | 必须匹配 `^[a-z][a-z0-9-]*$` |
| 契约版本高于底座 | `version.contract` | 声明范围不包含底座支持的 1.0 |
| 能力标识缺少插件前缀 | `capabilities[0].id` | 必须以 `<identity.id>.` 开头 |
| 声明的 schema 文件不存在 | `capabilities[0].input_schema_ref` | 文件不存在或不可读 |
| 入口文件不存在 | `entry.executable` | 入口文件不存在 |
| 声明了插件间依赖 | `dependencies` | v1 禁止声明 dependencies |

验证方式：把本目录与 `demo-echo` 一起放入插件目录并触发扫描，`demo-echo` 正常登记，
本目录计入 `rejected`，`warnings` 中给出上述字段路径。

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:8080/api/plugins/scan
```
