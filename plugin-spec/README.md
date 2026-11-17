# 插件开发规范（模板目录）

> 状态：`draft` ｜ 契约版本：`1.2`（`MAJOR.MINOR`，见 [07](07-versioning-compatibility.md)）
> 本目录已从「语义描述」升级为**可执行契约**：清单字段集、stdio 报文、常驻插件启动注入与远程 Invoke 契约、错误码与退出码均为字面定稿内容。
> 遇到规范未覆盖的问题，先查 [10-faq.md](10-faq.md)；仍无结论必须停下提问并按 [AGENTS.md](AGENTS.md) §5 写回规范，不得私定。

本目录是**插件开发的起点**。整目录复制到一个新的插件仓库，即可获得：契约要求、通信协议、错误约定、日志约定、目录布局与自查清单。

## 1. 这是什么 / 不是什么

- 是：底座与插件之间的**接口契约说明**与**开发自查依据**。
- 不是：可运行代码、SDK 实现、底座内部实现文档。本目录**不含任何实现**。

## 2. 如何使用

```mermaid
flowchart LR
    A[复制 plugin-spec/ 到新插件仓库] --> B[确定插件形态与能力]
    B --> C[填写 manifest 模板]
    C --> D[实现对应通信协议]
    D --> E[按 00 清单自查]
    E --> F[交给底座安装校验]
```

步骤：

1. **整目录复制**到新插件仓库根目录（建议保留目录名 `plugin-spec/`，便于后续跟随底座升级）。
   - 替代路径：从**可运行骨架**起步——复制底座仓库的 `examples/starter/command`（命令式）
     或 `examples/starter/daemon`（远端 daemon），它们已含最小实现、清单模板与本地自测；
     复制后仍须按本目录逐项自查（[00](00-contract-checklist.md)）。
2. 通读 [README](README.md) → [01 元数据](01-manifest.md) → 对应形态的协议文档。
3. 按 [manifest.template.yaml](manifest.template.yaml) 填写 `plugin.yaml`（字段集见 [01](01-manifest.md) §2）。
4. 按 [02](02-stdio-protocol.md)（命令式）或 [03](03-remote-protocol.md)（常驻）实现通信。
5. 按 [04](04-io-conventions.md)、[05](05-errors-and-exit-codes.md)、[06](06-logging-observability.md)、[07](07-versioning-compatibility.md) 落实细节。
6. 一个包可能装在多台主机上：按 [11-instance-and-config.md](11-instance-and-config.md) 处理实例标识与 `config[]`，不得假设全局唯一。
7. 用 [00-contract-checklist.md](00-contract-checklist.md) 逐项自查。
8. 参考 [09-example-plugin-spec.md](09-example-plugin-spec.md) 的最小示例规格作为对照。
9. 遇到疑问先查 [10-faq.md](10-faq.md)（已定结论）；仍无答案则停下提问，不得私定。

### 2.1 实现状态（契约 1.2，以此为准）

| 项 | 当前状态 |
| --- | --- |
| 契约版本 | `1.2`；唯一 proto 来源 `sdk/plugin/v1/plugin.proto`，契约常量在 `sdk/plugin/v1/contract.go`，生成物 `plugin.pb.go` / `plugin_grpc.pb.go` 已入库；生成脚本 `scripts/gen-proto.ps1` / `scripts/gen-proto.sh` 锁定 protoc 36.2.0、protoc-gen-go v1.36.6、protoc-gen-go-grpc v1.5.1 |
| 本机 daemon 托管 | **尚未实现**（OQ-044）。契约形状已定，但底座当前不会在本机拉起/托管 daemon；**本机实例请使用命令式形态** |
| 远端 daemon | **只能经底座下发运行**：底座通过 SSH 上传安装包、生成配置/证书/systemd unit 并 `systemctl daemon-reload + enable --now`；**不支持也不得指导用户手工在目标机部署二进制或注册 unit** |
| 命令式插件远端下发 | 不支持。`internal/host/service.go` 明确报错：远端实例要求 `form: daemon` 与 `runtime.listen.port` |
| 命令式骨架入口 | `examples/starter/command`：`form: command`，`entry.executable: bin/<id>` |
| 远端 daemon 骨架入口 | `examples/starter/daemon`：`form: daemon`，`entry.launch: bin/<id>`，并声明 `runtime.listen.port` |
| 插件目录与清单 | 插件按 `<plugin_dir>/<plugin-id>/plugin.yaml` 组织；新发现插件默认**禁用** |
| 插件界面 | **一档声明式界面为 v1 方向**（ADR-0025 proposed），但字段、控件与展示形态未定；**二档自带前端包挂起，未生效**。[12](12-plugin-ui.md) 为草案，**不得据此实现** |

## 3. 阅读顺序

| 顺序 | 文件 | 什么时候读 |
| --- | --- | --- |
| 1 | [README.md](README.md) | 第一次接触本模板 |
| 2 | [01-manifest.md](01-manifest.md) | 开始设计插件元数据与能力 |
| 3 | [08-directory-layout.md](08-directory-layout.md) | 建仓库、定目录 |
| 4 | [02-stdio-protocol.md](02-stdio-protocol.md) | 做命令式插件 |
| 5 | [03-remote-protocol.md](03-remote-protocol.md) | 做常驻插件 |
| 6 | [04-io-conventions.md](04-io-conventions.md) | 定义输入输出结构 |
| 7 | [05-errors-and-exit-codes.md](05-errors-and-exit-codes.md) | 定义错误与退出码 |
| 8 | [06-logging-observability.md](06-logging-observability.md) | 接入日志 |
| 9 | [07-versioning-compatibility.md](07-versioning-compatibility.md) | 声明版本、处理兼容 |
| 10 | [10-faq.md](10-faq.md) | 有疑问、或第一次做某类能力时 |
| 11 | [11-instance-and-config.md](11-instance-and-config.md) | 同一插件多主机部署、需要用户配置项时 |
| 12 | [12-plugin-ui.md](12-plugin-ui.md) | 了解界面声明方向（**草案，未生效**，不要据此实现） |
| 13 | [00-contract-checklist.md](00-contract-checklist.md) | 提交前自查 |

## 4. 硬性要求（速览）

1. 只实现清单中**已声明**的能力；未声明的一律不做。
2. 一个插件只声明**一种形态**（命令式 或 常驻）。
3. 命令式插件的 **stdout 只放结果**，日志走 stderr。
4. 错误必须表达为**结构化错误类别**，不能只返回字符串。
5. 所有调用都可能被**超时终止**，插件必须能承受。
6. 不得依赖交互式输入（TTY），不得有 `setup` / `version` 能力。
7. 不得读取其他插件的数据目录，不得调用其他插件。
8. 日志中不得出现凭据、令牌、私钥；本机凭据只经一次性文件与 `context.credential` 传递，远端身份只由 mTLS 证书承担。
9. 必须声明**契约版本范围**与插件版本；用 1.1 新错误类别时下限 ≥ `1.1`，声明远端部署或 `config[]` 时下限 ≥ `1.2`。
10. 行为必须与能力声明**自洽**：声明了却没做、做了却没声明，都是违规。
11. 常驻插件：**本机**只监听 `--ops-socket`，路径从 `--ops-config`/`--ops-data-dir` 获取，不 daemonize；**远端**只监听 `--ops-config` 中的 `listen` 并校验客户端证书，unit 由底座下发。注意：本机 daemon 托管当前尚未实现（OQ-044），本机请用命令式；远端只能经底座下发运行。
12. 所有能力由底座**串行**调度，插件不得假设并发；同一插件可有多个实例，不得跨实例共享数据目录。
13. 高危操作（`delete`/`restore`/`gc`）必须标记 `risk_level: destructive` 并在副作用前校验确认字段。
14. 远端证书由底座签发，插件不得自行生成或替换；证书不代表授权，授权始终在底座。

## 5. 与底座规格的对应关系

| 本目录文件 | 底座规格来源 |
| --- | --- |
| 01 元数据与能力 | `docs/03-plugin-model.md` §3-§4、ADR-0021、ADR-0023 |
| 02 stdio 协议 | `docs/03` §5.1、ADR-0004、ADR-0023 |
| 03 远程协议 | `docs/03` §5.2、ADR-0005、ADR-0018、ADR-0020、ADR-0022 |
| 04 输入输出 | `docs/03` §5.3、ADR-0021 |
| 05 错误约定 | `docs/03` §5.3、ADR-0004、ADR-0019 |
| 07 版本兼容 | `docs/05-extension-points-and-compatibility.md`、ADR-0012 |
| 08 目录布局 | `docs/06-non-functional-requirements.md`、`docs/08`、ADR-0018、ADR-0022 |
| 10 FAQ | 上述全部规范的问答投影 |
| 11 实例与配置 | `docs/17-plugin-registry-and-configuration.md`、ADR-0023 |
| 12 插件界面（草案，未生效） | `docs/02` CAP-08、OQ-011、ADR-0025 |

**冲突处理**：若本目录与底座规格不一致，以底座 `docs/` 为准，并立即反馈修正本目录。

## 6. 模板维护规则

- 底座契约变更（新增字段、改变错误语义、调整协议）时，必须同步更新本目录。
- 本目录的版本号与底座契约版本保持一致，不单独演进。
- 复制出去的插件仓库不自动跟随更新；插件维护者需按底座发布说明手动比对。

## 7. 当前未定项（会直接影响插件实现）

| 主题 | 编号 | 影响 |
| --- | --- | --- |
| 结果体积上限、超时默认值、并发上限 | OQ-017 | 边界值无法写死，但边界**行为**已定（超限报 `result_too_large`） |
| 健康检查阈值、重启退避 | OQ-012 | 只影响常驻插件的运维参数，不影响契约形状 |
| 常驻插件启动/就绪/关闭/退避/凭据有效期默认值、Windows 支持 | OQ-044 | 参数可配置，契约形状已定；**本机 daemon 托管尚未实现**，本机请用命令式 |
| 字段命名风格、废弃支持周期、分页、错误堆栈是否返回 | OQ-038 | 局部风格与策略，不阻塞实现 |
| 人类身份源与 `scope` 强制校验 | OQ-037 | 只影响底座侧授权配置 |
| 日志/审计字段清单 | OQ-016 | 日志字段名可能调整 |
| 证书算法/有效期/轮换/CA 备份 | OQ-047 | 证书形状已定，数值未定；不得写死 |
| systemd unit 细节、远端启停与探测超时、端口范围 | OQ-048 | 形状已定，数值未定 |
| 探测间隔、状态 TTL、降级阈值 | OQ-049 | 只影响底座侧探测参数 |
| 实例/类型授权粒度 | OQ-050 | 判定落实例已定，模板优先级待定 |
| 升级配置迁移与撤销保留期 | OQ-051 | 默认保留配置，具体策略待定 |
| 插件界面：一档控件与展示边界；二档挂起 | OQ-011 | 一档为 v1 方向但字段未定；二档未生效，**不得实现** |

说明：**P0 六项（OQ-001~OQ-006）已全部关闭**；常驻插件的启动注入、错误类别扩展、工具链与高危确认也已定稿（底座 ADR-0018~0021）。
清单格式、stdio 报文、远程 Invoke、启动参数、错误码/退出码、版本声明均已可直接实现。
上表剩余项均为「数值/策略」级参数，不改变契约形状，可按默认值先行实现并留出配置位。
