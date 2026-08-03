#!/usr/bin/env bash
#
# build-image.sh
#
# 在服务器上从本仓库源码构建 new-api 自定义镜像（new-api-custom:latest）。
# deploy-newapi.sh 默认使用该镜像；公共镜像 calciumion/new-api:latest 不含
# 本仓库的定制代码（如模型映射等功能的fork改动），生产部署应自建镜像。
#
# 用法（在服务器上）：
#   sudo bash build-image.sh [源码目录] [镜像tag]
#   例：sudo bash build-image.sh ~/new-api-src new-api-custom:latest
#
# 源码上传（在本地终端）：
#   git archive HEAD --format=tar.gz --prefix=new-api-src/ -o /tmp/new-api-src.tar.gz
#   scp /tmp/new-api-src.tar.gz ubuntu@<服务器IP>:~/
#   ssh ubuntu@<服务器IP> 'mkdir -p ~/new-api-src && tar xzf ~/new-api-src.tar.gz -C ~/new-api-src --strip-components=1'

set -euo pipefail

SRC_DIR="${1:-$HOME/new-api-src}"
IMAGE="${2:-new-api-custom:latest}"
# 国内构建默认走 goproxy.cn（proxy.golang.org 在大陆不可达）
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

log()  { echo -e "\033[34m[$(date '+%H:%M:%S')]\033[0m $*"; }
ok()   { echo -e "\033[32m[OK]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*" >&2; }

if [[ $EUID -ne 0 ]]; then
  err "请以 root 身份运行：sudo bash $0 [源码目录] [镜像tag]"
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  err "未检测到 docker，请先运行 install-docker-redis-mysql.sh 安装 Docker。"
  exit 1
fi

if [[ ! -f "${SRC_DIR}/Dockerfile" || ! -f "${SRC_DIR}/go.mod" ]]; then
  err "源码目录 ${SRC_DIR} 缺少 Dockerfile/go.mod，先按脚本头部说明上传源码。"
  exit 1
fi

log "构建镜像 ${IMAGE}（源码 ${SRC_DIR}，GOPROXY=${GOPROXY}），约需 5-15 分钟..."
docker build \
  --build-arg "GOPROXY=${GOPROXY}" \
  -t "${IMAGE}" \
  "${SRC_DIR}"

ok "镜像 ${IMAGE} 构建完成。继续：sudo bash deploy-newapi.sh start|update"
