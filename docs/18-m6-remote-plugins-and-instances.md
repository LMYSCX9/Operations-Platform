# DOC-18 M6 远端插件与实例实现基线

> 状态：`draft` ｜ 读者：底座开发、插件开发、评审 ｜ 上游：[DOC-09](09-host-management-and-plugin-distribution.md) §6、[DOC-17](17-plugin-registry-and-configuration.md) ｜ 决策：[ADR-0005](adr/0005-daemon-rpc-transport.md)、[ADR-0018](adr/0018-daemon-process-custody-and-injection.md)、[ADR-0022](adr/0022-remote-plugin-pki-and-mtls-certificates.md)、[ADR-0023](adr/0023-plugin-registry-instances-and-configuration.md)

## 1. 范围

本文件记录 M6 已落地的实现事实：内置 CA、证书签发与吊销、远端 mTLS 调用、类型/实例模型、三态与漂移、下发扩展与实例级授权。

语义与边界仍以 DOC-17 与上述 ADR 为准；本文件只回答「代码实际怎么做、配置键是什么、失败如何分类」。

**不在本文件内**：具体前端交互（见 [DOC-16](16-web-ui-m5.md) 与 M5 页面改造）、远端安装根目录（OQ-041）、人类身份源（OQ-037）。

## 2. 配置键与实现基线

所有数值均为**可配置的实现基线**，不是已定稿的产品阈值。对应待定项见 [DOC-07](07-open-questions.md) OQ-047～OQ-051。

| 配置键 / 命令行 | 默认值 | 作用 | 关联 OQ |
| --- | --- | --- | --- |
| `pki_base_id` | `base-01` | 内置 CA 与客户端证书的底座标识 | OQ-047 |
| `pki_ca_validity` | `87600h`（10 年） | CA 有效期；仅在首次创建 CA 时生效 | OQ-047 |
| `pki_leaf_validity` | `8760h`（365 天） | 叶子证书有效期 | OQ-047 |
| `plugin_probe_interval` | `1m` | 周期探测间隔 | OQ-049 |
| `plugin_state_ttl` | `5m` | 探测结果过期阈值，超时降 `unknown` | OQ-049 |
| `remote_dial_timeout` | `5s` | 远端 TCP + TLS 握手超时 | OQ-048 |
| `remote_ready_timeout` | `3s` | `Ready` / `GetVersion` 单次超时 | OQ-048 |
| `distribution_config_root` | `/etc/ops-platform/plugins` | 远端按主机配置与证书目录 | OQ-048 |
| `distribution_data_root` | `/var/lib/ops-platform/plugins` | 远端实例数据目录 | OQ-048 / OQ-051 |
| `distribution_unit_dir` | `/etc/systemd/system` | systemd unit 写入目录 | OQ-048 |
| `distribution_service_user` | `root` | unit 的 `User=`；最小权限账号待 OQ-028 | OQ-048 |
| `distribution_start_timeout` | `60s` | `daemon-reload + enable --now + is-active` 超时 | OQ-048 |
| `ssh_upload_timeout` | `5m` | 单次下发总预算（下发与文件写入共用） | OQ-029 |
| 探测重试窗口 | `30s` | `systemctl active` 后的 `Ready` 重试总时长（代码内常量） | OQ-048 |

证书算法固定为 **ECDSA P-256**（基线）；目前不提供算法切换配置，改动算法需走 ADR 修订（OQ-047）。

## 3. 内置 CA 与证书

### 3.1 落盘与加载

| 文件 | 权限 | 说明 |
| --- | --- | --- |
| `<data>/pki/ca.crt` | `0600` | CA 证书 PEM，可下发到目标机 |
| `<data>/pki/ca.key.enc` | `0600` | CA 私钥，AES-256-GCM 密文（主密钥来自 `credential_key`） |

- CA 私钥**永不下发、不入库、不写日志**；缺少主密钥时启动失败，不降级为明文（ADR-0022 §2）。
- 证书/私钥之一缺失时报「PKI 状态不一致」，要求人工恢复，不自动重建（重建会使全部已下发证书失效）。

### 3.2 签发规则

| 用途 | 主体 | URI SAN | EKU | 绑定键 |
| --- | --- | --- | --- | --- |
| 服务端 | `plugin:<插件>@<主机>` | `opsplatform://plugin/<插件>/host/<主机>` | `serverAuth` | 实例 |
| 客户端 | `base:<底座标识>` | `opsplatform://base/<底座标识>` | `clientAuth` | 底座 |

- 证书**不绑定端口**：迁移端口不需要重签（ADR-0022 §3）。
- 同一实例已有未吊销、未过期且剩余有效期 ≥10% 的服务端证书时复用，不重复签发；否则重签。
- 私钥以密文入 `cert_bindings.key_ciphertext`，仅在写入目标机时解密一次。

### 3.3 吊销与重签

- 吊销以**序列号**为准；重复吊销幂等。
- 下发失败回滚、撤销下发都会吊销该实例证书；拨号前的证书查询失败按「已吊销」处理（不确定不放行）。
- 重签由「重新下发」或证书接近过期触发，不复用旧序列号。

## 4. 远端调用

### 4.1 传输与校验

- 仅 **TCP + mTLS**；任何连接失败都**不回退明文**（ADR-0005）。
- 客户端校验顺序：证书链（内置 CA）→ EKU → URI SAN 精确匹配 → 有效期 → 吊销名单。
- 服务端要求客户端证书并校验 `clientAuth` + `base:<底座标识>` URI。
- 最小 TLS 版本 `1.2`。

### 4.2 阶段与错误分类

探测按 `connect → tls → ready → version` 分阶段，失败阶段可定位并写入探测记录。

| gRPC 状态 | 映射错误类别 |
| --- | --- |
| `DeadlineExceeded` | `timeout` |
| `Unavailable` / `Aborted` / `NotFound` / `Unimplemented` | `dependency_unavailable` |
| `PermissionDenied` / `Unauthenticated` | `permission_denied` |
| `InvalidArgument` / `FailedPrecondition` | `input_invalid` |
| 其他 | `internal_error` |

规则：

- 连接或 `Ready` 失败 → 实例降级；调用返回 `dependency_unavailable` 或 `timeout` 时立即把远端实例置为 `unknown` 且 `callable=false`。
- 结果体积超过 `plugin_max_result_bytes` 返回 `result_too_large`。
- 契约版本不一致在 `version` 阶段拒绝，不进入调用。

## 5. 类型、实例与存储

### 5.1 schema v7

| 表 | 内容 |
| --- | --- |
| `plugin_types` | 类型、版本、清单摘要、契约版本/区间、托管方式、默认端口、配置声明 |
| `plugin_type_capabilities` | 类型级能力、风险等级、幂等/长任务/并发 |
| `plugin_instances` | 实例、托管方式、三态、状态、漂移、证书序列号/指纹/到期 |
| `plugin_instance_configs` | 按实例的配置密文与更新人 |
| `instance_probes` | 探测时间、阶段、结果、耗时、详情 |
| `cert_bindings` | 序列号、指纹、用途、插件/主机/实例、有效期、吊销 |
| `registration_events` | 注册、启停、探测翻转、配置变更、撤销 |

`distributions` 增加 `instance_id`、`cert_serial`、`started_ok`；`tasks` 增加 `instance_id`。

### 5.2 标识规则

- 本机实例：`local:<插件标识>`。
- 远端实例：`remote:<主机标识>:<插件标识>`。
- `/api/plugins` 与 `/api/plugins/{id}/capabilities/{cap}/invoke` 保留为**本机实例别名**，解析后与 `/api/plugin-instances` 共用同一状态，不引入第二套语义。

### 5.3 状态与漂移

| 维度 | 取值 |
| --- | --- |
| 三态 | `installed` / `started` / `callable` 独立记录，不互相推断 |
| 失败 | `start_failed`、`stop_failed` |
| 其他 | `drifted`、`unknown`、`disabled`、`revoked` |
| 漂移 | `package_missing`、`version_mismatch`、`cert_mismatch`、`unit_missing` |

- 本机探测核对：注册表存在、安装目录、入口文件、包版本。
- 远端探测：连接、TLS、`Ready`、`GetVersion`；探测成功后再做一次独立 TLS 握手，比对叶子证书 SHA-256 与下发绑定（`cert_mismatch`）。
- 远端探测失败时，若主机模块可经 SSH 只读检查 unit 文件，则确认 unit 是否存在（`unit_missing`）；无法检查时不判漂移，避免把网络故障误报为删除。
- `Sweep`：超过 TTL 的远端实例降 `unknown`；本机实例按 TTL 重新核对安装事实以发现漂移。
- 周期探测只覆盖「已启用且已安装」的实例；手动探测不要求启用。

## 6. 下发扩展

### 6.1 产物与权限

| 产物 | 路径 | 权限 |
| --- | --- | --- |
| 插件包 | `<distribution_remote_root>/<插件>/<版本>` | 目录 `0755`，文件随包 |
| 按主机配置 | `<config_root>/<插件>/<实例>/config.yaml` | `0600` |
| 服务端证书 | 同目录 `tls/server.crt` | `0644` |
| 服务端私钥 | 同目录 `tls/server.key` | `0600` |
| CA 证书 | 同目录 `tls/ca.crt` | `0644` |
| systemd unit | `<unit_dir>/ops-platform-plugin-<实例>.service` | `0644` |
| 数据目录 | `<data_root>/<插件>/<实例>` | `0700` |

### 6.2 启动与三态

- 安装后执行 `systemctl daemon-reload && systemctl enable --now <unit> && systemctl is-active <unit>`。
- `is-active` 为 `active` 才记「已启动」；随后在探测重试窗口内做 `Ready` 探测，通过才记「可调用」。
- 启动失败或探测未通过：保留「已安装/已启动」事实，状态 `start_failed`，不伪造可调用。
- 命令式插件（`form: command`）没有远端 daemon：安装即终点，`callable=false`。

### 6.3 失败回滚与撤销

- 写配置/证书/unit、建数据目录、启动任一步失败：停止并删除 unit、`daemon-reload`、删除配置与证书目录、吊销证书，实例标 `start_failed`。
- 撤销下发：`disable --now` → 删 unit 并 `daemon-reload` → 删配置/证书/包目录 → 吊销证书 → 实例置 `revoked`、停用并保留静态事实。
- 数据目录默认保留，删除策略待 OQ-051。

## 7. 授权、任务与批量

- **授权按实例判定**：调用、启停、探测、配置、读取都以 `plugin_instance` 为目标；类型级只作模板（ADR-0006、OQ-050）。
- 定时任务目标为实例：任务执行时解析 `instance_id`；实例撤销或不可调用时任务失败并告警，不改投其他实例。
- 命令式插件不支持实例配置：接口显式返回 `not_ready` 并提示等待 OQ-051 决定下发方式，不静默丢弃。
- **批量操作**：当前代码库**不存在**主机或插件的批量执行入口（无 batch/bulk 路由）；因此 M6 无需展开批量实例化。未来新增批量入口时必须先固化实例集合并逐实例记录结果与审计（DOC-17 §8）。

## 8. 接口族

| 路径 | 方法 | 语义 |
| --- | --- | --- |
| `/api/plugin-types` | GET | 类型目录 |
| `/api/plugin-types/{id}` | GET | 类型详情 |
| `/api/plugin-instances` | GET | 实例列表（主机/类型/状态过滤） |
| `/api/plugin-instances/{id}` | GET | 实例详情（含类型、能力、配置状态） |
| `/api/plugin-instances/{id}/enable` / `disable` | POST | 启用/停用 |
| `/api/plugin-instances/{id}/probe` | POST | 手动探测 |
| `/api/plugin-instances/{id}/probes` | GET | 探测历史 |
| `/api/plugin-instances/{id}/config` | GET / PUT | 配置掩码 / 更新 |
| `/api/plugin-instances/{id}/capabilities/{capability}/invoke` | POST | 实例调用 |
| `/api/capabilities` | GET | 能力目录 |
| `/api/capabilities/{capability}/instances` | GET | 按能力反查实例 |
| `/api/hosts/{id}/distributions` | POST / GET | 下发安装 |
| `/api/distributions/{id}` | GET / DELETE | 详情 / 撤销 |
| `/api/distributions/{id}/retry` | POST | 重试 |

## 9. 验证

```powershell
go build ./...
go vet ./...
go test ./...
.\scripts\build.ps1
```

M6 新增单测覆盖：CA 签发/复用/吊销、mTLS 握手与 URI/EKU 校验、实例状态机、配置校验、远端错误映射、TTL 降级。

## 10. 待定与关联

| 主题 | 编号 |
| --- | --- |
| 证书算法切换、轮换周期、CA 备份 | OQ-047 |
| unit 细节、启停/探测超时、端口范围、journal | OQ-048 |
| 探测间隔、TTL、降级阈值、记录保留 | OQ-049 |
| 授权粒度与批量范围 | OQ-050 |
| 配置迁移、撤销保留期、类型变更 | OQ-051 |
| 远端安装根目录 | OQ-041 |
| 目标机最小权限账号 | OQ-028 |
