# M6 远端插件、实例模型与插件开发启动包 Spec

## Why

M1–M5 只覆盖「本机命令式插件 + 下发安装」：下发产物不会启动、也不会被调用，插件按插件标识而非「某台机器上的具体安装」被识别与授权。要支撑多主机真实运维，必须补齐内置 CA / mTLS、远端常驻插件托管、类型与实例分离、实例级授权与配置，并让插件作者拿到可直接复制的开发启动包。

## What Changes

- 新增内置 CA 与证书生命周期：生成/加载、加密落盘、服务端与客户端签发、序列号吊销、重签。
- 新增远端插件托管：按主机生成配置与 systemd unit、写入证书、`daemon-reload` + `enable --now`、三态回报（已安装/已启动/可调用）、失败回滚、撤销清理。
- 新增远端调用通道：TCP + mTLS，手工校验证书链/EKU/URI SAN/有效期/吊销，错误分类与超时，**不回退明文**。
- 新增类型/实例模型：`plugin_types`、`plugin_type_capabilities`、`plugin_instances`、`plugin_instance_configs`、`instance_probes`、`cert_bindings`、`registration_events`；`/api/plugins` 保留为本机类型目录别名。
- 新增实例 API：类型、实例、能力、探测、配置、调用、事件。
- 授权改为按实例能力判定；定时任务与批量操作目标改为实例。
- 新增探测调度与漂移检测（包缺失、版本不符、证书不符、unit 丢失）与状态过期降级。
- 修正旧口径：「下发不启动、不调用」的文案与 `callable:false` 固定值全部移除。
- M5 页面改为实例视角：插件页、主机页、任务页全部走真实接口，无假数据。
- 新增插件开发启动包：gRPC 生成脚本与锁版本、SDK 契约常量、命令式与 daemon 两种骨架、启动说明。
- **BREAKING**：`tasks.plugin_id` 语义保留但新增 `instance_id`；调用记录新增 `instance_id`；远端插件地址由「不调用」变为真实调用。

## Impact

- Affected specs：DOC-09 §6、DOC-17、plugin-spec 03/11、契约 1.2。
- Affected code：`internal/pki`、`internal/plugin/{instances,remote,instance_service}`、`internal/host/{model,store,distributor,service}`、`internal/api`、`internal/scheduler`、`internal/store`、`internal/config`、`cmd/ops-platform/main.go`、`prototype/index.html`、`sdk/plugin/v1`、`scripts/`、`examples/plugins/`。

## ADDED Requirements

### Requirement: 内置 CA 与证书

系统 SHALL 在数据目录内生成或加载内置 CA，CA 私钥仅以密文落盘且永不下发；SHALL 按「插件标识 + 主机标识 + 用途」签发叶子证书，URI SAN 为 `opsplatform://plugin/<plugin>/host/<host>` 与 `opsplatform://base/<base>`，EKU 分别为 serverAuth/clientAuth；SHALL 支持按序列号吊销与重签。

#### Scenario: 首次启动生成 CA

- **WHEN** 数据目录无 CA 且主密钥可用
- **THEN** 生成 CA 证书与加密私钥，权限 0600，日志不含私钥

#### Scenario: 主密钥缺失

- **WHEN** 主密钥缺失
- **THEN** 证书签发与读取显式失败，不降级为明文私钥

#### Scenario: 吊销后不可用

- **WHEN** 证书序列号被吊销
- **THEN** 拨号与握手校验均拒绝该证书

### Requirement: 远端插件托管

系统 SHALL 在下发时写入按主机配置、证书与 CA、systemd unit，执行 `daemon-reload` 与 `enable --now`，并回报已安装/已启动/可调用三态；任一步失败 SHALL 回滚（停止服务、删除 unit、删除配置、吊销证书）并记录原因。

#### Scenario: 下发成功

- **WHEN** 远端主机可用且单元启动成功、`Ready` 探测通过
- **THEN** 下发记录三态均为真，实例状态 `callable`

#### Scenario: 启动失败

- **WHEN** systemd 启动失败
- **THEN** 下发记录 `start_failed`，实例 `start_failed`，证书吊销，残留清理

#### Scenario: 撤销下发

- **WHEN** 用户撤销
- **THEN** 停止并禁用服务、删除 unit 与配置、吊销证书、实例置 `revoked` 且不可调用，数据目录保留

### Requirement: 远端调用与错误语义

系统 SHALL 使用 TCP + mTLS 调用远端插件，校验证书链、EKU、URI SAN、有效期与吊销；SHALL 对连接、握手、`Ready`、调用分别设超时；SHALL 将失败归类为可展示原因；SHALL 在 mTLS 配置失败时拒绝调用，**不得回退明文**。

#### Scenario: 证书不匹配

- **WHEN** 对端证书 URI 与目标实例不匹配
- **THEN** 调用失败并标记证书异常，不重试明文

### Requirement: 类型与实例模型

系统 SHALL 区分插件类型与实例，实例标识为 `local:<plugin>` 或 `remote:<host>:<plugin>`；SHALL 校验 `custody` 互斥（命令式→本机，daemon→远端）；`/api/plugins` SHALL 作为本机类型目录别名保留。

#### Scenario: 同一主机同一类型

- **WHEN** 重复下发
- **THEN** 幂等命中或按升级处理，不产生第二个活动实例

### Requirement: 状态、漂移与探测

系统 SHALL 维护 `installed/started/callable` 三态及 `start_failed/stop_failed/drifted/unknown/disabled/revoked`；SHALL 周期探测启用实例；探测结果超过 TTL SHALL 降级为 `unknown`；SHALL 检测包缺失、版本不符、证书不符、unit 丢失四类漂移。

#### Scenario: 探测过期

- **WHEN** 远端实例超过 TTL 未成功探测
- **THEN** 状态 `unknown`，界面不显示可调用

### Requirement: 实例级授权与任务

系统 SHALL 按实例能力判定调用、探测、配置与事件权限，默认拒绝；定时任务 SHALL 以实例为目标，实例撤销后任务失败且不改投。

### Requirement: M5 实例视角页面

插件页、主机页、任务页 SHALL 使用实例接口展示类型/实例/三态/漂移/配置/探测，保持真实接口、无假数据，未定项标注 OQ。

### Requirement: 插件开发启动包

系统 SHALL 提供锁定版本的 proto 生成脚本、SDK 契约常量、命令式与 daemon 两种可复制骨架与说明；生成物 SHALL 入库，不得手写桩。
