#!/usr/bin/env bash
#
# deploy-domain.sh
#
# 在已部署好 new-api（deploy-newapi.sh）的服务器上，用 Caddy 容器为域名提供
# HTTPS 反代：80/443 -> newapi-net 内的 new-api:3000。
# Let's Encrypt 证书由 Caddy 自动申请与续期。
#
# 用法（在服务器上，root）：
#   sudo DOMAIN=www.litemall.asia bash deploy-domain.sh start
#   sudo bash deploy-domain.sh stop | restart | status | logs | update
#
# 只读依赖：docker 网络 newapi-net 与容器 new-api（由 deploy-newapi.sh 创建）。
# 本脚本不触碰 mysql8 / redis6。
#
# 运维注意：Caddy 在加载配置时解析上游容器名并缓存 IP，之后任何时候重建
# new-api 容器（如 update/改环境变量），都必须再执行 docker restart caddy，
# 否则会因缓存旧 IP 而 502。

set -euo pipefail

# ---------------- 可配置项 ----------------
DOMAIN="${DOMAIN:-www.litemall.asia}"          # 主域名（证书 SAN 同时含裸域）
APEX_DOMAIN="${APEX_DOMAIN:-litemall.asia}"    # 裸域，一并反代
# cursor-api-proxy 仪表盘/API 域名（空=不写入 Caddyfile）
CURSOR_DOMAIN="${CURSOR_DOMAIN:-cursor.litemall.asia}"
CURSOR_UPSTREAM="${CURSOR_UPSTREAM:-cursor-api-proxy:8765}"
CADDY_IMAGE="${CADDY_IMAGE:-caddy:2}"
CADDY_CONTAINER="${CADDY_CONTAINER:-caddy}"
CADDY_BASE_DIR="${CADDY_BASE_DIR:-/data/caddy}"
SHARED_NETWORK="${SHARED_NETWORK:-newapi-net}"
UPSTREAM="${UPSTREAM:-new-api:3000}"           # newapi-net 内容器名回源
# -------------------------------------------

log()  { echo -e "\033[34m[$(date '+%H:%M:%S')]\033[0m $*"; }
ok()   { echo -e "\033[32m[OK]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*" >&2; }

if [[ $EUID -ne 0 ]]; then
  err "请以 root 身份运行：sudo bash $0 <command>"
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  err "未检测到 docker，请先运行 install-docker-redis-mysql.sh 安装 Docker。"
  exit 1
fi

ensure_network() {
  if ! docker network ls --format '{{.Name}}' | grep -qx "${SHARED_NETWORK}"; then
    err "docker 网络 ${SHARED_NETWORK} 不存在，请先运行 deploy-newapi.sh start。"
    exit 1
  fi
}

ensure_upstream() {
  if ! docker ps --format '{{.Names}}' | grep -qx "${UPSTREAM%%:*}"; then
    err "上游容器 ${UPSTREAM%%:*} 未在运行，请先运行 deploy-newapi.sh start。"
    exit 1
  fi
}

write_caddyfile() {
  mkdir -p "${CADDY_BASE_DIR}/data" "${CADDY_BASE_DIR}/config"

  local cursor_block=""
  if [[ -n "${CURSOR_DOMAIN}" ]]; then
    cursor_block="$(cat <<EOF

${CURSOR_DOMAIN} {
	encode gzip zstd
	reverse_proxy ${CURSOR_UPSTREAM}
}
EOF
)"
  fi

  cat > "${CADDY_BASE_DIR}/Caddyfile" <<EOF
${DOMAIN}, ${APEX_DOMAIN} {
	encode gzip zstd
	reverse_proxy ${UPSTREAM}
}
${cursor_block}
EOF
  ok "Caddyfile 已写入 ${CADDY_BASE_DIR}/Caddyfile"
  if [[ -n "${CURSOR_DOMAIN}" ]]; then
    ok "已包含 cursor 站点：https://${CURSOR_DOMAIN} → ${CURSOR_UPSTREAM}"
  fi
}

container_exists()  { docker ps -a --format '{{.Names}}' | grep -qx "${CADDY_CONTAINER}"; }
container_running() { docker ps --format '{{.Names}}'    | grep -qx "${CADDY_CONTAINER}"; }

create_container() {
  log "拉取 Caddy 镜像 ${CADDY_IMAGE}..."
  docker pull "${CADDY_IMAGE}"

  log "启动 Caddy 容器..."
  docker run -d \
    --name "${CADDY_CONTAINER}" \
    --restart always \
    --network "${SHARED_NETWORK}" \
    -p 80:80 \
    -p 443:443 \
    -p 443:443/udp \
    -v "${CADDY_BASE_DIR}/data:/data" \
    -v "${CADDY_BASE_DIR}/config:/config" \
    -v "${CADDY_BASE_DIR}/Caddyfile:/etc/caddy/Caddyfile:ro" \
    "${CADDY_IMAGE}"
}

# 等待证书签发成功（Caddy 日志出现证书获取/服务就绪标志）
wait_ready() {
  log "等待 Let's Encrypt 证书签发（最多 120 秒）..."
  local i=0
  while (( i < 60 )); do
    if docker logs "${CADDY_CONTAINER}" 2>&1 | grep -qE "certificate obtained successfully"; then
      ok "证书已签发并存储。"
      return 0
    fi
    if docker logs "${CADDY_CONTAINER}" 2>&1 | grep -q '"level":"error"'; then
      warn "出现错误日志，用 sudo bash $0 logs 查看详情（可能是 DNS/安全组未放行 80/443）。"
      return 1
    fi
    i=$((i+1))
    sleep 2
  done
  warn "120 秒内未确认证书签发结果，用 sudo bash $0 logs 查看。"
  return 1
}

cmd_start() {
  ensure_network
  ensure_upstream
  write_caddyfile

  if container_running; then
    ok "Caddy 容器已在运行中。"
  elif container_exists; then
    log "容器已存在但停止，直接启动..."
    docker start "${CADDY_CONTAINER}" >/dev/null
  else
    create_container >/dev/null
  fi

  wait_ready || true
  log "外部验证："
  if curl -fsS -m 10 "https://${DOMAIN}/api/status" | grep -q '"success"[[:space:]]*:[[:space:]]*true'; then
    ok "https://${DOMAIN} 已可访问。"
  else
    warn "https://${DOMAIN} 暂未通过验证：证书可能仍在签发中，稍后用 status 复查。"
    warn "同时确认云安全组已放行 80/443 入站。"
  fi
}

cmd_stop() {
  if ! container_exists; then
    warn "Caddy 容器不存在。"
    return 0
  fi
  if container_running; then
    log "停止 Caddy..."
    docker stop "${CADDY_CONTAINER}" >/dev/null
    ok "已停止。"
  else
    warn "Caddy 已处于停止状态。"
  fi
}

cmd_restart() {
  cmd_stop
  cmd_start
}

cmd_status() {
  if ! container_exists; then
    warn "Caddy 容器不存在。"
    return 0
  fi
  docker ps -a --filter "name=^${CADDY_CONTAINER}$" \
    --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
  log "HTTPS 探测："
  if curl -fsS -m 10 "https://${DOMAIN}/api/status" | grep -q '"success"[[:space:]]*:[[:space:]]*true'; then
    ok "https://${DOMAIN} 正常。"
  else
    warn "https://${DOMAIN} 不可达，查看日志：sudo bash $0 logs"
  fi
}

cmd_logs() {
  if ! container_exists; then
    err "Caddy 容器不存在。"
    exit 1
  fi
  docker logs -f --tail 200 "${CADDY_CONTAINER}"
}

cmd_update() {
  log "更新模式：拉取最新镜像并重建容器（证书由卷保留）..."
  ensure_network
  ensure_upstream
  write_caddyfile

  if container_exists; then
    log "移除旧容器..."
    docker rm -f "${CADDY_CONTAINER}" >/dev/null
  fi
  create_container >/dev/null
  wait_ready || true
  ok "Caddy 已更新。"
}

usage() {
  cat <<EOF
用法: sudo DOMAIN=<域名> bash $0 <command>

命令:
  start     部署或启动 Caddy（自动申请 Let's Encrypt 证书）
  stop      停止（不删容器，证书保留）
  restart   重启（重新加载 Caddyfile）
  status    查看容器状态与 HTTPS 健康
  logs      跟随查看日志（Ctrl-C 退出）
  update    拉取最新镜像并重建容器

环境变量:
  DOMAIN          主域名（默认 www.litemall.asia）
  APEX_DOMAIN     裸域（默认 litemall.asia）
  CURSOR_DOMAIN   cursor-api-proxy 域名（默认 cursor.litemall.asia；空=不反代）
  CURSOR_UPSTREAM 默认 cursor-api-proxy:8765
EOF
}

case "${1:-}" in
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  restart) cmd_restart ;;
  status)  cmd_status ;;
  logs)    cmd_logs ;;
  update)  cmd_update ;;
  ""|-h|--help) usage ;;
  *) err "未知命令: $1"; usage; exit 1 ;;
esac
