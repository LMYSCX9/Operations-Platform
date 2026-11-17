#!/usr/bin/env sh
# 构建单二进制。CGO 强制关闭，保证产物无 cgo 依赖（ADR-0007）。
#
# 用法：
#   ./scripts/build.sh
#   GOOS=linux GOARCH=amd64 OUTPUT=bin/linux ./scripts/build.sh
#   VERSION=v0.1.0 ./scripts/build.sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

# prototype/index.html 是页面唯一事实来源；go:embed 不能引用包目录之外的文件，
# 所以构建前把它同步到内嵌目录。缺少该文件时 go build 会直接失败（ADR-0001）。
cp prototype/index.html web/dist/index.html

version=${VERSION:-}
if [ -z "$version" ]; then
  version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
fi

commit=$(git rev-parse --short HEAD 2>/dev/null || echo none)
build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)

output_dir=${OUTPUT:-bin}
mkdir -p "$output_dir"

ext=""
if [ "${GOOS:-$(go env GOOS)}" = "windows" ]; then
  ext=".exe"
fi

CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$version -X main.commit=$commit -X main.buildTime=$build_time" \
  -o "$output_dir/operations-platform$ext" ./cmd/ops-platform

# 示例插件：入口必须落在插件包内（清单 entry.executable 依赖真实文件），
# 先构建到 examples/plugins/demo-echo/bin，再复制一份到产物目录。
demo_bin_dir="examples/plugins/demo-echo/bin"
mkdir -p "$demo_bin_dir"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$demo_bin_dir/demo-echo$ext" ./examples/plugins/demo-echo
cp "$demo_bin_dir/demo-echo$ext" "$output_dir/demo-echo$ext"

echo "构建完成: $output_dir/operations-platform$ext（CGO_ENABLED=0）"
echo "示例插件: $demo_bin_dir/demo-echo$ext"
