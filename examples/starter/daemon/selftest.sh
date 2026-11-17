#!/usr/bin/env sh
# daemon 骨架本地端到端自测：构建插件到 bin/，构建一次性自测工具到临时目录，
# 由工具生成测试证书与按主机配置、拉起插件进程并走 mTLS 完成 Ready/GetVersion/Invoke/Shutdown。
set -eu
cd "$(dirname "$0")"

ext=""
if [ "$(go env GOOS)" = "windows" ]; then
    ext=".exe"
fi
mkdir -p bin
go build -o "bin/starter-daemon$ext" .

harness=$(mktemp "${TMPDIR:-/tmp}/starter-daemon-selftest.XXXXXX")
go build -o "$harness" ./selftest
trap 'rm -f "$harness"' EXIT

"$harness" "bin/starter-daemon$ext"
