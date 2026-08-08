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
#   sudo bash deploy-newapi.sh update    # 拉取 fork 最新代码 + 重新构建镜像 + 重建容器
#
# 镜像来源：start/update 自动克隆/拉取 fork（REPO_URL）最新代码，在本机 docker build
#           （多阶段 Dockerfile）。不走 registry、不 docker pull —— 自建镜像不在任何
#           registry，pull 必然 403。工作流：本地 push 到 GitHub main → 服务器跑 update。
#
# 连接拓扑：
#   new-api 容器与 redis6 / mysql8 共享 docker 网络 newapi-net，
#   DSN 中直接用容器名 redis6 / mysql8 解析，无需宿主机 IP。
#   数据持久化：宿主机 /data/new-api/{data,logs,src}（src=fork 源码克隆目录）
#
# 需要以 root 身份运行（写 /data 目录、操作 docker）。

set -euo pipefail

# ---------------- 可配置项（均支持环境变量覆盖，便于上层编排脚本注入统一凭据与反代参数）----------------
# new-api 容器（镜像在本机从 fork 源码构建，tag=NEWAPI_IMAGE；不从 registry 拉取）
NEWAPI_IMAGE="${NEWAPI_IMAGE:-new-api-custom:latest}"
NEWAPI_CONTAINER="${NEWAPI_CONTAINER:-new-api}"

# ---- 源码（从你的 fork 克隆并在本机 docker build；每次 update 自动拉最新代码）----
# 国内网络访问 GitHub 可能不稳定；fetch 失败时用 build-image.md 的手动源码包方案兜底。
REPO_URL="${REPO_URL:-https://github.com/snecjk/new-api.git}"
REPO_BRANCH="${REPO_BRANCH:-main}"
SOURCE_DIR="${SOURCE_DIR:-/data/new-api/src}"   # fork 克隆目录（持久化，避免每次 update 全量重克隆）

# 持久化目录（与 redis6/mysql8 的 /data/redis /data/mysql 同级）
NEWAPI_DATA_DIR="${NEWAPI_DATA_DIR:-/data/new-api/data}"
NEWAPI_LOG_DIR="${NEWAPI_LOG_DIR:-/data/new-api/logs}"

# 共享网络（new-api 与 redis6/mysql8 都挂到这里，容器名互通）
SHARED_NETWORK="${SHARED_NETWORK:-newapi-net}"

# 已存在的依赖容器（由 install-docker-redis-mysql.sh 创建）
REDIS_CONTAINER="${REDIS_CONTAINER:-redis6}"
MYSQL_CONTAINER="${MYSQL_CONTAINER:-mysql8}"

# 数据库与 Redis 连接凭据（必须与 install-docker-redis-mysql.sh 中一致）
MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-622851Tt.}"
MYSQL_DB="${MYSQL_DB:-new-api}"
REDIS_USER="${REDIS_USER:-root}"
REDIS_PASSWORD="${REDIS_PASSWORD:-622851Tt.}"

# new-api 业务参数
TZ="${TZ:-Asia/Shanghai}"
NODE_NAME="${NODE_NAME:-new-api-node-1}"
ERROR_LOG_ENABLED="${ERROR_LOG_ENABLED:-true}"
BATCH_UPDATE_ENABLED="${BATCH_UPDATE_ENABLED:-true}"

# ---------------- 反代/会话相关（配合 deploy-domain.sh + HTTPS 域名）----------------
# 绑定域名：留空=本地 HTTP 直连(不设 Secure cookie)；设置=反代 HTTPS 场景
DOMAIN="${DOMAIN:-}"
# 会话密钥：单机建议设，迁移到新机器时必须复用同一值（多机必须）
SESSION_SECRET="${SESSION_SECRET:-}"
# SESSION_COOKIE_SECURE 不作为独立配置项：DOMAIN 非空时 create_container 自动强制 true
# （代码强绑定 SESSION_COOKIE_TRUSTED_URL，缺一启动报错）；DOMAIN 为空即本地 HTTP，不设 secure。
# 可信代理网段：留空=信任默认(loopback/RFC1918/fc00::/7，含 docker 172.x)；none=严格；或填 CIDR 逗号列表
TRUSTED_PROXIES="${TRUSTED_PROXIES:-}"

# DEBUG_BIND：留空=3000 不映射宿主机(默认,靠 newapi-net 容器名回源,公网不可直连)；
#             127.0.0.1=仅本机可访问 3000(调试用)，不对外暴露
DEBUG_BIND="${DEBUG_BIND:-}"
# ----------------------------------------

# 启动后健康检查轮询参数
HEALTH_WAIT_ROUNDS="${HEALTH_WAIT_ROUNDS:-30}"
HEALTH_WAIT_INTERVAL="${HEALTH_WAIT_INTERVAL:-2}"
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

if ! command -v git >/dev/null 2>&1; then
  err "未检测到 git。本脚本从 fork 克隆源码并在本地构建镜像，需要 git：sudo apt-get install -y git（或 yum install -y git）。"
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
      if ! docker network connect "${SHARED_NETWORK}" "${dep}"; then
        err "挂载 ${dep} 到 ${SHARED_NETWORK} 失败（容器状态异常或权限问题）。"
        err "检查：docker ps -a | grep ${dep}；docker network inspect ${SHARED_NETWORK}"
        exit 1
      fi
    fi
  done
}

ensure_dirs() {
  mkdir -p "${NEWAPI_DATA_DIR}" "${NEWAPI_LOG_DIR}"
}

# ---------------- 源码与镜像构建 ----------------
# 从 fork 克隆/拉取最新代码（镜像不在 registry 拉取，而是在本机从源码构建）
ensure_source() {
  if [[ -d "${SOURCE_DIR}/.git" ]]; then
    log "拉取 fork 最新代码：${REPO_URL}（分支 ${REPO_BRANCH}）..."
    git -C "${SOURCE_DIR}" fetch --quiet origin "+${REPO_BRANCH}:refs/remotes/origin/${REPO_BRANCH}"
    git -C "${SOURCE_DIR}" reset --hard --quiet "origin/${REPO_BRANCH}"
    git -C "${SOURCE_DIR}" clean -fd --quiet
    ok "源码已更新到 origin/${REPO_BRANCH}：$(git -C "${SOURCE_DIR}" log -1 --format='%h %s' 2>/dev/null || echo '未知')"
  else
    # 残留目录（上次克隆中断）先清理，否则 git clone 报 already exists
    if [[ -d "${SOURCE_DIR}" ]]; then
      warn "源码目录存在但不完整（上次克隆中断？），清理后重克隆：${SOURCE_DIR}"
      rm -rf "${SOURCE_DIR}"
    fi
    # 浅克隆：国内网络访问 GitHub 不稳，全量克隆容易卡死
    log "首次克隆 fork（浅克隆）：${REPO_URL}（分支 ${REPO_BRANCH}）→ ${SOURCE_DIR} ..."
    mkdir -p "$(dirname "${SOURCE_DIR}")"
    git clone --branch "${REPO_BRANCH}" --single-branch --depth 1 --quiet "${REPO_URL}" "${SOURCE_DIR}"
    ok "源码已克隆：$(git -C "${SOURCE_DIR}" log -1 --format='%h %s' 2>/dev/null || echo '未知')"
  fi
}

# 在本机从 fork 源码构建镜像（多阶段 Dockerfile：bun 前端 + Go 后端，均在容器内完成）
# 首次构建约 5-15 分钟；后续有 BuildKit 层缓存，增量通常 1-3 分钟。
build_image() {
  log "本地构建镜像 ${NEWAPI_IMAGE}（源码：${SOURCE_DIR}，首次约需数分钟）..."
  docker build --build-arg "GOPROXY=${GOPROXY:-https://goproxy.cn,direct}" -t "${NEWAPI_IMAGE}" "${SOURCE_DIR}"
  ok "镜像 ${NEWAPI_IMAGE} 构建完成。"
}

# ---------------- 容器生命周期 ----------------
container_exists() {
  docker ps -a --format '{{.Names}}' | grep -qx "${NEWAPI_CONTAINER}"
}

container_running() {
  docker ps --format '{{.Names}}' | grep -qx "${NEWAPI_CONTAINER}"
}

create_container() {
  # 镜像由 build_image 从 fork 源码本地构建（tag=NEWAPI_IMAGE），此处不 pull、直接 run。

  # 端口映射：默认不暴露宿主机(靠 newapi-net 容器名回源,公网不可直连)；
  # DEBUG_BIND=127.0.0.1 时仅本机可访问 3000(调试用)。
  # 用字符串拼接而非数组，避免 set -u 下空数组在旧 bash(<4.4) 报 unbound variable。
  local extra=""
  if [[ -n "${DEBUG_BIND}" ]]; then
    extra+=" -p ${DEBUG_BIND}:3000:3000"
  fi

  # 会话/反代 env：DOMAIN 非空时强制 SECURE=true + TRUSTED_URL(代码强绑定,缺一启动报错)
  if [[ -n "${DOMAIN}" ]]; then
    extra+=" -e SESSION_COOKIE_SECURE=true -e SESSION_COOKIE_TRUSTED_URL=https://${DOMAIN}"
    [[ -n "${SESSION_SECRET}"  ]] && extra+=" -e SESSION_SECRET=${SESSION_SECRET}"
    [[ -n "${TRUSTED_PROXIES}" ]] && extra+=" -e TRUSTED_PROXIES=${TRUSTED_PROXIES}"
  elif [[ -n "${SESSION_SECRET}" ]]; then
    # 无域名(本地 HTTP)：SESSION_SECRET 若显式提供也注入(单机也建议设)
    extra+=" -e SESSION_SECRET=${SESSION_SECRET}"
  fi

  log "启动 new-api 容器..."
  # shellcheck disable=SC2086  # extra 经分词展开为多个 docker 参数，值均无空格/通配符
  docker run -d \
    --name "${NEWAPI_CONTAINER}" \
    --restart always \
    --network "${SHARED_NETWORK}" \
    ${extra} \
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
    ensure_source
    build_image
    create_container >/dev/null
  fi

  wait_healthy || return 1
  if [[ -n "${DOMAIN}" ]]; then
    ok "new-api 已启动：反代 https://${DOMAIN}（由 deploy-domain.sh 提供，3000 未暴露公网）"
  elif [[ -n "${DEBUG_BIND}" ]]; then
    ok "new-api 已启动：本地直连 http://${DEBUG_BIND}:3000（仅服务器本机可访问，公网不可直连）"
  else
    ok "new-api 已启动：3000 仅在 newapi-net 内可达（无主机端口映射，公网不可直连）"
  fi
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
  log "更新模式：拉取 fork 最新代码 → 重新构建镜像 → 重建容器（配置/数据由卷保留）..."
  ensure_network
  connect_deps
  ensure_dirs

  # 先拉代码 + 构建成功再移除旧容器：构建失败（国内网络抖动 / 编译错误）时
  # 旧节点不下线——旧容器一旦 rm，--restart 也救不回来。
  ensure_source
  build_image

  if container_running || container_exists; then
    log "移除旧容器..."
    docker rm -f "${NEWAPI_CONTAINER}" >/dev/null
  fi

  create_container >/dev/null
  wait_healthy || return 1
  ok "new-api 已更新并启动（源码：origin/${REPO_BRANCH}）。"
}

usage() {
  cat <<EOF
用法: sudo bash $0 <command>

命令:
  start     首次部署或启动（克隆 fork + 构建镜像 + 启动容器）
  stop      停止 new-api 容器（不删除，数据保留）
  restart   重启 new-api 容器（stop + start，重新应用配置）
  status    查看容器运行状态与接口健康
  logs      跟随查看日志（Ctrl-C 退出）
  update    拉取 fork 最新代码并重新构建镜像、重建容器（本地 push 后用它生效）
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
