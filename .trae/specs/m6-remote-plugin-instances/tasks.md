# TASKS：M6 远端插件、实例模型与启动包

> 对应 [spec.md](spec.md)。按顺序执行；每个里程碑完成立即验证并汇报。

## A. 测试回归修复（M6 代码已落地，先恢复绿灯）

- [x] A1 修复 `internal/host/distributor_test.go` 中 4 处 `dist.process(...)` 调用，补 `*Deployment` 参数 `nil`。
  - 验证：`go test ./internal/host/...`
- [x] A2 修正 `internal/plugin/manifest/validate_test.go` 契约用例：底座支持 1.2 后 `=1.1` 应为 true。
  - 验证：`go test ./internal/plugin/manifest/...`
- [x] A3 修正 `internal/scheduler/service_test.go` 期望调用标识为 `local:demo-echo`。
  - 验证：`go test ./internal/scheduler/...`

## B. M6 功能补全

- [x] B1 契约 1.2 扩展：`risk_level`、`runtime.listen.port`、`config[]`、契约区间解析、清单 schema 同步。
- [x] B2 内置 CA：生成/加载、加密私钥、签发服务端/客户端、序列号吊销、重签。
- [x] B3 schema v7：类型、能力、实例、实例配置、探测、证书绑定、注册审计、下发与任务实例列。
- [x] B4 实例服务：类型同步、实例视图、启用/禁用、探测、配置、事件、实例调用。
- [x] B5 远端 gRPC + mTLS 客户端与错误分类。
- [x] B6 下发扩展：证书/CA/配置/unit/启停/三态/回滚/撤销。
- [x] B7 API 与实例级授权；调度器与任务目标改实例。
- [x] B8 探测补全：本地实例漂移（包缺失、版本不符、入口缺失）纳入 `Sweep`；`ProbeEnabled` 覆盖本机与远端。
  - 验证：单测覆盖过期降级与四类漂移。
- [x] B9 批量操作实例化：检查现有主机/插件批量入口，统一按实例集合展开并审计；无批量入口则记录为不适用。
- [x] B10 新增 M6 单测：PKI 签发/校验/吊销、实例状态机、配置校验与掩码、远端错误映射、TTL 降级。
  - 验证：`go test ./internal/pki/... ./internal/plugin/instances/... ./internal/plugin/...`

## C. M6 文档同批更新

- [x] C1 新建 `docs/18-m6-remote-plugins-and-instances.md`（实现基线：配置键、表、状态、错误码、探测与下发流程）。
- [x] C2 更新 `docs/09` §6：下发产物含证书/CA/按主机配置/unit/启动/三态/撤销回滚；移除「不启动、不调用」旧口径。
- [x] C3 更新 `docs/17`、`docs/07`（OQ-047~051 基线值与落点）、`docs/adr/0018`、`docs/adr/0022`、`docs/adr/0023` 实现状态。
- [x] C4 更新 `.ai/index.md`、`.ai/open-questions.yaml`、`docs/README.md`、`AGENTS.md` 当前阶段。
- [x] C5 更新 `plugin-spec/01`、`03`、`11` 与 schema，保持与契约 1.2 一致；同步 `tasks.md`/`checklist.md`。
- [x] C6 M6 验证：`gofmt -l .`、`go vet ./...`、`go test ./...`、构建脚本；汇报。

## D. M5 页面实例视角

- [x] D1 插件页：类型目录 + 实例列表/详情（三态、漂移、探测历史、配置掩码、实例调用）。
- [x] D2 主机页：主机详情展示实例与下发三态，下发参数含端口，撤销/重试使用真实接口。
- [x] D3 任务页：目标选择实例（保留本机别名兼容），展示实例不可用原因。
- [x] D4 空状态/错误态/待定标注；`web/dist/index.html` 同步；验证并汇报。

## E. 插件开发启动包

- [x] E1 `scripts/gen-proto.ps1` / `.sh`：锁定 protoc 36.2.0 与 protoc-gen-go v1.36.6 / protoc-gen-go-grpc v1.5.1，生成 `sdk/plugin/v1/*.pb.go`（幂等，字节一致；npm 包 protoc@36.2.0 经 npx，生成头记内部版本 v7.36.2 属预期，ADR-0024）。
- [x] E2 SDK 契约常量 `sdk/plugin/v1/contract.go`（契约版本、错误类别、退出码），plugin-spec/03 §9 与 05 引用。
- [x] E3 `examples/starter/command/`：最小命令式插件（manifest、schema、main、contract.go 副本、selftest.ps1/.sh、README、独立 go.mod）；本地自测通过（ok / input_invalid(2) / capability_unknown(3)）。
- [x] E4 `examples/starter/daemon/`：最小远端 daemon 插件（gRPC+mTLS、按主机配置、expected_client 校验、串行 Invoke、gen/pluginv1 三件套副本、一次性测试证书自测工具、README）；本地端到端自测通过。
- [x] E5 启动包总说明 `examples/starter/README.md`（复制必改清单、本地验证、底座发现链路、bin/ 产物硬要求）；plugin-spec/README §2 增加骨架替代路径。
- [x] E6 新增 ADR-0024（工具链版本基线与升级流程）、关闭 OQ-045；docs/07、.ai/open-questions.yaml、.ai/index.md、docs/README.md、plugin-spec/03/05/README、本文件与 checklist 同批更新；修复本机漂移检查对 Windows `.exe` 入口的误报（`resolveLocalEntry` + 测试）；最终验证通过。

# 任务依赖

- A 必须先于 B/C/D/E（保证红灯清除）。
- B8/B9 依赖 B4/B6。
- C 与 B 同批（文档即代码）。
- D 依赖 B7（实例 API）。
- E 独立，可在 D 之后执行。
