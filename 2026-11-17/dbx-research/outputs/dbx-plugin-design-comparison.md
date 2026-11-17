# DBX 插件设计与本仓库插件模型对比

> 调研日期：2026-11-17 ｜ 对象：DBX（dbxio.com，站点展示版本 v0.6.71）与本仓库 `docs/03`、`docs/05`、`docs/17`、`plugin-spec/01`、`plugin-spec/03`、`plugin-spec/05`
> 说明：本文是**调研报告**，不改动 `docs/` 与 `plugin-spec/`。凡官网/仓库未写明的内容一律标注「公开资料未说明」，不做推测。

## 1. DBX 是什么

DBX 是一个开源、轻量的**数据库与数据基础设施工作台**（桌面 / Docker-Web / CLI 三种形态，宣称 100+ 种数据库与数据系统、约 25MB 安装包、完全开源），见 [dbxio.com](https://dbxio.com/cn) 与 [What is DBX?](https://dbxio.com/en/docs/what-is-dbx)。

需要先纠正一个前提：**DBX 并没有把「所有能力」都做成插件**。它的核心数据库连接能力（100+ 引擎的 Rust 原生驱动、SQL 方言、类型映射）是**内置**的，构建期注册表位于仓库 `plugins/connection-types`、`plugins/dialects`、`plugins/mappings`；插件是「可选、按需安装、不增大基础安装包」的增量扩展（[plugins/README.md](https://github.com/t8y2/dbx/blob/main/plugins/README.md) 开篇与 Repository layout）。S3、Kubernetes、SSH、JDBC 这类确实是插件形态，例如官方目录中的 [S3 Browser](https://dbxio.com/en/plugins/io.github.t8y2.s3)、Kubernetes Manager、Terminal、Files Studio、JDBC 插件。

## 2. DBX 的插件模型

### 2.1 形态与打包方式

- 插件是独立的 **`.dbxp` 安装包**，与「源码仓库」「已安装插件」是三件分离的产物（[README · Source, package, and installation](https://github.com/t8y2/dbx/blob/main/plugins/README.md)）。
- `.dbxp` 是**带额外约束的 ZIP**：必须含 `manifest.json`、`checksums.json`、`signature.json`（显式开发安装除外）、`bin/<target>/<exe>`、`assets/`、`ui/`；拒绝绝对路径、父目录穿越、重复条目、符号链接、超大条目与解压炸弹；已安装版本不可变，激活记录支持回滚；安装/回滚/卸载/信任库变更共用一把文件锁（README · Package layout）。
- 纯前端插件发布一个 `universal` 包；含原生 sidecar 的插件必须按 **操作系统 + CPU 架构**分别出包（[插件开发文档](https://dbxio.com/en/docs/plugin-development)）。
- 开发项目布局：`manifest.json` + `dbx-plugin.toml` + `assets/` + `ui/` + `backend/`（可选原生 sidecar）+ `.github/workflows/plugin-release.yml` + `dist/`。

### 2.2 安装与发现机制

- 入口是 **Plugin Center**，内部分三块：**Marketplace**（聚合仓库、搜索、按平台选包、安装/更新）、**Installed**（本地生命周期）、**Settings**（本地 `.dbxp` 安装、自定义仓库、开发用未签名包、仓库信任）（README · Marketplace and repository model）。
- **官方仓库地址写死在客户端契约里**，不可编辑：`https://raw.githubusercontent.com/t8y2/dbx-store/main/catalog/index.json`；官方签名公钥内置在 DBX 中，可用 `DBX_PLUGIN_MARKETPLACE_TRUSTED_KEYS_JSON` 追加轮换密钥；自定义仓库由用户自管公钥，但官方仓库只接受 DBX 托管密钥，防止自定义密钥冒充官方包（README 同上）。
- 安装来源有四类：市场点击安装、**Install from URL**（安装前做签名校验，见[插件详情页](https://dbxio.com/en/plugins/terry.devtools)）、Settings 里的**本地 `.dbxp` 安装**、开发用**未签名包**（需显式打开开发安装开关）。
- **不是目录扫描**。安装时校验：目录 JSON ≤ 4 MiB、包 ≤ 512 MiB、仅允许 HTTP(S) 且限制重定向、单仓库故障隔离、校验目录声明的 size 与 SHA-256、并要求包内 Manifest 的 ID/版本/publisher/权限/Ed25519 key ID 与目录记录一致后才激活（README）。

### 2.3 插件与宿主的交互

**UI 侧**：`ui/` 运行在**沙箱 iframe**（受限 CSP、有界桥接载荷、安全资源路径、插件身份绑定），通过 `window.dbxPlugin` 的 **Host API** 与 DBX 通信（README · Security model；插件开发文档）。

**后端侧**：`backend/` 是**常驻子进程 sidecar**，stdin/stdout 专用于协议、诊断只能写 stderr，协议为 **JSON-RPC 2.0**（README · Backend protocol）：

- `stdio-jsonl`：一行一个 JSON，单条消息上限 8 MiB；`stdio-framed`：5 字节头（kind + 大端长度），支持二进制通道，单帧上限 64 MiB。
- 启动即 `plugin/initialize` 握手：宿主下发 `host.dbxVersion` / `host.hostApiVersion` / `protocolVersions` / 插件 id+version / 授予的 permissions；插件返回 `protocolVersion`、`capabilities`、自身身份。协议或身份不匹配时宿主**在暴露会话前**拒绝。
- sidecar **按需拉起一次、按插件共享**（不按标签页/连接各起一个），插件自管内部会话表；DBX 在替换/回滚/卸载前拆除插件自有连接池。
- 宿主支持并发在途请求、逐请求超时、严格 JSON-RPC 校验、崩溃传播与有界事件缓冲；握手/协议/输出失败会自动终止子进程。
- 典型方法：`connection/test`、`connection/connect`、`connection/disconnect`、`connection/action`、`filesystem/list|read|write|createDirectory|delete|rename`、`contextMenu/<id>`、`mcp/tools`、`mcp/call`、`redis/codec/decode`；Host API 1.1 起还允许**插件反向调用宿主**的 `host/requestUserInput`（用字符串 id 与宿主数字 id 区分方向）。
- 另有**旧版 JDBC 插件协议**：每请求 30s 超时，仅支持 `testConnection`/`connect`/`executeQuery`/`listDatabases`/`listSchemas`/`listTables`/`getColumns`（[JDBC Plugin](https://dbxio.com/en/docs/plugins)）。

### 2.4 能力、配置与界面入口的声明方式

全部通过 **Manifest v1（`manifest.json`，JSON Schema 校验，拒绝未声明字段）** 声明，没有「代码注册」通道（[插件开发文档](https://dbxio.com/en/docs/plugin-development)、[manifest.schema.json](https://github.com/t8y2/dbx/blob/main/plugins/manifest.schema.json)）：

- **贡献点 `contributions[]`**：`connection-provider`（声明连接表单字段、`binding` 语义、`test/connect/disconnect`、自定义 action）、`workbench`（工作台 UI 入口）、`filesystem-provider`（URI scheme、`root_uri`、`read/write/delete/rename/mkdir`）、`context-menu`（连接/表右键）、`result-view`、`command`/`menus`、`mcp`（MCP 工具面）。
- **能力 `capabilities[]`**：只用于少数「宿主可识别能力」的注册（如 `kind: "redis-value-codec-v1"`），**不是**通用调用单元；绝大多数行为靠上述贡献点 + sidecar 方法承载。
- **权限 `permissions[]`**：`host.workbench` / `host.events` / `host.filesystem` / `host.binary` / `host.storage` / `host.ai` / `host.clipboard:read` / `host.data:read` / `host.connections:read` / `host.plans:read` / `host.schema:read`，以及带 origin 的 `host.network:https://host[:port]`、`host.openUrl:http(s)://...`。权限在**安装时授予并原样展示**（[插件详情页](https://dbxio.com/en/plugins/terry.devtools)）。
- **配置项不是独立声明**，而是连接表单字段：`binding: "config"` 存外部配置、`"secret"` 进 Secret Store、其余映射到标准连接字段。文档明确要求「不要把密码、Token 或私钥放进 config、Workbench context、事件或日志」。

### 2.5 第三方开发、分发与更新

- **开放第三方开发**：`dbx-plugin create` 脚手架（npm `@dbx-app/plugin-cli`）+ Rust / Go SDK；其它语言可直接实现 JSON-RPC 协议（插件开发文档、README）。
- **分发链路分权**：作者在自己仓库构建**未签名候选包**（每平台一个，附 `release-candidates.json`）→ 向 `t8y2/dbx-store` 提审 → DBX Store 人工审核源码/清单/权限/哈希 → 受保护的签名工作流用**仓库密钥**签名 → 目录 PR 只登记元数据，不存二进制（[RELEASING.md](https://github.com/t8y2/dbx/blob/main/plugins/RELEASING.md)）。
- **签名模型**：SHA-256 + 可信 Ed25519 **仓库签名**；`publisher` 只记作者身份，**不是签名密钥所有者**；v1 **明确不做作者签名**（理由：作者密钥的恢复/轮换/组织转移成本高，且不改善「仓库→用户」的完整性路径）；支持 `signingKeyId` 轮换与 `revoked.json` 吊销（[SIGNING.md](https://github.com/t8y2/dbx/blob/main/plugins/SIGNING.md)）。
- **更新**：由 Marketplace 管理，版本不可变、支持回滚；同一 `id + version` 的正式包不可覆盖，字节变更必须发新语义化版本。
- **版本与兼容**（README · Compatibility policy）：`manifest_version` 只在旧宿主无法解释清单形状时升；sidecar **协议版本**只在 wire 不兼容时升；`engines.host_api` 管 Host API 兼容、`engines.dbx` 管产品版本约束；同一 host API 大版本内新增贡献点字段应保持可选。

### 2.6 官方明确写出的限制

- **原生 sidecar 以当前 OS 用户权限运行，不是 OS 沙箱**；签名只证明「哪个仓库批准并发布了这份字节」，不等于作者无害，「只安装你信任其后端代码的插件」（README · Security model）。
- **原生进程的文件系统/网络访问目前无法被 DBX 完全中介**；能做的是 UI 侧出网全封 + 只放行声明的 `host.network:` origin（README）。
- `host.network:` **不是 sidecar 的网络防火墙**；且同一上限中英文文档不一致：英文页写 64 个 origin，中文页写 8 个（见 §6）。
- 未签名包**只能**通过显式「开发安装」开关安装；有保存的连接仍引用插件时**禁止卸载**。
- 单条 JSON 消息 8 MiB、二进制单帧 64 MiB、文件系统 inline 读写 4 MiB、MCP 工具发现 8s / 调用 120s；旧 JDBC 协议每请求 30s（README、[JDBC 插件页](https://dbxio.com/en/docs/plugins)）。
- **新权限/新贡献点会破坏旧宿主**：旧版 DBX 遇到未知权限会在安装时直接失败，未知贡献点类型会让旧宿主读不懂 manifest（插件开发文档 `host.ai`、`mcp` 两节）。
- 私有仓库凭据必须走「secret-store 支撑的请求层」，不能写进 `.repositories.json`（README）。
- DBX 的 Web API「主要服务 DBX Web 与内部集成，不是稳定公共契约」（[What is DBX?](https://dbxio.com/en/docs/what-is-dbx)）。

## 3. 与本仓库逐条对照

| 维度 | 本仓库（Operations-Platform） | DBX |
| --- | --- | --- |
| 插件定位 | 运维底座**全部业务能力**由插件提供（命令式/常驻），底座只留基础设施 | 基础数据库能力**内置**；插件只做可选增量（S3/K8s/SSH/JDBC/工具），不增大基础包 |
| 形态 | `command`（stdio NDJSON，一次调用一个进程）与 `daemon`（gRPC；本机 UDS 托管**尚未实现**，远端 TCP+mTLS） | UI 沙箱 iframe + 可选**常驻 sidecar**（JSON-RPC 2.0 over stdio，jsonl 或 framed）；没有网络 RPC 形态 |
| 扩展面 | EP-01 命令、EP-02 服务；EP-03/04 配置与数据目录；EP-05 UI/EP-06 事件/EP-07 身份源均为 `idea` | 连接类型、工作台 UI、文件系统、右键菜单、结果视图、命令/菜单、MCP 工具面（UI 与业务扩展是一等公民） |
| 打包产物 | 插件包 = `plugin.yaml` + 可执行文件（+ 服务描述/资源），摘要或签名强度待 OQ-015 | `.dbxp` ZIP = manifest + checksums + Ed25519 签名 + 分平台二进制 + UI 资源；纯前端可用 universal |
| 清单 | `plugin.yaml`，内置 JSON Schema + 语义校验；**未知字段忽略**（向前兼容） | `manifest.json`，JSON Schema；**拒绝未声明字段**（`additionalProperties: false`），靠 `manifest_version` 硬切形状变化 |
| 能力声明 | 能力是**调用最小单位**：`<插件标识>.<能力名>`、输入输出 schema、幂等/长任务/取消/并发/敏感性/`risk_level` | `capabilities[]` 只做宿主可识别能力（如 Redis codec）；真正入口是 `contributions[]`（provider/workbench/filesystem/mcp…） |
| 权限模型 | 能力级授权 + 「资源类别 + 动作」，**默认拒绝**；`invoke_plugin` v1 恒拒绝；`scope` 是否强制待 OQ-037 | 安装时授予的 Host API 权限码（`host.data:read` 等）+ 逐连接用户同意 + MCP 工具 access 上限；**原生 sidecar 不在权限系统内** |
| 安装与发现 | 本机目录扫描 / 本地导入 → 只产生**类型候选**；安装/启用才建实例；远端不扫描，实例只来自下发记录 | Plugin Center 市场（官方 catalog + 自定义仓库）、Install from URL、本地 `.dbxp`、开发未签名包；**无目录扫描** |
| 分发与更新 | v1 无应用市场；包靠人工传输 + SSH 下发；升级/回滚策略待 OQ-051 | 官方目录 + 第三方/私有仓库；版本不可变、支持回滚、可吊销；仓库级签名 + 人工审核 |
| 完整性/信任 | 摘要或签名，强度**待定 OQ-015**；远端另有内置 CA 与 mTLS 证书绑定 | SHA-256 + Ed25519 仓库签名 + catalog 元数据交叉校验 + `signingKeyId` 轮换 + 吊销清单；作者身份靠审核记录而非作者签名 |
| 契约版本 | `MAJOR.MINOR` 范围（插件写 `">=1.0 <2.0"`），底座同时支持同一 MAJOR 下多个 MINOR（当前 1.0/1.1/1.2） | 三层：`manifest_version`（形状）、`engines.host_api`（Host API 范围）、`backend.protocol_versions`（wire），另有 `engines.dbx` 约束产品版本 |
| 实例/部署模型 | **类型与实例分离**；同一 `(主机, 类型)` 一个活动实例；实例有独立配置、证书、探测与三态 | 无类型/实例概念：本机已安装插件 + 保存的连接；卸载受连接引用约束，没有跨主机实例与漂移视图 |
| 远端部署 | 支持：SSH 上传、生成配置/证书/systemd unit、`systemctl enable --now`；远端只能经底座下发 | **不支持**插件远程部署；插件只运行在 DBX 所在机器（桌面或自托管 Web 服务器） |
| 运行状态 | 静态事实 / 运行状态（`installed`/`started`/`callable`/`unhealthy`/`unknown`…）/ 自报三层；漂移检测 | 文档描述为「已安装」与「连接是否打开」；未见三态、漂移、自报边界的公开说明 |
| 错误语义 | 统一错误类别枚举（`input_invalid`…`version_incompatible`，1.1 增 3 类）+ 命令式退出码 0~8 同桶 + 未登记类别归一并留原码 | JSON-RPC 2.0 错误；零散约定（如 `-32001` 无 UI、`-32602` 参数非法、`-32601` 方法不支持）；**未见统一错误分类体系** |
| 并发/超时 | 每个插件**一律串行**（跨能力亦串行），`concurrency` 仅承受能力声明；遵守 `deadline_ms` | 宿主支持并发在途请求与逐请求超时；sidecar 按插件共享；具体并发上限公开资料未说明 |
| 配置与密钥 | 清单 `config[]` 声明项，按**实例**填值，敏感项 AES-256-GCM 加密落库，注入 `0600` 配置文件 | 表单字段声明 + `binding`；secret 进 Secret Store，从连接 JSON 移除；云同步默认只含脱敏占位符，配置了同步口令才进加密载荷 |
| 插件间关系 | 禁止点对点依赖与共享进程空间（INV-7）；`invoke_plugin` v1 恒拒绝 | 公开资料未说明是否存在插件互调/依赖机制 |
| 交互式输入 | **禁止** TTY/交互；初始化与能力都必须无交互可重复执行 | 允许 sidecar 经 `host/requestUserInput` 向用户弹阻塞对话框（验证码、主机密钥确认等），Host API 1.1 起 |

## 4. 关键差异及成因

### 差异 1：安全边界放在完全不同的位置

DBX 把信任建立在「**仓库签名 + 人工审核 + UI 沙箱**」上，并明确承认「原生代码不在 OS 沙箱里、文件与网络无法完全中介」。我们把信任建立在「**默认拒绝 + 能力级授权 + 底座托管与证书**」上，且规定插件自报不得用于自证健康或扩权。

原因是产品形态不同：DBX 是**单用户桌面/自托管客户端**，插件由用户自己安装，风险模型接近「安装一个浏览器扩展/桌面小工具」；本平台是**多主机运维底座**，插件跑在被纳管的机器上，底座必须假定主机可能被入侵，因此事实来源、状态与授权只能由底座掌握（`docs/17` §3）。这也解释了为什么我们不敢把「签名=安全」写进设计，而 DBX 敢把这一点写成显式免责声明。

### 差异 2：清单兼容策略相反（忽略未知字段 vs 拒绝未知字段）

我们选择**忽略未知字段**，把它作为 MINOR 向前兼容的技术基础（`docs/05` §5）；DBX 选择 `additionalProperties: false`，靠 `manifest_version` 在形状变化时硬切，并在文档里提醒「新增贡献点类型会让旧宿主读不懂 manifest」。

原因在演进节奏：DBX 是**中心化应用市场**，宿主版本碎片可以通过审核与最低版本约束收敛，严格校验能更早暴露作者写错的键；本仓库是**单二进制离线交付、插件由不同团队长期维护**，宿主升级不可强制，忽略未知字段才能让新旧插件共存。两种选择都自洽，但混用会互相拆台。

### 差异 3：有没有「类型 / 实例」这层抽象

DBX 只有「已安装的插件」和「保存的连接」；我们强制拆成**类型**（包与清单事实）与**实例**（某主机上的一次安装），并要求调用、任务、授权都以实例为目标，另有静态事实/运行状态/自报三层与漂移检测（`docs/17`）。

原因是部署拓扑：DBX 插件只跑在 DBX 自己那台机器上，「装没装」几乎等价于「能不能用」；本平台同一插件会下发到几十上百台主机，版本、配置、证书、探测结果各不相同，没有实例层就无法表达「A 机器已下发但没启动、B 机器证书异常」。

### 差异 4：扩展面宽度不同（执行型 vs 体验型）

DBX 把**工作台 UI、文件系统、结果视图、右键菜单、MCP 工具面**都做成贡献点，宿主还提供通用的文件管理器/结果渲染器给插件复用；本仓库 v1 只有 EP-01/EP-02 两类执行扩展点，EP-05（前端界面）、EP-06（事件订阅）仍是 `idea`。

原因是两者的「插件价值」定义不同：DBX 的插件要让用户**在界面里多出一个可用的工作区**（S3 浏览器、K8s 管理器），UI 扩展是主产品；本平台的插件是**被调度、被审计的能力提供方**，界面由底座统一渲染，优先保证契约可校验、可授权、可回放，而不是给插件开前端口子。

### 差异 5：分发治理成熟度差距

DBX 已有真实的应用市场：官方 catalog（Git 托管 JSON，天然支持离线镜像）、第三方/私有仓库、不可变版本、回滚、吊销、仓库密钥轮换、审核记录。本仓库 v1 的完整性强度仍挂在 OQ-015，升级/撤销策略挂在 OQ-051，包靠人工与 SSH 传输。

原因是阶段不同：DBX 面对公网开发者生态，必须有供应链治理；本平台当前插件由内部/受控团队提供，先把「下发—托管—探测—调用」闭环做扎实，市场属于后续能力。这也意味着**OQ-015 不应该继续悬空**——一旦要开放第三方插件，签名与目录就是前置条件。

### 差异 6：交互模型（反向调用与常驻复用）

DBX 的 sidecar 是**按插件共享的常驻进程**，支持并发在途请求，还允许插件**反向调用宿主**（`host/requestUserInput`）来索取验证码/确认；我们命令式是「一调用一进程」，常驻插件**一律串行**，且契约明确禁止 TTY 与交互式输入。

原因是运行环境：DBX 的插件在用户面前运行，弹窗是合理交互；本平台的插件大多在无人值守的目标主机上被定时任务或批量操作触发，任何交互都会变成挂起或超时，因此「确认」被固化成 `confirm_partition` / `confirm: true` 这类**字段**而不是对话（ADR-0021）。反过来，DBX 的「常驻复用 + 并发」也提醒我们：串行调度是**安全与可审计的取舍**，代价是吞吐，需要在 OQ-017 的量化目标里正视。

## 5. 值得借鉴 / 不该照搬

### 值得借鉴

1. **仓库级签名 + 不可变版本 + 回滚 + 吊销**这一整套最小供应链治理，正好可以填 OQ-015/OQ-051 的空白；尤其「catalog 只存元数据、不存二进制」对单二进制离线交付很友好。
2. **签名与审核职责分离**的表述：SHA-256 证明「下载的就是目录选中的字节」，签名证明「该仓库批准并发布了这份字节」，人工审核决定「能不能进目录」，三者互不替代（README · Review and signatures have different jobs）。这套话术可以直接用于我们的威胁建模。
3. **权限码原样展示 + 安装时授予**：把 `host.data:read` 这类细粒度权限做成可读字符串并在安装界面逐条展示，比只给一个「是否信任」的开关更能约束插件作者。
4. **Host API 版本 + 运行时 capabilities 双闸**：清单写兼容下限、运行时再读 `hostApiVersion`/`features` 判断能力是否存在，避免「装了但用不了」。我们的契约只有清单范围校验，可考虑在 `GetVersion` 之外补一个能力探测。
5. **升级导致权限扩大时重新确认**：DBX 规定插件更新后，新增或提权的工具在用户确认前保持隐藏（README · mcp 一节）。我们的插件升级目前只校验配置迁移，权限变化缺少「重新授权」环节（可挂到 OQ-050/OQ-051）。

### 不该照搬

1. **拒绝未声明字段**：会直接破坏我们「忽略未知字段 = MINOR 兼容」的地基，且离线部署下宿主无法强制收敛版本。
2. **「原生代码不做沙箱，签名即信任」的定位**：我们面对的是被管主机与多租户运维场景，不能把供应链签名当作运行时隔离；底座托管、证书绑定、能力级授权必须保留。
3. **取消类型/实例分离**：单机产品可以省略，多主机下发一旦省略就无法表达漂移与三态。
4. **交互式输入通道**：与「禁止 TTY、必须无交互可重复执行」的契约冲突，远端无人值守场景会直接挂死。
5. **把官方仓库 URL 写死为唯一来源**：我们的部署常在隔离网络，必须允许离线目录/镜像，而不是依赖一个公网 raw URL。
6. **UI 扩展点全面铺开**：EP-05 一旦开放，就要同时解决 CSP、桥接载荷、插件身份绑定与前端审计，成本远超当前收益；DBX 为此投入了沙箱 iframe、Host API、UI kit 与主题一整套设施。

## 6. 信息缺口

- **官方文档中英文不一致**：`host.network:` origin 上限英文页写「at most 64」，中文页写「最多 8」；以哪版为准公开资料未说明，本文不取单一数值作为事实。
- **插件间通信**：未发现「插件 A 调用插件 B」或插件依赖声明机制的任何说明；`contributions` 只描述插件→宿主的贡献，跨插件编排是否支持**公开资料未说明**。
- **统一错误分类**：只有 JSON-RPC 标准错误码与零散业务码示例，未见类似我们 `input_invalid`/`precondition_failed` 的枚举与归类规则；错误是否参与审计口径**公开资料未说明**。
- **并发与资源限额**：宿主持有并发在途请求，但单插件并发上限、CPU/内存配额、进程数限制**公开资料未说明**。
- **私有仓库鉴权**：只说凭据必须走 secret-store 支撑的请求层，具体支持哪些认证方式（Token/双向 TLS/离线介质）**公开资料未说明**。
- **团队/多租户授权**：DBX 是单用户桌面或自托管 Web，未见「谁能安装/启用某插件」的角色模型；企业策略能力**公开资料未说明**。
- **更新策略细节**：只说明版本不可变与支持回滚，是否自动更新、失败如何回滚、灰度机制**公开资料未说明**。
- **审核与废止 SLA**：官方目录的审核时限、插件下架/废止后的通知与宽限期**公开资料未说明**。
- **Windows/Linux 权限差异**：只说 sidecar 以当前 OS 用户权限运行，是否在特定平台做额外限制**公开资料未说明**。
- **运行时状态与漂移**：DBX 是否检测「已安装但二进制被替换/版本不符」**公开资料未说明**（其不可变安装目录只是间接防护）。

## 7. 参考来源

- DBX 产品与文档：[dbxio.com](https://dbxio.com/cn)、[What is DBX?](https://dbxio.com/en/docs/what-is-dbx)、[插件开发文档（英）](https://dbxio.com/en/docs/plugin-development)、[插件开发文档（中）](https://dbxio.com/cn/docs/plugin-development)、[JDBC 插件与旧协议](https://dbxio.com/en/docs/plugins)、[MCP 集成](https://dbxio.com/en/docs/mcp)、[Plugin Center](https://dbxio.com/en/plugins)
- DBX 插件详情示例：[Dev Tools（纯前端）](https://dbxio.com/en/plugins/terry.devtools)、[S3 Browser（原生 sidecar、分平台包）](https://dbxio.com/en/plugins/io.github.t8y2.s3)
- DBX 仓库：[plugins/README.md](https://github.com/t8y2/dbx/blob/main/plugins/README.md)、[SIGNING.md](https://github.com/t8y2/dbx/blob/main/plugins/SIGNING.md)、[RELEASING.md](https://github.com/t8y2/dbx/blob/main/plugins/RELEASING.md)、[manifest.schema.json](https://github.com/t8y2/dbx/blob/main/plugins/manifest.schema.json)
- 本仓库：[docs/03-plugin-model.md](../../../docs/03-plugin-model.md)、[docs/05-extension-points-and-compatibility.md](../../../docs/05-extension-points-and-compatibility.md)、[docs/17-plugin-registry-and-configuration.md](../../../docs/17-plugin-registry-and-configuration.md)、[plugin-spec/01-manifest.md](../../../plugin-spec/01-manifest.md)、[plugin-spec/03-remote-protocol.md](../../../plugin-spec/03-remote-protocol.md)、[plugin-spec/05-errors-and-exit-codes.md](../../../plugin-spec/05-errors-and-exit-codes.md)
