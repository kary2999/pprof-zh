#!/usr/bin/env bash
# 一键演示：编译 demo -> 生成 profile -> 启动 pprof-zh
# 用法：cmd/pprof-zh/demo/run.sh [cpu|heap|goroutine|mutex|block]（默认 cpu）
set -euo pipefail
cd "$(dirname "$0")/../../.."          # 仓库根目录
OUT=cmd/pprof-zh/demo/out
KIND=${1:-cpu}
mkdir -p "$OUT"
go build -o "$OUT/pprof-zh" ./cmd/pprof-zh
# 业务代码里没有任何埋点：编译期自动插桩（原始源码不改），HTTP handler 按签名自动识别为入口，无需任何参数
"$OUT/pprof-zh" instrument -dir ./cmd/pprof-zh/demo -out "$OUT/overlay"
go build -overlay="$PWD/$OUT/overlay/overlay.json" -o "$OUT/demo" ./cmd/pprof-zh/demo
PPROF_ZH_CALLS_OUT="$OUT/calls.json" "$OUT/demo" "$OUT"
echo "打开 http://127.0.0.1:8078"
export PPROF_ZH_CALLS="$OUT/calls.json"
exec "$OUT/pprof-zh" -http=127.0.0.1:8078 -no_browser "$OUT/$KIND.pprof"
