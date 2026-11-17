# starter-command：命令式插件起步模板

整目录复制到一个新目录（建议目录名 = 插件 ID），即可获得一个能通过底座清单校验、
可被 stdio 调用的最小命令式插件。协议依据：本仓库 [plugin-spec/02-stdio-protocol.md](../../../plugin-spec/02-stdio-protocol.md)、
[05-errors-and-exit-codes.md](../../../plugin-spec/05-errors-and-exit-codes.md)。

## 目录内容

| 文件 | 作用 |
| --- | --- |
| `plugin.yaml` | 清单：身份、契约区间、入口 `bin/starter-command`、能力 `starter-command.greet` |
| `main.go` | stdio 协议骨架 + 示例能力 `greet`；改这里实现你的能力 |
| `contract.go` | 错误类别与退出码常量（从底座 `sdk/plugin/v1/contract.go` 复制，package 已改为 main） |
| `schemas/greet-input.json` / `greet-output.json` | 输入/输出 JSON Schema |
| `selftest.ps1` / `selftest.sh` | 本地自测：构建到 `bin/` 并验证 ok / input_invalid / capability_unknown 三条路径 |
| `go.mod` | 独立模块；复制后可改 module 名 |

## 本地验证（无需底座）

```powershell
.\selftest.ps1     # Windows
```

```sh
./selftest.sh      # Linux / macOS
```

脚本先 `go build -o bin/starter-command(.exe) .`，再向 stdin 喂三行请求，
断言：成功路径 `status=ok`；缺必填字段 → `input_invalid` + 退出码 2；
未知能力 → `capability_unknown` + 退出码 3。

## 复制后必改的位置

1. **插件 ID**：`plugin.yaml` 的 `identity.id`（小写短横线，全局唯一）与 `display_name`、`description`。
2. **能力 ID**：`capabilities[].id` 必须是 `<新插件 ID>.<能力名>`；同步改 `main.go` 的 dispatch 与 README。
3. **入口名**：`entry.executable` 建议改为 `bin/<新插件 ID>`，`selftest.*` 与 `go build -o` 里的产物名同步改。
4. **schema**：`schemas/` 按真实输入输出改写；`input_schema_ref` / `output_schema_ref` 指向实际文件。
5. **`contract.go`**：随底座升级重新复制；只使用已登记的错误类别。
6. **`go.mod`**：module 名改为你的仓库路径（可选）。

## 被底座发现与调用

- 入口必须构建进插件目录的 `bin/`：清单 `entry.executable` 按**真实文件**校验，
  缺失时底座清单校验直接失败；已登记后缺失会报「入口文件缺失 → package_missing 漂移」。
  Windows 上实机文件是 `<入口名>.exe`（清单按契约写无后缀路径，底座做同名回退，见 OQ-039）。
- 把整个插件目录（含 `bin/`、`plugin.yaml`、`schemas/`）放入底座 `plugin_dir`
  （默认 `<data_dir>/plugins/`，可用配置键 `plugin_dir` 改到数据目录内其他位置），
  重启底座或等待目录扫描 → 插件登记为类型与本机实例候选 → 控制台「插件」页启用。
- 调用入口：定时任务页目标选择该插件实例，或实例页「实例调用」。
- 能力声明与实现必须自洽（plugin-spec/README §4）：清单里没声明的行为一律不做；
  需要用户配置项时按 [plugin-spec/11](../../../plugin-spec/11-instance-and-config.md) 增加清单 `config[]`。
- `delete` / `restore` / `gc` 类能力必须声明 `risk_level: destructive`（契约 1.2），
  并在产生副作用前校验确认字段（ADR-0021）。

## 更完整的对照示例

[examples/plugins/demo-echo](../../plugins/demo-echo) 演示了双能力、可控失败与全部错误类别的用法；
本模板刻意保持单能力，方便从零起步。
