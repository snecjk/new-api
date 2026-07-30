#!/usr/bin/env bash
#
# deploy-newapi.sh
#
# 在服务器上把 new-api 以 docker 容器方式部署起来，并连接同一台机器上已由
# install-docker-redis-mysql.sh 部署好的 redis6 / mysql8 容器。
#
# 用法（在服务器上）：
#   sudo bash deploy-newapi.sh start    # 首次部署 / 启动
#   sudo bash deploy-newapi.sh stop     # 停止
#   sudo bash deploy-newapi.sh restart  # 重启（先 stop 再 start，重新应用配置）
#   sudo bash deploy-newapi.sh status    # 查看运行状态
#   sudo bash deploy-newapi.sh logs      # 查看日志（Ctrl-C 退出）
#   sudo bash deploy-newapi.sh update    # 拉取最新镜像并重建容器
#
# 连接拓扑：
#   new-api 容器与 redis6 / mysql8 共享 docker 网络 newapi-net，
#   DSN 中直接用容器名 redis6 / mysql8 解析，无需宿主机 IP。
#   数据持久化：宿主机 /data/new-api/{data,logs}
#
# 需要以 root 身份运行（写 /data 目录、操作 docker）。

set -euo pipefail

# ---------------- 可配置项 ----------------
# new-api 容器
NEWAPI_IMAGE="calciumion/new-api:latest"
NEWAPI_CONTAINER="new-api"
NEWAPI_PORT=3000

# 持久化目录（与 redis6/mysql8 的 /data/redis /data/mysql 同级）
NEWAPI_DATA_DIR="/data/new-api/data"
NEWAPI_LOG_DIR="/data/new-api/logs"

# 共享网络（new-api 与 redis6/mysql8 都挂到这里，容器名互通）
SHARED_NETWORK="newapi-net"

# 已存在的依赖容器（由 install-docker-redis-mysql.sh 创建）
REDIS_CONTAINER="redis6"
MYSQL_CONTAINER="mysql8"

# 数据库与 Redis 连接凭据（必须与 install-docker-redis-mysql.sh 中一致）
MYSQL_USER="root"
MYSQL_PASSWORD="622851Tt."
MYSQL_DB="new-api"
REDIS_USER="root"
REDIS_PASSWORD="622851Tt."

# new-api 业务参数
TZ="Asia/Shanghai"
NODE_NAME="new-api-node-1"
ERROR_LOG_ENABLED="true"
BATCH_UPDATE_ENABLED="true"

# 启动后健康检查轮询参数
HEALTH_WAIT_ROUNDS=30
HEALTH_WAIT_INTERVAL=2
# ----------------------------------------

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

# 组装 new-api 连接串（容器名走共享网络解析）
sql_dsn="${MYSQL_USER}:${MYSQL_PASSWORD}@tcp(${MYSQL_CONTAINER}:3306)/${MYSQL_DB}"
redis_conn="redis://${REDIS_USER}:${REDIS_PASSWORD}@${REDIS_CONTAINER}:6379"

# ---------------- 网络与依赖容器 ----------------
ensure_network() {
  if docker network ls --format '{{.Name}}' | grep -qx "${SHARED_NETWORK}"; then
    log "共享网络 ${SHARED_NETWORK} 已存在。"
  else
    log "创建共享网络 ${SHARED_NETWORK}..."
    docker network create "${SHARED_NETWORK}" >/dev/null
    ok "网络 ${SHARED_NETWORK} 已创建。"
  fi
}

# 把已存在的 redis6/mysql8 挂到共享网络（幂等：已挂则跳过）
connect_deps() {
  local dep
  for dep in "${REDIS_CONTAINER}" "${MYSQL_CONTAINER}"; do
    if ! docker ps -a --format '{{.Names}}' | grep -qx "${dep}"; then
      err "依赖容器 ${dep} 不存在，请先运行 install-docker-redis-mysql.sh。"
      exit 1
    fi
    # 已挂载到该网络的容器不会重复连接
    if docker network inspect "${SHARED_NETWORK}" \
        --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null \
        | grep -qw "${dep}"; then
      log "容器 ${dep} 已挂载到 ${SHARED_NETWORK}。"
    else
      log "将容器 ${dep} 挂载到 ${SHARED_NETWORK}..."
      docker network connect "${SHARED_NETWORK}" "${dep}" 2>/dev/null \
        || warn "挂载 ${dep} 失败（可能已挂或容器状态异常），稍后如连接报错请检查 docker network inspect ${SHARED_NETWORK}。"
    fi
  done
}

ensure_dirs() {
  mkdir -p "${NEWAPI_DATA_DIR}" "${NEWAPI_LOG_DIR}"
}

# ---------------- 容器生命周期 ----------------
container_exists() {
  docker ps -a --format '{{.Names}}' | grep -qx "${NEWAPI_CONTAINER}"
}

container_running() {
  docker ps --format '{{.Names}}' | grep -qx "${NEWAPI_CONTAINER}"
}

create_container() {
  log "拉取 new-api 镜像 ${NEWAPI_IMAGE}..."
  docker pull "${NEWAPI_IMAGE}"

  log "启动 new-api 容器..."
  docker run -d \
    --name "${NEWAPI_CONTAINER}" \
    --restart always \
    --network "${SHARED_NETWORK}" \
    -p "${NEWAPI_PORT}:3000" \
    -e "SQL_DSN=${sql_dsn}" \
    -e "REDIS_CONN_STRING=${redis_conn}" \
    -e "TZ=${TZ}" \
    -e "ERROR_LOG_ENABLED=${ERROR_LOG_ENABLED}" \
    -e "BATCH_UPDATE_ENABLED=${BATCH_UPDATE_ENABLED}" \
    -e "NODE_NAME=${NODE_NAME}" \
    -v "${NEWAPI_DATA_DIR}:/data" \
    -v "${NEWAPI_LOG_DIR}:/app/logs" \
    "${NEWAPI_IMAGE}" \
    --log-dir /app/logs
}

# 等待 new-api 自身 HTTP 接口就绪
wait_healthy() {
  log "等待 new-api 就绪（最多 $((HEALTH_WAIT_ROUNDS * HEALTH_WAIT_INTERVAL)) 秒）..."
  local i=0
  until docker exec "${NEWAPI_CONTAINER}" \
      wget -q -O - http://localhost:3000/api/status 2>/dev/null \
      | grep -q '"success"[[:space:]]*:[[:space:]]*true'; do
    i=$((i+1))
    if (( i >= HEALTH_WAIT_ROUNDS )); then
      warn "new-api 未在预期时间内就绪，用 docker logs ${NEWAPI_CONTAINER} 查看日志。"
      return 1
    fi
    sleep "${HEALTH_WAIT_INTERVAL}"
  done
  ok "new-api 已就绪。"
}

cmd_start() {
  ensure_network
  connect_deps
  ensure_dirs

  if container_running; then
    ok "new-api 容器已在运行中，无需重复启动。"
    return 0
  fi

  if container_exists; then
    log "容器已存在但处于停止状态，直接启动..."
    docker start "${NEWAPI_CONTAINER}" >/dev/null
  else
    create_container >/dev/null
  fi

  wait_healthy || return 1
  ok "new-api 已启动：http://<服务器IP>:${NEWAPI_PORT}"
}

cmd_stop() {
  if ! container_exists; then
    warn "new-api 容器不存在，无需停止。"
    return 0
  fi
  if container_running; then
    log "停止 new-api 容器..."
    docker stop "${NEWAPI_CONTAINER}" >/dev/null
    ok "已停止。"
  else
    warn "new-api 容器已处于停止状态。"
  fi
}

cmd_restart() {
  cmd_stop
  cmd_start
}

cmd_status() {
  if ! container_exists; then
    warn "new-api 容器不存在。"
    return 0
  fi
  docker ps -a --filter "name=^${NEWAPI_CONTAINER}$" \
    --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
  if container_running; then
    log "HTTP 接口探测："
    if docker exec "${NEWAPI_CONTAINER}" \
        wget -q -O - http://localhost:3000/api/status 2>/dev/null \
        | grep -q '"success"[[:space:]]*:[[:space:]]*true'; then
      ok "接口正常响应。"
    else
      warn "接口未正常响应，用 docker logs ${NEWAPI_CONTAINER} 查看。"
    fi
  fi
}

cmd_logs() {
  if ! container_exists; then
    err "new-api 容器不存在。"
    exit 1
  fi
  docker logs -f --tail 200 "${NEWAPI_CONTAINER}"
}

cmd_update() {
  log "更新模式：拉取最新镜像并重建容器（配置/数据由卷保留）..."
  ensure_network
  connect_deps
  ensure_dirs

  if container_running || container_exists; then
    log "移除旧容器..."
    docker rm -f "${NEWAPI_CONTAINER}" >/dev/null
  fi

  create_container >/dev/null
  wait_healthy || return 1
  ok "new-api 已更新并启动。"
}

usage() {
  cat <<EOF
用法: sudo bash $0 <command>

命令:
  start     首次部署或启动 new-api 容器
  stop      停止 new-api 容器（不删除，数据保留）
  restart   重启 new-api 容器（stop + start，重新应用配置）
  status    查看容器运行状态与接口健康
  logs      跟随查看日志（Ctrl-C 退出）
  update    拉取最新镜像并重建容器
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
