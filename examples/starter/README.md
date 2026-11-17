# 插件开发启动包（starter pack）

复制即用的插件骨架。目标：插件作者整目录复制后，不读底座源码就能开工，并全程对齐
[plugin-spec](../../plugin-spec/README.md) 契约。

| 形态 | 目录 | 传输 | 本地验证 | 接入底座 |
| --- | --- | --- | --- | --- |
| 命令式 | [command/](command/) | stdio（NDJSON，按次调用） | `selftest.ps1` / `selftest.sh`（纯本地） | 本机 `plugin_dir` 扫描 → 启用 → 任务/实例调用 |
| 常驻（远端 daemon） | [daemon/](daemon/) | gRPC over TCP + mTLS（systemd 托管） | `selftest.ps1` / `selftest.sh`（一次性测试证书端到端） | Linux 产物 → 下发到主机 → 三态 → 实例调用 |

一个插件只声明一种形态（plugin-spec/README §4.2）；不确定选哪个时读
[03-remote-protocol.md](../../plugin-spec/03-remote-protocol.md) §1 的形态对比表。

## 共同的复制后检查单

1. **改身份**：`plugin.yaml` 的 `identity.id`（小写短横线，全局唯一）、`display_name`、`description`。
2. **改能力**：能力 ID 改为 `<新插件 ID>.<能力名>`；同步改代码 dispatch 与 `schemas/`。
3. **改入口**：`entry.executable`（command）/ `entry.launch`（daemon）改为 `bin/<新插件 ID>`；
   构建产物名与自测脚本同步改。
4. **改模块名**（可选）：两个骨架各自带独立 `go.mod`（`example.com/starter-*` 占位），
   复制后改成你的仓库路径。骨架自带 `go.sum`（daemon），构建开箱即用。
5. **改契约范围**：需要 1.1 新错误类别时下限 `>=1.1`；声明远端部署或 `config[]` 时下限 `>=1.2`
   （plugin-spec/README §4.9）。
6. **对齐能力声明**：清单声明 = 实际行为；`delete`/`restore`/`gc` 必须 `risk_level: destructive`。

## 错误与版本常量的单一来源

- SDK 契约常量：`sdk/plugin/v1/contract.go`（契约版本、错误类别、命令式退出码）。
  事实来源是 [plugin-spec/05](../../plugin-spec/05-errors-and-exit-codes.md)，新增类别属契约 MINOR。
- daemon 骨架的生成物三件套在 `gen/pluginv1/`，与底座 `sdk/plugin/v1/` 字节一致；
  工具链锁定 protoc `36.2.0` / protoc-gen-go `v1.36.6` / protoc-gen-go-grpc `v1.5.1`，
  生成脚本 `scripts/gen-proto.ps1` / `scripts/gen-proto.sh`，升级流程见 [ADR-0024](../../docs/adr/0024-proto-toolchain-baseline.md)。
- **禁止手写桩**：需要重新生成时用锁定工具链，没有 protoc 时按 plugin-spec/03 §9 的降级路径处理。

## 本地验证（每段完成后立刻跑）

```powershell
# 命令式
cd examples/starter/command; .\selftest.ps1
# 常驻
cd examples/starter/daemon; .\selftest.ps1
```

两个自测都会先把构建产物落到**插件目录 `bin/` 下**——这是硬要求：清单入口按真实文件校验，
产物不在 `bin/` 时清单校验直接失败；已登记后缺失则报「入口文件缺失 → package_missing 漂移」。
Windows 上实机文件为 `<入口名>.exe`（清单按契约写无后缀路径，底座做同名回退，OQ-039）。

## 被底座发现与调用（真实链路）

1. **放入插件目录**：整个插件目录（含 `bin/`、`plugin.yaml`、`schemas/`）放入底座
   `plugin_dir`（默认 `<data_dir>/plugins/`，可用配置键 `plugin_dir` 指向数据目录内其他位置）。
2. **类型登记**：底座目录扫描后登记插件类型与本机实例候选（不自动启用）。
3. **启用与调用**：控制台「插件」页启用；「任务」页选目标实例；daemon 类型走「主机」页下发到
   Linux 主机（自动签发证书、写 unit、启动、三态探测）后经实例调用。
4. **排查**：实例三态原因、探测历史、执行记录；本机安装事实漂移会标记 `package_missing` / `version_mismatch`。

## 红线速览（完整清单见 plugin-spec/00-contract-checklist.md）

- stdout 纯净（命令式）；日志走 stderr / journald；不得输出凭据与敏感输入。
- 错误必须结构化（`error.code` 取已登记类别），退出码与 `error.code` 同桶。
- 必须承受超时终止；不留子进程与临时文件；不依赖 TTY。
- 不得调用其他插件、不得读取其他插件数据目录、不得请求清单外权限。
- 常驻插件：只监听注入的地址；证书由底座签发；不回退明文；一律串行。

## 更多参考

- 完整示例（双能力 + 可控失败）：[examples/plugins/demo-echo](../plugins/demo-echo)
- 反例（故意违约，用于验证拒绝路径）：[examples/plugins/broken-demo](../plugins/broken-demo)
- 契约自查：[plugin-spec/00-contract-checklist.md](../../plugin-spec/00-contract-checklist.md)
