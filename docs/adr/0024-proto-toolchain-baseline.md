# ADR-0024 proto 工具链版本基线与升级流程

- 状态：`accepted`
- 日期：2026-11-17
- 决策者：用户（「插件开发启动包」任务明确要求锁定 protoc 版本并登记基线，本 ADR 为其落点）
- 关联：OQ-045（关闭）、[ADR-0020](0020-contract-source-and-protoc-toolchain.md)、[ADR-0005](0005-daemon-rpc-transport.md)、[plugin-spec/03](../../plugin-spec/03-remote-protocol.md) §9、`scripts/gen-proto.ps1` / `scripts/gen-proto.sh`、`sdk/plugin/v1/`

## 背景

ADR-0020 定了契约唯一来源、生成物入库与「脚本锁版本」的形状，把具体版本号留给 OQ-045。
「插件开发启动包」（examples/starter）要求插件作者复制骨架即可开工，工具链必须可复现：

- 生成物 `plugin.pb.go` / `plugin_grpc.pb.go` 已入库，重新生成必须与入库版本**字节一致**；
- 本机可能没有 protoc，需要一条不依赖环境的获取路径；
- 版本升级必须有三件套同步的明确流程，否则生成物与运行时库漂移。

## 决策

### 1. 锁定版本基线（OQ-045 关闭）

| 工具 | 锁定版本 | 获取方式 |
| --- | --- | --- |
| protoc | `36.2.0` | npm 包 `protoc@36.2.0`，经 `npx -y protoc@36.2.0` 调用；npmmirror 分发，无需全局安装 |
| protoc-gen-go | `v1.36.6` | `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6` |
| protoc-gen-go-grpc | `v1.5.1` | `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1` |

与运行时库的匹配（ADR-0020 §3）：`go.mod` 中 `google.golang.org/protobuf v1.36.10`、
`google.golang.org/grpc v1.79.3`。生成器与运行时库主版本一致，升级运行时库必须同批次重新生成。

**已知现象（务必知悉，避免误判漂移）**：`protoc --version` 输出 `libprotoc 36.2`，但
protoc-gen-go 写入生成文件头的是编译器**内部版本串** `protoc v7.36.2`。两者对应同一发布版
（npm 包 `protoc@36.2.0` → 头部 `v7.36.2`），差异属预期；生成脚本按头部串校验。

### 2. 生成脚本行为（`scripts/gen-proto.ps1` / `gen-proto.sh`）

- 三个版本号固定在脚本内；先探测本机 protoc-gen / protoc 版本，**不符即失败并打印精确安装指引**，
  绝不静默改用其他版本、绝不自动安装；
- protoc 经 `npx -y protoc@36.2.0` 调用（首次下载后进入 npx 缓存；Node.js 缺失时给出指引）；
- 统一用 `-I .` + `paths=source_relative` 生成到 `sdk/plugin/v1/`，保证描述符内源路径为
  `sdk/plugin/v1/plugin.proto`，与入库产物一致；
- 生成后校验产物文件头包含锁定版本串；
- **幂等性**：在无契约变更时重复运行，产物字节不变（已实测：SHA-256 前后一致）。

### 3. 升级流程（三件套必须同步）

1. 同时更新 `scripts/gen-proto.*` 内三个版本常量与本 ADR §1 表格（一次提交，不得只改其一）；
2. 安装对应版本的 protoc-gen-go / protoc-gen-go-grpc，protoc 经 npx 版本参数指定；
3. 运行生成脚本；diff 只允许出现本次契约变更，无契约变更时 diff 必须为空；
4. 若 proto 有变更：同步 `plugin-spec/03` §5 副本（逐字一致，ADR-0020）、SDK 契约常量
   `sdk/plugin/v1/contract.go` 与 [plugin-spec/05](../../plugin-spec/05-errors-and-exit-codes.md)；
5. 升级 `go.mod` 运行时库（protobuf/grpc）并回归 `go test ./...`；
6. 更新 `examples/starter/daemon/gen/pluginv1/` 三件套副本（启动包跟随底座）。

### 4. 启动包与 SDK 常量

- `sdk/plugin/v1/contract.go`：契约常量（契约版本、错误类别、退出码、status 取值），
  事实来源为 plugin-spec/05，随契约变更同步修订；不 import 底座内部包（ADR-0020 §5）。
- `examples/starter/daemon/gen/pluginv1/`：proto + 生成物 + 契约常量的完整副本，
  插件复制后独立可用；唯一允许的本地改动是 `plugin.proto` 的 `go_package`（模块路径），
  字段名与编号不得改动；升级按 §3.6 走。

## 被否方案与原因

| 方案 | 否决原因 |
| --- | --- |
| protoc 全局安装 / 随环境而定 | 版本不可复现，生成物易漂移；与「脚本锁版本」矛盾 |
| 脚本自动安装/升级工具 | 静默改变工具链版本，违背「不符即失败」的可审计原则 |
| 手写等价生成代码（桩） | ADR-0020 §4 明令禁止；不可被 CI 校验 |
| 各骨架自带独立 protoc | 工具链分裂，多份「事实来源」 |

## 影响与约束

**正面**：干净环境 `go build ./...` 即可（构建不依赖 protoc）；改契约时生成可复现；
启动包复制后无需搭建工具链。

**负面 / 代价**：版本升级需三件套同批；npx 首次运行需要网络（npmmirror）；
头部版本串与 CLI 版本号不一致需要文档说明。

## 验证方式

1. 运行 `scripts/gen-proto.ps1` / `.sh`：退出码 0，产物与入库版本字节一致（SHA-256 相同）。
2. 改动 `plugin.pb.go` 任一字符后重跑脚本：文件恢复为锁定产物（幂等）。
3. 卸载/更换任一工具版本后运行脚本：以非零码失败并打印精确指引。
4. `examples/starter/daemon` 的 `selftest.ps1` / `selftest.sh` 通过（生成物可用性）。

## 遗留待定

- 无（OQ-045 关闭）。SDK 独立发布形态仍属 OQ-018。
