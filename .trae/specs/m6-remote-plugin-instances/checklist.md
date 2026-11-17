# CHECKLIST：M6 远端插件、实例模型与启动包

## 契约与模型
- [x] 清单 1.2 新增字段（`risk_level`、`runtime.listen.port`、`config[]`）与 `plugin-spec/01` 及 schema 完全一致。
- [x] 契约区间仍只接受 `MAJOR.MINOR` 与 `>= < =`，未引入 `~`/`^`/PATCH。
- [x] 类型与实例分离；实例 ID 统一为 `local:<plugin>` / `remote:<host>:<plugin>`。
- [x] `custody` 互斥：命令式不得远端托管，daemon 不得本机 stdio 托管。
- [x] `/api/plugins` 仅为本机别名，未引入第二套状态语义。

## 安全
- [x] CA 私钥只以密文落盘，永不下发、不入日志、不入接口。
- [x] 叶子证书绑定插件 + 主机 + 用途（URI SAN + EKU），不绑定端口。
- [x] 调用侧手工校验链、EKU、URI、有效期、吊销；失败不回退明文。
- [x] 敏感配置加密落库，接口只回是否已设置，不返回值。
- [x] 撤销后证书吊销，实例不可调用。

## 状态与探测
- [x] `installed/started/callable` 三态独立；`enabled` 为业务开关，不覆盖技术可用性。
- [x] 漂移四类（包缺失、版本不符、证书不符、unit 丢失）均有检测与展示。
- [x] 探测超过 TTL 降级 `unknown`，界面不显示可调用。
- [x] 启动失败/停止失败有明确状态与原因，不吞错误。

## 授权与任务
- [x] 调用、探测、配置、事件均按实例能力判定，默认拒绝。
- [x] 定时任务以实例为目标；实例撤销后任务失败且不改投。
- [x] 批量操作（若存在）按实例集合展开并审计。

## 页面
- [x] 插件页、主机页、任务页均使用真实接口，无假数据与写死状态。
- [x] 空状态、错误态、待定项（OQ-047~051）标注齐全。

## 启动包
- [x] protoc 与插件版本锁定在脚本中，生成物入库，无手写桩（ADR-0024：protoc 36.2.0 / protoc-gen-go v1.36.6 / protoc-gen-go-grpc v1.5.1；OQ-045 已 decided）。
- [x] 命令式与 daemon 骨架可直接复制并本地自测（command：stdio 三路径；daemon：一次性测试证书端到端 mTLS）。
- [x] 启动说明与 plugin-spec 现有条款一致，未新增未确认字段。

## 文档与验证
- [x] `docs/18` 新建并登记；`docs/09` §6 旧口径已清除。
- [x] `docs/07` 与 `.ai/open-questions.yaml` 登记 OQ-047~051。
- [x] `.ai/index.md`、`docs/README.md`、`AGENTS.md` 同步。
- [x] `gofmt -l .` 无输出；`go vet ./...`、`go test ./...` 通过；构建脚本成功。
