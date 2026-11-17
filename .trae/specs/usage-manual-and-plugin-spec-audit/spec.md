# 规格：运维底座使用与配置手册 + 插件侧文档核对

> 状态：待用户确认（Spec Mode） ｜ 日期：2026-11-17
> 范围：只改文档，不改运行时代码。

## 1. 目标

1. 新增一份面向「第一次部署 + 日常使用」的中文手册，按代码实际实现核实，不重复 DOC-11~18 的实现细节。
2. 核对 `plugin-spec/` 全部文件与契约 1.2、底座实际实现是否一致；补齐缺失/过时表述，并保证整目录复制后不依赖 `docs/` 相对链接。

## 2. 已核实事实（写作依据）

### 2.1 配置与启动

- 优先级：命令行 > 环境变量 > 配置文件 > 内置默认值（`internal/config/config.go` 的 Load 顺序：文件 → env → flag.Visit）。
- 必填：`data_dir`（绝对路径）、`listen_addr`（host:port）；未配置分别提示 OQ-022、OQ-036。
- `plugin_dir` 默认 `<data_dir>/plugins`，相对路径按 `data_dir` 解析，且不得越出数据目录。
- 配置文件为 YAML，`KnownFields(true)`：未知键直接启动失败；需 `-config <path>` 指定。
- `credential_key`：YAML 键 `credential_key`；环境变量 `OPS_PLATFORM_CREDENTIAL_KEY`；命令行 `-credential-key`（存在，手册标注不推荐）。值必须 base64 解码后恰好 32 字节，否则启动失败。
- 缺 `credential_key` 时底座**正常启动**，仅 Warn：「credential_key 未配置：内置 CA、证书与远端实例能力不可用」；主机凭据写入/读取等操作返回 `credential_key_missing`。
- 丢失主密钥后果：已保存的主机凭据与 CA 私钥不可解密；轮换策略未定（OQ-023/OQ-047），手册只陈述事实，不给未定方案。
- 管理员引导：用户数为 0 且配置 `admin_password`（或 `OPS_PLATFORM_ADMIN_PASSWORD`）时创建 `admin`；未配置则告警且不创建。口令策略仅「非空且 ≤1024 字节」。
- 数据目录：`db/`、`backups/`、`plugins/`、`plugin-data/`、`tmp/`，权限 0700；数据库文件 `platform.db`。
- 会话 Cookie `ops_session`；非安全方法要求同源 Origin/Referer 或 `X-Ops-Request: 1`；统一错误结构 `{"error":{"code","message","details"}}`。

### 2.2 默认值（config.go）

| 键 | 默认值 |
| --- | --- |
| log_level | `info` |
| shutdown_timeout | `10s` |
| plugin_scan_interval | `30s` |
| plugin_invoke_timeout | `30s` |
| plugin_max_result_bytes | `8388608`（8 MiB） |
| plugin_max_concurrency | `20` |
| execution_queue_batch / interval | `50` / `200ms` |
| session_ttl | `12h` |
| login_max_attempts / login_lockout | `5` / `5m` |
| ssh_connect_timeout / ssh_upload_timeout | `10s` / `5m` |
| distribution_max_concurrency | `4` |
| distribution_remote_root | `/opt/ops-platform/plugins` |
| task_scan_interval | `30s` |
| task_min_interval | `1m` |
| task_default_timeout | `5m` |
| task_default_retries | `1` |
| pki_base_id | `base-01` |
| pki_ca_validity / pki_leaf_validity | `87600h` / `8760h` |
| plugin_probe_interval / plugin_state_ttl | `1m` / `5m` |
| remote_dial_timeout / remote_ready_timeout | `5s` / `3s` |
| distribution_config_root / data_root / unit_dir | `/etc/ops-platform/plugins` / `/var/lib/ops-platform/plugins` / `/etc/systemd/system` |
| distribution_service_user / start_timeout | `root` / `60s` |

命令行参数全集：`-config`、`-data-dir`、`-listen`、`-log-level`、`-shutdown-timeout`、`-plugin-dir`、`-plugin-hot-reload`、`-plugin-scan-interval`、`-plugin-invoke-timeout`、`-plugin-max-result-bytes`、`-plugin-max-concurrency`、`-credential-key`。

### 2.3 首次使用与主机

- 路由见 `internal/api/server.go`：登录/会话/审计、插件类型与实例、执行记录、主机与下发、定时任务。
- 主机输入字段：`name`、`address`（不得含 `://`）、`port`（1~65535，**无默认值**）、`username`、`auth_method`（`key` / `password`）、`credential`。新增必须带凭据；`key` 必须 `private_key`，`password` 必须 `password`。
- 连通性测试为 TOFU：首次连接锁存主机指纹；指纹变化/不匹配报 `fingerprint_mismatch`；主机状态 `unknown` / `reachable` / `unreachable` / `fingerprint_mismatch`。
- 下发请求体：`plugin_id`（必填）、`plugin_version`、`port`（可选，0 表示用清单端口）。
- 下发流程：connect → upload → verify（sha256）→ install（解包 + 清单摘要校验）→ 命令式结束于 `installed`；daemon 继续写配置/证书/unit → `systemctl daemon-reload && enable --now` → `started` → 探测通过 → `callable`，探测失败记 `start_failed`（保留已安装/已启动）。
- 命令式插件不支持远端下发（`host/service.go` 明确报错）；远端实例要求 `form: daemon` 且声明 `runtime.listen.port`。

### 2.4 三态、漂移与调用

- 实例初始（本机）：`installed=1`、`started=0`、`callable=0`、`enabled=0`。
- 本机探测只校验安装事实：包目录、入口文件（Windows 回退 `.exe`）、版本。
- 远端探测阶段：`connect → tls → ready → version`；漂移类型：`package_missing`、`version_mismatch`、`unit_missing`、`cert_mismatch`。
- 调用前置判定顺序（`invokeInstanceWithTask`）：未安装 → 有漂移 → 未启用 → 不可调用，均返回 `not_ready`；权限先判，拒绝为 `permission_denied`。
- 远端调用遇 `dependency_unavailable` / `timeout` 立即降级为 `unknown`、`callable=false`，避免界面继续显示可调用。
- 探测结果超过 `plugin_state_ttl` 且未启动未可调用时，`Sweep` 降为 `unknown`。

### 2.5 定时任务

- `trigger_type`：`cron`（5 段，分钟级，OQ-042）或 `interval`；`interval_seconds` ≥ `task_min_interval`（默认 60s）。
- 任务字段：`instance_id` / `plugin_id`、`capability_id`、`input`、`timezone`、`timeout_seconds`、`retries`、`state`。
- 启动时已过期的启用任务写 `missed` 记录并推进下次触发时间，**不补跑**。

### 2.6 插件侧实现状态（写入手册与 plugin-spec）

- 契约 1.2；proto 唯一来源 `sdk/plugin/v1/plugin.proto`；契约常量 `sdk/plugin/v1/contract.go`。
- 生成脚本 `scripts/gen-proto.ps1` / `.sh` 锁 protoc 36.2.0、protoc-gen-go v1.36.6、protoc-gen-go-grpc v1.5.1。
- 两套骨架入口：`examples/starter/command`（`form: command`，`entry.executable: bin/<id>`）与 `examples/starter/daemon`（`form: daemon`，`entry.launch: bin/<id>` + `runtime.listen.port`）。
- **本机 daemon 托管尚未实现**（docs/07 §4.11，OQ-044）；**远端 daemon 只能经底座下发运行**（SSH 安装 + 底座生成 systemd unit），不支持人工在目标机自行部署/注册服务。
- 命令式与 daemon 的错误语义差异：命令式「退出码 0~8 + 响应 code」且必须同桶；daemon 无退出码业务语义，只经 `InvokeResponse.error` 表达，崩溃折算见 plugin-spec/05 §3.1。

## 3. 交付物

### 3.1 新增 `docs/19-usage-and-configuration.md`

章节（以「怎么用」为主线，细节引用 DOC-11~18）：

1. 适用对象与阅读方式
2. 部署与启动：最小配置、配置项全集与默认值、命令行/环境变量/优先级、启动命令、数据目录结构、credential_key 生成与丢失后果
3. 首次使用：管理员引导、登录与 CSRF、插件发现与启用、添加主机（密钥/密码）、测试连接、下发插件、三态含义、定时任务、执行记录与审计、凭据管理
4. 常见问题：credential_key_missing、插件包缺失/漂移、callable=false 排查顺序、会话过期、CSRF 报错
5. 附录：可直接照抄的 curl 示例、配置片段、OQ 索引

要求：所有命令与配置可直接照抄；数值用真实默认值；未定项标 OQ；Mermaid 内嵌；不贴大段代码。

### 3.2 同批更新

- `docs/README.md`：清单新增 DOC-19；依赖图增加 19；文件清单增加该文件；当前基线提及使用手册。
- `.ai/index.md`：新增 DOC-19 条目与关键词。

### 3.3 `plugin-spec/` 修订（逐文件）

1. `README.md`
   - 增加「实现状态」提示：本机 daemon 托管未实现；远端 daemon 只能经下发运行。
   - 去掉 `../examples/...`、`../docs/...` 相对链接，改为纯文本路径说明，保持整目录可复制。
   - 阅读顺序表补充骨架入口与生成物位置。
2. `00-contract-checklist.md`
   - 增加「daemon 形态是否走底座下发 / 本机 daemon 是否可用」检查项。
   - 把 `docs/` 引用改为「底座规格（若可获取）」纯文本，避免复制后死链。
3. `01-manifest.md`
   - §3.6 补充：`runtime.listen.port` 只表示可远端部署；当前远端部署只能经底座下发；本机 daemon 托管未实现。
   - §4.1 保持；去掉 ADR 相对链接。
4. `02-stdio-protocol.md`
   - 无实现冲突；仅确认命令式仍可用。必要时把 `[05](...)` 保留（目录内链接允许）。
5. `03-remote-protocol.md`
   - 增加醒目的「实现状态」块：本机 daemon 托管未实现；远端 daemon 仅经下发运行；不得手工注册 unit。
   - 去掉 `../docs/adr/...` 链接，保留 ADR 编号纯文本。
   - §9 明确生成物与常量位置：`sdk/plugin/v1/plugin.proto`、`sdk/plugin/v1/contract.go`、`examples/starter/daemon/gen/pluginv1/`（纯文本）。
6. `04-io-conventions.md`、`05-errors-and-exit-codes.md`、`06-logging-observability.md`、`07-versioning-compatibility.md`
   - 去掉 `docs/` / `../docs` 死链；05 补充命令式 vs daemon 错误语义对照（若已有则只做指向）。
7. `08-directory-layout.md`
   - 本机/远端常驻约束增加实现状态注解；去掉 `docs/08...` 链接。
8. `09-example-plugin-spec.md`
   - 按需补充两套骨架入口示例；保持可复制。
9. `10-faq.md`
   - 修正「远端主机上的 daemon 怎么办」→ 只能经底座下发运行，不得手工部署。
   - 修正「本机常驻插件」条目 → 契约已定但底座托管未实现（OQ-044），当前请用命令式。
   - 去掉 `../docs/adr/...` 链接；§16 的 `docs/` 改为纯文本。
10. `11-instance-and-config.md`
    - 去掉 `../docs/adr/...`、`../docs/17...` 链接，保留编号纯文本。
11. `manifest.template.yaml`、`manifest.schema.json`
    - 核对 1.2 字段集（`runtime.listen.port`、`config[]`、`risk_level`）是否齐全；不新增未实现字段。
12. `AGENTS.md`
    - 保留；`docs/` 引用改为纯文本或说明为底座仓库路径。

## 4. 明确不做

- 不修改 `docs/18-m6-remote-plugins-and-instances.md:45` 的「缺少主密钥时启动失败」；在最终报告中列为待裁决。
- 不修改运行时代码、数据库、接口契约。
- 不新增未登记的配置项/字段。
- 不改 `docs/11`~`18` 的既有实现细节（除 README 索引与必要的引用）。

## 5. 验收

1. `docs/19-usage-and-configuration.md` 存在，章节完整，所有默认值与代码一致。
2. `docs/README.md`、`.ai/index.md` 已同步，链接可达。
3. `plugin-spec/` 内不存在 `../docs` 或 `../examples` 的 Markdown 链接；整目录复制后所有相对链接只在目录内。
4. 手册明确标出：缺 `credential_key` 不阻断启动；本机 daemon 未实现；远端 daemon 只能下发运行；`docs/18:45` 待裁决。
5. 最终报告列出手册路径、章节、plugin-spec 修补点、待裁决项。

## 6. 风险与待裁决

- `docs/18:45` 与代码懒校验不一致：本规格只记录，不修改。
- `identity.Bootstrap` 告警文案曾写 `bootstrap_admin_password`，实际配置键是 `admin_password`；已按用户确认改为真实键名（`admin_password / OPS_PLATFORM_ADMIN_PASSWORD`）。
- 本机 daemon 的契约细节（OQ-044）与远端数值（OQ-047~OQ-049）仍为未定项，手册/plugin-spec 只写形状与现有默认值。
