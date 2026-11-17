#!/usr/bin/env sh
# 命令式骨架本地自测：先构建到 bin/（清单入口指向该目录），
# 再走 stdio 协议验证成功路径与结构化错误路径。
set -eu
cd "$(dirname "$0")"

ext=""
if [ "$(go env GOOS)" = "windows" ]; then
    ext=".exe"
fi
mkdir -p bin
go build -o "bin/starter-command$ext" .

assert_ok() {
    resp=$(printf '%s' "$1" | "./bin/starter-command$ext")
    echo "$resp" | grep -q '"status":"ok"' || { echo "selftest FAIL: 期望 ok，实际: $resp" >&2; exit 1; }
    echo "$resp" | grep -q '"message":"你好，ops"' || { echo "selftest FAIL: 输出不含预期 message: $resp" >&2; exit 1; }
}

assert_err() {
    resp=$(printf '%s' "$1" | "./bin/starter-command$ext" || true)
    echo "$resp" | grep -q "\"error\":{\"code\":\"$2\"" || { echo "selftest FAIL: 期望 $2，实际: $resp" >&2; exit 1; }
}

assert_ok '{"contract_version":"1.2","capability":"starter-command.greet","input":{"name":"ops"},"context":{"invocation_id":"t1","deadline_ms":3000}}'
assert_err '{"contract_version":"1.2","capability":"starter-command.greet","input":{},"context":{"invocation_id":"t2","deadline_ms":3000}}' input_invalid
assert_err '{"contract_version":"1.2","capability":"starter-command.nope","input":{},"context":{"invocation_id":"t3","deadline_ms":3000}}' capability_unknown

echo "selftest ok: ok 路径 / input_invalid / capability_unknown"
