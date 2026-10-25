#!/usr/bin/env bash
# 切换到本脚本所在目录 (app/)，保证相对路径正确
cd "$(dirname "$0")" || exit 1

# 读取 start.conf 里的环境变量 (KEY=VALUE，# 为注释)，并导出
if [ -f start.conf ]; then
  set -a
  # shellcheck disable=SC1091
  . ./start.conf
  set +a
fi


# 运行 webui.py，附加参数原样透传
exec ./build_venv/bin/python -u webui.py --server_name 0.0.0.0 "$@"
