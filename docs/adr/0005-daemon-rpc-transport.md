# ADR-0005 常驻插件远程调用协议与传输

- 状态：`accepted`（2026-11-16 修订：传输扩展为「本机 UDS + 远端 TCP/mTLS」两种）
- 日期：2026-11-16
- 决策者：用户（P0 六项批准；本轮补充：否决 SSH 隧道与节点代理，远端采用 TCP + 双向 TLS）
- 关联：OQ-001（decided）、OQ-010/OQ-027（decided）、OQ-047、OQ-048、[DOC-03](../03-plugin-model.md) §5.2、[DOC-05](../05-extension-points-and-compatibility.md) §3、[plugin-spec/03](../../plugin-spec/03-remote-protocol.md)、[ADR-0012](0012-module-boundaries-and-sdk.md)、[ADR-0018](0018-daemon-process-custody-and-injection.md)、[ADR-0022](0022-remote-plugin-pki-and-mtls-certificates.md)

## 背景

常驻插件长期驻留并提供能力。必须决定：传输层、序列化与契约、调用寻址方式、生命周期归属、安全边界。

关键约束：插件是**动态加载**的，底座在编译期不知道插件的能力集合，因此**不能依赖为每个能力生成静态方法**。

初版只覆盖「底座与插件同机」。用户随后要求远端插件直接部署在目标机并可被调用，并否决了 SSH 隧道与节点代理；
因此传输层必须扩展为两种部署位置，同时保持同一套 RPC 契约与错误语义。

## 决策

**同一套 gRPC 契约、两种传输：本机 UDS；远端 TCP + 双向 TLS。只暴露通用 `Invoke`，不预生成强类型方法。
不使用任何形式的无认证 TCP。**

### 1. 传输与安全

#### 1.1 本机形态

| 项 | 结论 |
| --- | --- |
| 传输 | 本机 Unix Domain Socket（UDS） |
| 地址 | 底座生成并通过 `--ops-socket` 注入；`<data-dir>/run/plugins/<plugin-id>/<instance-id>.sock` |
| 网络暴露 | **不得**监听 TCP（含回环） |
| 认证 | 启动期一次性凭据文件 + 每次调用携带的短期凭据（[ADR-0006](0006-plugin-authz.md)、[ADR-0018](0018-daemon-process-custody-and-injection.md) §3） |
| 对端身份 | 文件系统权限（`0600` / 目录 `0700`）+ 短时凭据；不依赖网络位置 |

#### 1.2 远端形态

| 项 | 结论 |
| --- | --- |
| 传输 | TCP + **双向 TLS（mTLS）** |
| 地址发现 | 主机记录中的地址 + 「主机-插件安装记录」中的生效端口；端口默认来自清单 `runtime.listen.port`，可按主机覆盖（[ADR-0018](0018-daemon-process-custody-and-injection.md) §4.3） |
| 连接建立 | 底座拨号 `tcp://<host-address>:<effective-port>`，携带客户端证书；插件以服务端证书应答；两侧都校验到内置 CA 的链 |
| 身份校验 | 底座校验服务端证书的 CN/SAN 与「插件标识 + 主机标识」严格相等；插件校验客户端证书的 CN/SAN 为期望的底座节点标识；同时校验 EKU、有效期与吊销名单（[ADR-0022](0022-remote-plugin-pki-and-mtls-certificates.md) §2） |
| 失败处理 | 握手失败/证书过期/序列号被吊销/身份不匹配 → 不进入 `Ready`，状态为不可调用；按退避重试，**不得**回退明文或跳过校验；连续失败达阈值转 `unhealthy` 并告警 |
| 超时 | 连接、握手、`Ready` 探测各自有独立超时；具体数值待 OQ-048 |
| 端口可达 | 由用户负责防火墙与网络策略；底座只如实记录「不可达」，不修改防火墙、不做端口映射 |
| 日志 | 进程日志由目标机 journald 承载；底座诊断时按需读取（方式待 OQ-048） |

#### 1.3 为什么仍不开放无认证 TCP

- 调用面跨主机后，**网络可达即调用可达**；无认证 TCP 等于把高危能力（删除、恢复、GC）暴露给任何能连上端口的进程。
- 能力级授权发生在底座网关，但前提是「调用方确实是该底座节点、被调用方确实是该插件」；没有证书就没有这个前提。
- 仅靠端口随机化、防火墙或「内网可信」不是可审计的安全边界，也无法支持吊销。
- 因此：**明文 TCP 一律禁止**，包括回环地址与调试场景；没有「开发模式跳过 TLS」的开关。

### 2. 调用寻址

- 只提供**一个**业务方法 `Invoke`，入参含 `capability`（能力标识）+ `input_json`（JSON 字节）+ 上下文。
- **不**为每个能力生成 RPC 方法：动态插件无法在底座编译期预生成方法，强类型方法会强制重新编译底座。
- 能力载荷的结构定义由插件在清单中以 JSON Schema 声明，底座只做结构校验与透传。
- 部署位置无关：本地与远端使用**同一** `capability` 命名规则与同一份 proto；部署位置不改变能力标识。

### 3. 服务定义（契约骨架）

```proto
syntax = "proto3";
package opsplatform.plugin.v1;

option go_package = "opsplatform/sdk/plugin/v1;pluginv1";

service PluginService {
  rpc GetVersion(GetVersionRequest) returns (GetVersionResponse);
  rpc Ready(ReadyRequest) returns (ReadyResponse);
  rpc Health(HealthRequest) returns (HealthResponse);
  rpc Invoke(InvokeRequest) returns (InvokeResponse);
  rpc Shutdown(ShutdownRequest) returns (ShutdownResponse);
}

message InvokeRequest {
  string contract_version = 1;  // 宿主使用的契约版本 MAJOR.MINOR
  string capability       = 2;  // <插件标识>.<能力名>
  bytes  input_json       = 3;  // UTF-8 JSON，结构由能力输入 schema 定义
  InvocationContext context = 4;
}

message InvocationContext {
  string invocation_id = 1;
  int64  deadline_ms   = 2;  // 剩余时间预算，插件必须遵守
  string subject       = 3;  // 调用主体，可能敏感
  string credential    = 4;  // 短期凭据（契约 1.1 新增；远端可省略）
}

message InvokeResponse {
  string status        = 1;  // "ok" | "error"
  bytes  output_json   = 2;  // 成功时的结果 JSON
  PluginError error    = 3;  // 失败时的结构化错误
  string plugin_version = 4;
}

message PluginError {
  string code      = 1;  // 错误类别，枚举见 plugin-spec/05 §2
  string message   = 2;
  bytes  details_json = 3;
  bool   retryable = 4;
}
```

说明：

- 字段名与编号属契约，**变更须走 MAJOR**（[DOC-05](../05-extension-points-and-compatibility.md) §4）。
- `status` 与 `error` 的语义与 stdio 形态**完全一致**，两种形态共用同一错误模型（INV-6）。
- 传输层（UDS / TCP+TLS）不改变 proto；部署位置只影响地址发现与认证方式。
- 完整字段说明与示例见 [plugin-spec/03](../../plugin-spec/03-remote-protocol.md)。

### 4. 进程归属

| 形态 | 归属 |
| --- | --- |
| 本机 | 底座以子进程方式拉起并托管；就绪/健康/重启/关闭由底座负责 |
| 远端 | 目标机 systemd 托管；底座通过 SSH 下发 unit、`systemctl enable/start`，并负责状态探测 |

- 同一主机 + 同一插件只允许一种托管方式（[ADR-0018](0018-daemon-process-custody-and-injection.md) §1.4）。
- 本机 UDS 路径由底座生成并注入，插件不得硬编码；远端监听地址与端口来自按主机生成的配置文件。
- 异常退出的处理：本机由底座按退避策略重启；远端由 systemd `Restart=on-failure` 承担，底座只观测并记录。

### 5. 引入的依赖（已批准）

- **gRPC 与 protobuf 工具链**（Go 侧 gRPC 运行时 + protoc/protoc-gen-go/protoc-gen-go-grpc）为已批准依赖，
  理由：契约可版本化、超时/元数据/状态码语义成熟、生态与跨语言支持好，且通用 `Invoke` 能解决动态能力寻址。
- **TLS 使用 Go 标准库 `crypto/tls` 与 `crypto/x509`**，不引入新的第三方依赖；证书签发与存储见 ADR-0022。
- 除上述之外**不引入**其他新依赖；具体库版本在落地时随 `go.mod` 锁定，不在规格中臆造版本号。

## 被否方案与原因

| 方案 | 说明 | 否决原因 |
| --- | --- | --- |
| 仅本机 UDS，不支持远端 | 初版结论 | 用户要求远端插件可在目标机独立服务；且 SSH 隧道/节点代理已被否决 |
| 远端 SSH 隧道承载 gRPC | 复用 SSH 通道 | 调用传输与运维凭据耦合；底座需维持长连接与会话复用，故障面大；用户已否决 |
| 目标机部署节点代理（agent）转发 | 统一出站 | 需要额外的常驻组件与自举问题；用户已否决 |
| 远端明文 TCP | 实现简单 | 网络可达即调用可达，高危能力直接暴露；无法审计与吊销 |
| 远端单向 TLS（只验服务端） | 稍好于明文 | 插件无法识别调用方，任何本机进程可冒充底座 |
| HTTP + JSON（REST，每能力一路径） | 语言无关、易调试 | 路径需随动态能力生成，契约弱、无统一状态码语义 |
| 自定义帧协议 | 完全可控、无依赖 | 需自建超时/元数据/流控，维护成本高、生态差 |
| 为每个能力生成强类型 RPC | 类型安全 | 底座编译期不知道插件能力集，每次插件升级都要重编底座 |
| Go plugin / 进程内加载 | 性能最好 | 违背故障隔离（NFR-RUN-05），插件语言受限，平台兼容性差 |
| 本机也改用 systemd 托管 | 统一模型 | 本机已有底座作为唯一管理者，双托管会产生两套状态事实来源 |

> 初版曾以「证书生命周期与端口暴露面管理成本高」为由否决 TCP/mTLS。该结论已被本轮用户决策推翻；
> 成本通过「内置 CA + 下发安装时签发 + 序列号吊销」控制在可接受范围（ADR-0022）。

## 影响与约束

**正面**：一套契约适配两种部署位置；本机通信面最小；远端有明确的对端身份与吊销能力；契约可版本化并自动生成客户端。

**负面 / 代价**：引入 gRPC/protobuf 工具链与 TLS 证书生命周期；远端需 systemd 与可写状态目录；
端口与防火墙成为运维职责；底座需实现两套地址发现与探测逻辑。

**实现约束**：

- 插件必须能区分「未就绪」「已崩溃」「超时」「证书/身份失败」几类失败，不得统一返回泛化错误（NFR-REL-03）。
- 底座对每个插件**一律串行调度**（跨能力亦串行，[ADR-0021](0021-capability-execution-and-destructive-confirmation.md)）。
- 调用上下文中的 `subject` 不得写入插件日志明文；`credential` 不得落盘。
- 本机 socket 权限与远端私钥权限必须按 ADR-0018 / ADR-0022 执行。
- 远端连接不得复用本机 UDS 客户端；两套拨号器分别记录指标与错误。

## 验证方式

1. 本机常驻插件崩溃 → 调用返回可区分错误类别，底座不退出，并按策略重启。
2. 未就绪时调用（本机/远端）→ 立即返回 `not_ready`，不挂起。
3. 远端无客户端证书连接 → 握手失败，状态不可调用。
4. 远端服务端证书与插件标识/主机不匹配 → 底座拒绝，记录证书序列号与期望标识。
5. 使用已吊销序列号的证书 → 握手被拒；日志可定位到吊销。
6. 远端端口按主机覆盖 → 新端口握手成功，证书无需重签。
7. 契约字段被改名 → 旧插件被兼容性校验拒绝（MAJOR 变更检测生效）。
8. 插件监听未授权地址（本机 TCP / 远端未配置地址）→ 视为违规，底座拒绝启用并记录。
9. 调用超过 `deadline_ms` → 返回 `timeout`，插件停止占用资源。
10. 远端主机不可达 → 状态停在 `start_failed`/不可调用，不伪造成功。

## 遗留待定

- 证书算法、有效期、轮换周期、CA 备份：OQ-047。
- systemd unit 细节、远端启动/停止/探测超时、端口默认值与范围：OQ-048。
- 并发上限、载荷大小上限：OQ-017；健康检查频率与阈值：OQ-012。
- 远端安装根目录：OQ-041。
