#!/usr/bin/env sh
# 用锁定的工具链版本从 sdk/plugin/v1/plugin.proto 重新生成 gRPC 代码。
#
# 锁定基线（ADR-0024；关闭 OQ-045）：
#   protoc             36.2.0   经 npm 包 "protoc" 分发，通过 npx 调用
#   protoc-gen-go      v1.36.6
#   protoc-gen-go-grpc v1.5.1
#
# 已知现象：`protoc --version` 输出 "libprotoc 36.2"，但生成文件头记录的是编译器
# 内部版本 "v7.36.2"；两者对应同一发布版，差异属预期，已在 ADR-0024 说明。
#
# 生成物入库（plugin-spec/03 §9）。脚本绝不静默安装或升级工具：
# 工具缺失或版本不符时输出精确修复指引并以非零码退出。

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

protoc_version=36.2.0
protoc_header_version=v7.36.2   # 生成文件头记录的版本串
gen_go_version=v1.36.6
gen_go_grpc_version=v1.5.1
proto=sdk/plugin/v1/plugin.proto

fail() {
    echo "gen-proto: FAIL" >&2
    echo "$1" >&2
    exit 1
}

# --- 1. protoc 经 npx（锁定版本；首次下载后进入 npx 缓存）---
if ! command -v npx >/dev/null 2>&1; then
    fail "npx 不可用。请安装 Node.js（自带 npx），或从 https://github.com/protocolbuffers/protobuf/releases 安装 protoc $protoc_version 后替换下方 npx 调用。"
fi

# --- 2. protoc 插件（Go 侧，锁定版本）---
gobin=$(go env GOBIN)
[ -n "$gobin" ] || gobin=$(go env GOPATH)/bin
gen_go="$gobin/protoc-gen-go"
gen_go_grpc="$gobin/protoc-gen-go-grpc"
[ -x "$gen_go" ] || fail "缺少 $gen_go
请精确安装：
  go install google.golang.org/protobuf/cmd/protoc-gen-go@$gen_go_version"
[ -x "$gen_go_grpc" ] || fail "缺少 $gen_go_grpc
请精确安装：
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$gen_go_grpc_version"

ver_go=$("$gen_go" --version)
case "$ver_go" in
    *"$gen_go_version"*) ;;
    *) fail "protoc-gen-go 版本不符：got '$ver_go'，expected $gen_go_version。禁止改用其他版本；升级流程见 ADR-0024。" ;;
esac
ver_go_grpc=$("$gen_go_grpc" --version)
case "$ver_go_grpc" in
    *"$gen_go_grpc_version"*) ;;
    *) fail "protoc-gen-go-grpc 版本不符：got '$ver_go_grpc'，expected $gen_go_grpc_version。禁止改用其他版本；升级流程见 ADR-0024。" ;;
esac

# --- 3. 生成（与入库产物字节一致；ADR-0024）---
# -I . 使描述符内保留源路径 sdk/plugin/v1/plugin.proto，与已入库的 plugin.pb.go 一致。
npx -y protoc@$protoc_version -I . \
    --plugin=protoc-gen-go="$gen_go" \
    --plugin=protoc-gen-go-grpc="$gen_go_grpc" \
    --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    "$proto"

# --- 4. 校验生成文件头与锁定版本一致 ---
for f in sdk/plugin/v1/plugin.pb.go sdk/plugin/v1/plugin_grpc.pb.go; do
    [ -f "$f" ] || fail "缺少预期产物：$f"
    head -n 12 "$f" | grep -q "$protoc_header_version" \
        || fail "$f 文件头未找到 protoc 版本 $protoc_header_version；protoc 包可能已变化，三件版本需同步更新（ADR-0024）。"
done

echo "gen-proto: ok (protoc $protoc_version / protoc-gen-go $gen_go_version / protoc-gen-go-grpc $gen_go_grpc_version)"
echo "generated: sdk/plugin/v1/plugin.pb.go, sdk/plugin/v1/plugin_grpc.pb.go（已入库；提交前请核对 diff）"
