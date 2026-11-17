# 19 使用与配置手册

> 状态：`draft` ｜ 读者：首次部署者、日常运维 ｜ 上游：[DOC-11](11-implementation-baseline-m0.md)、[DOC-13](13-m2-identity-and-authz.md)、[DOC-14](14-host-management-m3.md)、[DOC-15](15-m4-scheduled-tasks.md)、[DOC-18](18-m6-remote-plugins-and-instances.md)

本文只回答「怎么部署、怎么用、撞到问题怎么办」。配置键的完整实现背景见 DOC-11、DOC-13~18；插件契约见 [plugin-spec/README.md](../plugin-spec/README.md)。

## 1. 阅读方式

- 第一次部署：按 §2 → §3.1 → §3.2 顺序做。
- 日常使用：直接看 §3.3~§3.9。
- 出问题：先看 §4 的排查顺序，再回到对应章节核对配置。

## 2. 部署与启动

### 2.1 前置条件

- Go 1.26+（仅从源码构建时需要；使用预编译二进制可忽略）。
- 一个可写的本地数据目录；SQLite 由二进制内置，无需单独安装数据库。
- 若使用远端 daemon 插件：目标主机为 Linux + systemd，且允许 SSH 登录。

### 2.2 最小配置文件

配置文件是 YAML，必须通过 `-config <path>` 显式指定。未知键会直接导致启动失败（严格模式），因此不要写本文没有列出的键。

```yaml
# config.yaml
data_dir: C:/ops-platform-data
listen_addr: 127.0.0.1:8080

# 32 字节随机值的 base64；生成方法见 §2.6
credential_key: "REPLACE_WITH_BASE64_OF_32_RANDOM_BYTES"

# 仅在「库中没有任何用户」时用于创建内置管理员
admin_password: "REPLACE_WITH_INITIAL_ADMIN_PASSWORD"
```

Linux 示例：

```yaml
data_dir: /var/lib/ops-platform
listen_addr: 0.0.0.0:8080
credential_key: "REPLACE_WITH_BASE64_OF_32_RANDOM_BYTES"
admin_password: "REPLACE_WITH_INITIAL_ADMIN_PASSWORD"
```

说明：

- `data_dir` 与 `listen_addr` 必填；缺失时启动失败并提示对应待定项（OQ-022、OQ-036）。
- `admin_password` 只在首次启动、用户表为空时生效；已有用户后再改这个键不会改密码。
- `credential_key` 缺失不会阻断启动，但主机凭据、内置 CA、证书与远端实例不可用（见 §2.7）。

### 2.3 启动命令

```powershell
.\bin\operations-platform.exe -config .\config.yaml
```

等价的环境变量方式：

```powershell
$env:OPS_PLATFORM_DATA_DIR = "C:\ops-platform-data"
$env:OPS_PLATFORM_LISTEN_ADDR = "127.0.0.1:8080"
.\bin\operations-platform.exe
```

启动成功后访问 `http://127.0.0.1:8080/`。健康检查与版本：

```powershell
curl.exe http://127.0.0.1:8080/healthz
curl.exe http://127.0.0.1:8080/version
```

### 2.4 配置优先级

**命令行 > 环境变量 > 配置文件 > 内置默认值**。

同名配置只取最高优先级来源；配置文件中的键不会覆盖命令行显式给出的值。注意 `-plugin-dir` 等布尔/时长参数以命令行显式出现为准。

### 2.5 配置项全集

#### 2.5.1 基础与运行

| YAML 键 | 环境变量 | 命令行 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `data_dir` | `OPS_PLATFORM_DATA_DIR` | `-data-dir` | 无（必填） | 绝对路径；相对路径会被拒绝 |
| `listen_addr` | `OPS_PLATFORM_LISTEN_ADDR` | `-listen` | 无（必填） | `host:port` |
| `log_level` | `OPS_PLATFORM_LOG_LEVEL` | `-log-level` | `info` | `debug`/`info`/`warn`/`error` |
| `shutdown_timeout` | `OPS_PLATFORM_SHUTDOWN_TIMEOUT` | `-shutdown-timeout` | `10s` | 优雅关闭等待上限 |

#### 2.5.2 插件运行时

| YAML 键 | 环境变量 | 命令行 | 默认值 |
| --- | --- | --- | --- |
| `plugin_dir` | `OPS_PLATFORM_PLUGIN_DIR` | `-plugin-dir` | `<data_dir>/plugins` |
| `plugin_hot_reload` | `OPS_PLATFORM_PLUGIN_HOT_RELOAD` | `-plugin-hot-reload` | `false` |
| `plugin_scan_interval` | `OPS_PLATFORM_PLUGIN_SCAN_INTERVAL` | `-plugin-scan-interval` | `30s` |
| `plugin_invoke_timeout` | `OPS_PLATFORM_PLUGIN_INVOKE_TIMEOUT` | `-plugin-invoke-timeout` | `30s` |
| `plugin_max_result_bytes` | `OPS_PLATFORM_PLUGIN_MAX_RESULT_BYTES` | `-plugin-max-result-bytes` | `8388608`（8 MiB） |
| `plugin_max_concurrency` | `OPS_PLATFORM_PLUGIN_MAX_CONCURRENCY` | `-plugin-max-concurrency` | `20` |
| `execution_queue_batch` | `OPS_PLATFORM_EXECUTION_QUEUE_BATCH` | 无 | `50` |
| `execution_queue_interval` | `OPS_PLATFORM_EXECUTION_QUEUE_INTERVAL` | 无 | `200ms` |

`plugin_dir` 允许相对路径，但必须落在 `data_dir` 内；越界会被拒绝。

#### 2.5.3 身份与会话

| YAML 键 | 环境变量 | 命令行 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `session_ttl` | `OPS_PLATFORM_SESSION_TTL` | 无 | `12h` | 会话有效期 |
| `login_max_attempts` | `OPS_PLATFORM_LOGIN_MAX_ATTEMPTS` | 无 | `5` | 连续失败上限 |
| `login_lockout` | `OPS_PLATFORM_LOGIN_LOCKOUT` | 无 | `5m` | 锁定时长 |
| `admin_password` | `OPS_PLATFORM_ADMIN_PASSWORD` | 无 | 空 | 仅首次创建管理员时使用 |
| `credential_key` | `OPS_PLATFORM_CREDENTIAL_KEY` | `-credential-key` | 空 | base64(32 字节)；不推荐用命令行传 |

#### 2.5.4 主机与下发

| YAML 键 | 环境变量 | 默认值 |
| --- | --- | --- |
| `ssh_connect_timeout` | `OPS_PLATFORM_SSH_CONNECT_TIMEOUT` | `10s` |
| `ssh_upload_timeout` | `OPS_PLATFORM_SSH_UPLOAD_TIMEOUT` | `5m` |
| `distribution_max_concurrency` | `OPS_PLATFORM_DISTRIBUTION_MAX_CONCURRENCY` | `4` |
| `distribution_remote_root` | `OPS_PLATFORM_DISTRIBUTION_REMOTE_ROOT` | `/opt/ops-platform/plugins` |

#### 2.5.5 定时任务

| YAML 键 | 环境变量 | 默认值 |
| --- | --- | --- |
| `task_scan_interval` | `OPS_PLATFORM_TASK_SCAN_INTERVAL` | `30s` |
| `task_min_interval` | `OPS_PLATFORM_TASK_MIN_INTERVAL` | `1m` |
| `task_default_timeout` | `OPS_PLATFORM_TASK_DEFAULT_TIMEOUT` | `5m` |
| `task_default_retries` | `OPS_PLATFORM_TASK_DEFAULT_RETRIES` | `1` |

#### 2.5.6 内置 CA、远端探测与远端产物

| YAML 键 | 环境变量 | 默认值 | 关联 OQ |
| --- | --- | --- | --- |
| `pki_base_id` | `OPS_PLATFORM_PKI_BASE_ID` | `base-01` | OQ-047 |
| `pki_ca_validity` | `OPS_PLATFORM_PKI_CA_VALIDITY` | `87600h` | OQ-047 |
| `pki_leaf_validity` | `OPS_PLATFORM_PKI_LEAF_VALIDITY` | `8760h` | OQ-047 |
| `plugin_probe_interval` | `OPS_PLATFORM_PLUGIN_PROBE_INTERVAL` | `1m` | OQ-049 |
| `plugin_state_ttl` | `OPS_PLATFORM_PLUGIN_STATE_TTL` | `5m` | OQ-049 |
| `remote_dial_timeout` | `OPS_PLATFORM_REMOTE_DIAL_TIMEOUT` | `5s` | OQ-048 |
| `remote_ready_timeout` | `OPS_PLATFORM_REMOTE_READY_TIMEOUT` | `3s` | OQ-048 |
| `distribution_config_root` | `OPS_PLATFORM_DISTRIBUTION_CONFIG_ROOT` | `/etc/ops-platform/plugins` | OQ-048 |
| `distribution_data_root` | `OPS_PLATFORM_DISTRIBUTION_DATA_ROOT` | `/var/lib/ops-platform/plugins` | OQ-048 |
| `distribution_unit_dir` | `OPS_PLATFORM_DISTRIBUTION_UNIT_DIR` | `/etc/systemd/system` | OQ-048 |
| `distribution_service_user` | `OPS_PLATFORM_DISTRIBUTION_SERVICE_USER` | `root` | OQ-048 |
| `distribution_start_timeout` | `OPS_PLATFORM_DISTRIBUTION_START_TIMEOUT` | `60s` | OQ-048 |

这些数值是可配置的实现基线，不是已定稿的产品阈值；调整前先看对应 OQ。

### 2.6 数据目录与主密钥

启动时会自检并创建以下目录（权限 `0700`）：

```text
<data_dir>/
├── db/              # platform.db 等 SQLite 文件
├── backups/         # 备份（当前阶段预留）
├── plugins/         # 本机插件包，默认 plugin_dir
├── plugin-data/     # 插件实例数据
└── tmp/             # 临时文件
```

`platform.db` 位于 `db/platform.db`。

生成 `credential_key`（32 字节随机值的 base64）：

```powershell
$b = New-Object byte[] 32
[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
[Convert]::ToBase64String($b)
```

```bash
head -c 32 /dev/urandom | base64
```

把输出填入配置文件：

```yaml
credential_key: "把生成的 base64 原样粘贴到这里"
```

主密钥规则（与代码一致）：

- 值必须是 **base64 解码后恰好 32 字节**；长度不符时启动失败。
- 只放在配置文件或环境变量中；命令行 `-credential-key` 会出现在进程列表，不建议使用。
- 缺失时底座**仍然启动**，只打一条 Warn；主机凭据读写、内置 CA、证书与远端实例能力不可用，相关操作返回 `credential_key_missing`。
- 丢失或更换主密钥：已保存的主机凭据与 CA 私钥无法解密；轮换与备份策略尚未定稿（OQ-023、OQ-047）。不要在没有备份的情况下更换。

### 2.7 启动自检顺序

固定为「数据目录 → 存储 → HTTP」，任一步失败都不带病启动。插件目录不可读只告警，不阻断启动。

## 3. 首次使用

以下示例统一使用 `curl.exe`（Windows PowerShell）或 `curl`（Linux）。除登录与 GET 外，写操作都要带 `X-Ops-Request: 1`。

先保存会话 Cookie：

```powershell
curl.exe -c cookies.txt -H "Content-Type: application/json" `
  -d '{\"username\":\"admin\",\"password\":\"REPLACE_WITH_INITIAL_ADMIN_PASSWORD\"}' `
  http://127.0.0.1:8080/api/auth/login
```

### 3.1 管理员引导与登录

- 启动时若用户表为空且配置了 `admin_password`（或 `OPS_PLATFORM_ADMIN_PASSWORD`），自动创建 `admin`。
- 未配置初始口令：只告警、不创建账号，需要补配置后重启。
- 口令策略只要求非空且不超过 1024 字节；复杂度策略未定（OQ-040）。
- 连续失败达到 `login_max_attempts` 后锁定 `login_lockout`。

查询当前会话：

```powershell
curl.exe -b cookies.txt http://127.0.0.1:8080/api/auth/session
```

退出：

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/auth/logout
```

### 3.2 插件发现与启用

把插件包放到插件目录，每个插件一个子目录，清单位于 `<plugin_dir>/<plugin-id>/plugin.yaml`：

```text
C:\ops-platform-data\plugins\
└── demo-echo\
    ├── plugin.yaml
    └── bin\
        └── demo-echo.exe
```

触发扫描并查看结果：

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/plugins/scan

curl.exe -b cookies.txt http://127.0.0.1:8080/api/plugins
curl.exe -b cookies.txt http://127.0.0.1:8080/api/plugin-instances
```

新发现的插件默认**禁用**。本机实例初始为 `installed=true`、`started=false`、`callable=false`、`enabled=false`，需要显式启用：

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/plugin-instances/INSTANCE_ID/enable
```

类型级启用（等价于对类型下实例启用）：

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/plugins/PLUGIN_ID/enable
```

插件契约与清单字段见 [plugin-spec/README.md](../plugin-spec/README.md)；本地骨架可直接复制 `examples/starter/command`。

### 3.3 添加主机

主机字段：`name`、`address`（不能带 `http://` 等协议前缀）、`port`（1~65535，**没有默认值**）、`username`、`auth_method`（`key` 或 `password`）、`credential`。

SSH 密钥方式：

```powershell
curl.exe -b cookies.txt -H "Content-Type: application/json" -H "X-Ops-Request: 1" `
  -d '{\"name\":\"web-01\",\"address\":\"10.0.0.11\",\"port\":22,\"username\":\"ops\",\"auth_method\":\"key\",\"credential\":{\"method\":\"key\",\"private_key\":\"-----BEGIN OPENSSH PRIVATE KEY-----\\n...\\n-----END OPENSSH PRIVATE KEY-----\"}}' `
  http://127.0.0.1:8080/api/hosts
```

账号密码方式：

```powershell
curl.exe -b cookies.txt -H "Content-Type: application/json" -H "X-Ops-Request: 1" `
  -d '{\"name\":\"web-02\",\"address\":\"10.0.0.12\",\"port\":22,\"username\":\"ops\",\"auth_method\":\"password\",\"credential\":{\"method\":\"password\",\"password\":\"REPLACE_WITH_PASSWORD\"}}' `
  http://127.0.0.1:8080/api/hosts
```

注意：

- 新增主机必须提供凭据；只编辑其他字段时可省略 `credential`，表示沿用旧凭据。
- 凭据加密依赖 `credential_key`；未配置时接口返回 `credential_key_missing`。
- 响应不会返回凭据明文，只返回 `credential_configured`。

### 3.4 测试连接

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/hosts/HOST_ID/test
```

- 首次连接采用 TOFU：成功后将主机指纹锁存，`host_key_trusted=true`。
- 指纹与已锁存值不一致时状态为 `fingerprint_mismatch`，需人工确认后处理，不会自动覆盖。
- 其他状态：`unknown`、`reachable`、`unreachable`。

### 3.5 下发插件

只支持 `form: daemon` 的插件；命令式插件只能本机运行。远端 daemon 也**只能通过底座下发**，不要手工在目标机安装或注册 systemd unit。

```powershell
curl.exe -b cookies.txt -H "Content-Type: application/json" -H "X-Ops-Request: 1" `
  -d '{\"plugin_id\":\"demo-daemon\",\"plugin_version\":\"1.0.0\"}' `
  http://127.0.0.1:8080/api/hosts/HOST_ID/distributions
```

可选 `port` 覆盖清单里的 `runtime.listen.port`；不传则用清单值。

查看进度与结果：

```powershell
curl.exe -b cookies.txt http://127.0.0.1:8080/api/hosts/HOST_ID/distributions
curl.exe -b cookies.txt http://127.0.0.1:8080/api/distributions/DISTRIBUTION_ID
```

阶段含义：

| 阶段 | 含义 |
| --- | --- |
| `connect` | SSH 连接 |
| `upload` | 上传插件包 |
| `verify` | 校验 sha256 |
| `install` | 解包并校验清单摘要 |
| `configure` | 写配置、证书、systemd unit |
| `start` | `daemon-reload` + `enable --now` |
| `probe` | 等待并验证服务可调用 |

失败后可用重试或撤销：

```powershell
curl.exe -b cookies.txt -X POST -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/distributions/DISTRIBUTION_ID/retry

curl.exe -b cookies.txt -X DELETE -H "X-Ops-Request: 1" `
  http://127.0.0.1:8080/api/distributions/DISTRIBUTION_ID
```

### 3.6 三态含义

| 状态 | 含义 | 判定依据 |
| --- | --- | --- |
| 已安装 `installed` | 包与入口存在 | 本机检查包目录与入口文件；远端检查安装目录与版本 |
| 已启动 `started` | 远端为服务在运行；本机命令式启用即视为已启动 | 远端 `systemctl is-active` 与 `Ready`；本机按启用状态推导 |
| 可调用 `callable` | 技术可用性（不等同于业务启用） | 已安装 + 无漂移 + 探测通过；本机实例启用时直接置为可调用。真正发起调用还要求 `enabled=true` |

`enabled` 是业务开关，与 `callable` 是两层：技术可用不代表已启用。实例的 `state` 还可能出现 `disabled`、`drifted`、`start_failed`、`unknown`、`revoked`。

### 3.7 定时任务

新建任务：

```powershell
curl.exe -b cookies.txt -H "Content-Type: application/json" -H "X-Ops-Request: 1" `
  -d '{\"name\":\"每分钟巡检\",\"instance_id\":\"INSTANCE_ID\",\"capability_id\":\"echo\",\"input\":{\"message\":\"ping\"},\"trigger_type\":\"cron\",\"cron_expr\":\"*/5 * * * *\",\"timezone\":\"Asia/Shanghai\",\"timeout_seconds\":300,\"retries\":1,\"state\":\"enabled\"}' `
  http://127.0.0.1:8080/api/tasks
```

- `trigger_type` 支持 `cron`（5 段，分钟级）与 `interval`（`interval_seconds`）。
- 最小间隔由 `task_min_interval` 限制，默认 60 秒。
- 未填 `timeout_seconds` / `retries` 时分别用 `task_default_timeout`、`task_default_retries`。
- 底座重启后，已错过的触发只记 `missed`，**不补跑**（DOC-15）。
- 手动运行：`POST /api/tasks/{id}/run`；暂停：`POST /api/tasks/{id}/pause`。

### 3.8 执行记录与审计

```powershell
curl.exe -b cookies.txt "http://127.0.0.1:8080/api/executions?limit=50"
curl.exe -b cookies.txt "http://127.0.0.1:8080/api/audit-events?limit=50"
curl.exe -b cookies.txt http://127.0.0.1:8080/api/tasks/TASK_ID/executions
```

- 执行记录保留输入/输出摘要（各 ≤2048 字节，超出截断），不保留完整内容（OQ-046）。
- 审计事件覆盖登录、插件启停/调用、主机与凭据操作、下发、任务增删改等。

### 3.9 凭据管理

- 凭据只写不读：接口不会返回私钥或密码，只返回 `credential_configured`。
- 更换凭据：`PUT /api/hosts/{id}` 并带上新的 `credential`；不带则保持原值。
- 删除主机：`DELETE /api/hosts/{id}`。删除后凭据密文随记录处理，具体保留期未定（OQ-051）。
- 主密钥丢失后无法恢复已存凭据，只能重新录入。

## 4. 常见问题

### 4.1 `credential_key_missing`

现象：底座能启动，但添加/读取主机凭据、测试连接、下发或远端实例操作失败。

排查顺序：

1. 启动日志是否有「credential_key 未配置」Warn。
2. 配置是否通过 `-config` 加载，键名是否为 `credential_key`（不是 `credential-key`）。
3. base64 解码后是否恰好 32 字节。
4. 是否只放在环境变量里但进程环境没带上。

修复：补上 `credential_key` 后重启。已丢失旧密钥时，旧凭据无法解密，需要重新录入。

### 4.2 插件包缺失 / 版本漂移

| `drift` | 含义 | 处理 |
| --- | --- | --- |
| `package_missing` | 包目录或入口不存在 | 恢复包目录后重新扫描/探测 |
| `version_mismatch` | 实际版本与登记版本不一致 | 用目标版本重新下发或替换包 |
| `unit_missing` | 远端 systemd unit 不存在 | 重新下发 |
| `cert_mismatch` | 证书与实例绑定不一致 | 重新下发以重签证书 |

本机可用 `POST /api/plugin-instances/{id}/probe` 主动探测。

### 4.3 `callable=false` 排查顺序

按以下顺序逐项排除：

1. `installed` 是否为 `true`；否则看 `drift`。
2. `drift` 是否为空；非空先按 §4.2 处理。
3. `enabled` 是否为 `true`；否则启用实例。
4. `last_probe_result` 是否为 `failed`，`last_probe_reason` 指向哪个阶段（`connect`/`tls`/`ready`/`version`）。
5. 探测结果是否已超过 `plugin_state_ttl` 被降为 `unknown`；重新探测。
6. 调用方角色是否有该实例/能力的权限；无权限返回 `permission_denied`。

调用前置判定顺序是：未安装 → 有漂移 → 未启用 → 不可调用，任一不满足都返回 `not_ready`。

### 4.4 会话过期

现象：任意接口返回 401，错误码 `session_invalid`。

处理：重新登录。会话有效期由 `session_ttl` 控制，默认 12 小时。

### 4.5 `csrf_rejected`

现象：非 GET 请求返回 403，错误码 `csrf_rejected`。

原因：既没有同源 `Origin`/`Referer`，也没有 `X-Ops-Request: 1`。

处理：脚本调用统一加 `-H "X-Ops-Request: 1"`；浏览器页面正常同源访问不会触发。

### 4.6 登录被锁定

连续失败达到 `login_max_attempts` 后锁定 `login_lockout`。等待锁定结束后重试，或用具备权限的账号处理。锁定与失败计数属于 DOC-13 的实现范围。

### 4.7 远端 daemon 起不来

按阶段看下发记录：

- `connect`：SSH 地址、端口、账号或凭据问题。
- `upload`/`verify`：磁盘空间、权限、包摘要不一致。
- `configure`：`distribution_config_root`、`distribution_data_root`、`distribution_unit_dir` 是否可写。
- `start`：目标机是否 systemd、`distribution_service_user` 是否有权限。
- `probe`：端口是否被占用、证书是否匹配、`remote_dial_timeout`/`remote_ready_timeout` 是否过短。

远端实例首次探测失败会停在 `start_failed`，但已安装/已启动事实会保留；修复后重新探测或重新下发。

## 5. 附录

### 5.1 常用接口速查

| 操作 | 方法与路径 |
| --- | --- |
| 登录 / 退出 / 会话 | `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/session` |
| 总览 | `GET /api/overview` |
| 插件类型 / 实例 | `GET /api/plugin-types`、`GET /api/plugin-instances` |
| 扫描 / 启用 / 禁用 | `POST /api/plugins/scan`、`POST /api/plugin-instances/{id}/enable`、`POST /api/plugin-instances/{id}/disable` |
| 探测 / 探测记录 | `POST /api/plugin-instances/{id}/probe`、`GET /api/plugin-instances/{id}/probes` |
| 调用能力 | `POST /api/plugin-instances/{id}/capabilities/{capability}/invoke` |
| 主机 | `GET/POST /api/hosts`、`GET/PUT/DELETE /api/hosts/{id}` |
| 测试连接 | `POST /api/hosts/{id}/test` |
| 下发 | `POST/GET /api/hosts/{id}/distributions`、`GET /api/distributions/{id}` |
| 任务 | `GET/POST /api/tasks`、`PUT/DELETE /api/tasks/{id}` |
| 执行记录 / 审计 | `GET /api/executions`、`GET /api/audit-events` |

### 5.2 错误响应结构

```json
{
  "error": {
    "code": "credential_key_missing",
    "message": "可读说明",
    "details": {}
  }
}
```

### 5.3 本文涉及的待定项

| OQ | 内容 |
| --- | --- |
| OQ-022 | 数据目录缺失/不可写时的行为细节 |
| OQ-023 | 主密钥轮换与凭据重加密 |
| OQ-036 | 监听地址默认值与端口选择 |
| OQ-040 | 口令复杂度策略 |
| OQ-044 | 本机 daemon 托管数值与 Windows 支持 |
| OQ-046 | 执行记录摘要保留与脱敏 |
| OQ-047 | 证书轮换、CA 备份与算法切换 |
| OQ-048 | systemd 与远端启动细节 |
| OQ-049 | 探测与状态数值 |
| OQ-051 | 配置/数据保留期与升级迁移 |
