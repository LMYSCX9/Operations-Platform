# starter-daemon：常驻插件起步模板（远端 daemon 形态）

整目录复制到一个新目录（建议目录名 = 插件 ID），即可获得一个符合 [plugin-spec/03](../../../plugin-spec/03-remote-protocol.md)
远端常驻契约的最小插件：由目标机 systemd 托管、只监听按主机配置中的地址与端口、强制 mTLS、串行处理调用。

## 目录内容

| 文件 | 作用 |
| --- | --- |
| `plugin.yaml` | 清单：`form: daemon`、`entry.launch: bin/starter-daemon`、默认端口 `29444`（基线示例，待 OQ-048）、实例配置 `label` |
| `main.go` | 参数解析、按主机配置加载、mTLS 服务端构造；改动的机会最少 |
| `service.go` | `PluginService` 实现 + 示例能力 `now`；改这里实现你的能力 |
| `gen/pluginv1/` | 入库生成物三件套（`plugin.proto` / `plugin.pb.go` / `plugin_grpc.pb.go`）+ 契约常量 `contract.go`，与底座 `sdk/plugin/v1/` 字节一致 |
| `schemas/now-*.json` | 能力输入/输出 JSON Schema |
| `selftest/` + `selftest.ps1` / `selftest.sh` | 本地端到端自测（无需底座与远端主机） |
| `go.mod` / `go.sum` | 独立模块（依赖 grpc / protobuf / yaml，版本与底座对齐） |

## 本地验证（无需底座）

```powershell
.\selftest.ps1     # Windows
```

```sh
./selftest.sh      # Linux / macOS
```

自测工具会：生成**一次性测试证书**（仅本地自测；生产证书一律由底座内置 CA 签发，插件不得自行生成或替换）、
写一份与底座下发形状一致的按主机配置（`deployment/listen/tls/data_dir/config`）、
以底座注入参数（`--ops-plugin-id/--ops-instance-id/--ops-config/--ops-data-dir/--ops-contract-version`）
拉起插件进程，然后走 mTLS 断言：Ready、GetVersion 契约版本、`Invoke` 输出携带实例配置 `label`
（配置作用域生效）、非法输入 → `input_invalid`、未知能力 → `capability_unknown`、`Shutdown` 后进程退出码 0。

## 复制后必改的位置

1. **插件 ID**：`plugin.yaml` 的 `identity.id` 与能力 ID（`<新插件 ID>.<能力名>`）、`service.go` 的 dispatch。
2. **入口名**：`entry.launch: bin/<新插件 ID>`，自测脚本与 README 中的产物名同步改。
3. **默认端口**：`runtime.listen.port`（生效值由底座按主机写入配置文件，可覆盖；端口范围待 OQ-048）。
4. **实例配置**：按 [plugin-spec/11](../../../plugin-spec/11-instance-and-config.md) 增删清单 `config[]`；
   配置值经按主机配置文件的 `config` 段到达插件（见 `s.now` 读取 `label` 的写法），同插件不同实例可以不同。
5. **能力实现**：替换 `s.now`；错误只用 `gen/pluginv1/contract.go` 已登记类别；保持串行（`invokeMu`）。
6. **`gen/pluginv1/`**：跟随底座升级重新复制（见下），**唯一允许的本地修改**是 `plugin.proto` 的
   `go_package`（改成你的模块路径）；字段名与编号不得改动。
7. **`go.mod`**：module 名改为你的仓库路径；依赖版本与底座保持一致。

## 生成物升级方式

底座契约演进后，从底座仓库重新复制 `sdk/plugin/v1/` 下四个文件到 `gen/pluginv1/`
（或用锁定工具链自行重新生成）：protoc `36.2.0`（npm 包分发）+ protoc-gen-go `v1.36.6` +
protoc-gen-go-grpc `v1.5.1`，三件版本必须同步更新；生成脚本与升级流程见底座 `scripts/gen-proto.*` 与 ADR-0024。

## 被底座发现与下发

1. **构建 Linux 产物**：远端 daemon 运行在 Linux 主机上（systemd 托管）。
   `GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/starter-daemon .`
2. **类型登记**：把整个插件目录放入底座 `plugin_dir`（默认 `<data_dir>/plugins/`，可用配置键
   `plugin_dir` 修改），底座目录扫描登记类型；daemon 形态托管方式为 remote，本机不产生可调用实例。
3. **下发**：控制台「主机」页对该插件发起下发（可选端口覆盖与实例配置）→ 底座自动完成：
   打包上传 → 安装校验 → 签发并写入证书/CA/按主机配置 → 生成 systemd unit →
   `daemon-reload && systemctl enable --now` → 三态探测（已安装/已启动/可调用）。
4. **调用**：「插件」页实例详情、「任务」页实例目标或实例调用接口；失败时看实例三态原因与探测历史。

## 契约红线（plugin-spec/03 §3.7）

- 只监听配置文件中的 `listen.address:port`；禁止明文 TCP、禁止关闭证书校验、禁止 `InsecureSkipVerify`（自测工具除外）。
- 证书/配置缺失时启动失败退出，不降级为明文；systemd 按 `Restart=on-failure` 处理。
- 私钥 `0600`、状态目录 `0700`；私钥不写日志、不进包、不进环境变量。
- 一律串行处理调用；`Ready`/`Health` 分离；`deadline_ms` 必须遵守。

## 关于本机 daemon 形态

契约同时定义了本机形态（UDS + 一次性凭据文件，`--ops-socket` 注入），但底座当前只实现了
**远端** daemon 托管（ADR-0018 规划、实现未落地）。本骨架按远端形态交付；待底座支持本机
spawn 后，需要在 `main.go` 增加 `--ops-socket` 分支（读凭据文件后立即删除）。
