#!/bin/sh
# 本地构建脚本
#
# 服务端用 //go:embed front/* 把 Web UI 打进二进制，因此本地编译前
# 必须先把前端产物放到 front/ 目录，否则镜像/二进制里的界面是空的。
#
# 用法：
#   ./scripts/build-local.sh            # 构建当前平台二进制
#   ./scripts/build-local.sh linux amd64
#   FRONTEND_DIR=/path/to/react ./scripts/build-local.sh
#
# 依赖：go、npm、node
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# 前端源码目录：默认取同级的 Docker-Copilot-React
FRONTEND_DIR="${FRONTEND_DIR:-$ROOT_DIR/../Docker-Copilot-React}"
GOOS_TARGET="${1:-$(go env GOOS)}"
GOARCH_TARGET="${2:-$(go env GOARCH)}"
VERSION="$(tr -d '[:space:]' < version)"
BUILD_DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

info() { printf '\033[36m[build]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[build]\033[0m %s\n' "$*" >&2; exit 1; }

# 1. 构建前端
if [ ! -d "$FRONTEND_DIR" ]; then
    fail "找不到前端目录: $FRONTEND_DIR（可用 FRONTEND_DIR 指定）"
fi

info "构建前端: $FRONTEND_DIR"
(
    cd "$FRONTEND_DIR"
    if [ ! -d node_modules ]; then
        info "安装前端依赖"
        npm ci
    fi
    npm run build
) || fail "前端构建失败"

# 2. 把产物放到 front/（embed 的目标目录）
# 目标目录固定为仓库内的 front/，且拒绝空值/根路径，避免误删
FRONT_TARGET="$ROOT_DIR/front"
case "$FRONT_TARGET" in
    ""|"/"|"/*") fail "拒绝写入异常路径: $FRONT_TARGET" ;;
esac
case "$FRONT_TARGET" in
    "$ROOT_DIR"/front) ;;
    *) fail "前端产物目录必须位于仓库内: $FRONT_TARGET" ;;
esac

info "同步前端产物到 front/"
rm -rf "$FRONT_TARGET"
cp -R "$FRONTEND_DIR/dist" "$FRONT_TARGET"
[ -f "$FRONT_TARGET/index.html" ] || fail "前端产物缺少 index.html"

# 3. 编译后端
OUT_DIR="$ROOT_DIR/dist/linux/$GOARCH_TARGET"
mkdir -p "$OUT_DIR"
info "编译 $GOOS_TARGET/$GOARCH_TARGET  版本=$VERSION"
CGO_ENABLED=0 GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" \
    go build -trimpath \
        -ldflags "-s -w \
            -X 'github.com/onlyLTY/dockerCopilot/internal/config.Version=$VERSION' \
            -X 'github.com/onlyLTY/dockerCopilot/internal/config.BuildDate=$BUILD_DATE'" \
        -o "$OUT_DIR/dockerCopilot" .

info "完成: $OUT_DIR/dockerCopilot"
"$OUT_DIR/dockerCopilot" --help >/dev/null 2>&1 || true
