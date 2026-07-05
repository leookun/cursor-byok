#!/usr/bin/env bash
set -euo pipefail

# ── 颜色输出 ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()  { echo -e "${RED}[ERR]${NC} $*" >&2; exit 1; }

# ── 切换到脚本所在目录（保证相对路径正确）────────────────────────────────────
cd "$(dirname "$0")"

echo ""
echo "=============================="
echo "  Cursor BYOK  开发环境启动"
echo "=============================="
echo ""

# ── 依赖检查 ──────────────────────────────────────────────────────────────────
check_cmd() {
  local cmd=$1 install_hint=$2
  if ! command -v "$cmd" &>/dev/null; then
    err "未找到 '$cmd'，请先安装。\n  参考：$install_hint"
  fi
  ok "$cmd 已安装 ($(command -v "$cmd"))"
}

check_cmd go     "https://go.dev/doc/install"
check_cmd wails3 "go install github.com/wailsapp/wails/v3/cmd/wails3@latest"
check_cmd task   "https://taskfile.dev/installation/"

echo ""

# ── 前端依赖（yarn install，仅在 node_modules 缺失或 yarn.lock 变更时执行）──
if [ ! -d "frontend/node_modules" ]; then
  warn "未检测到 frontend/node_modules，正在安装前端依赖..."
  if command -v yarn &>/dev/null; then
    (cd frontend && yarn install --frozen-lockfile)
    ok "前端依赖安装完成"
  elif command -v npm &>/dev/null; then
    warn "未找到 yarn，改用 npm install"
    (cd frontend && npm install)
    ok "前端依赖安装完成"
  else
    err "未找到 yarn 或 npm，请先安装 Node.js：https://nodejs.org"
  fi
else
  ok "frontend/node_modules 已存在，跳过安装"
fi

echo ""
echo ">>> 启动开发模式（task dev）..."
echo ""

exec task dev