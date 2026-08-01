#!/usr/bin/env bash
#
# deploy-newapi.sh  (海外节点 / overseas)
#
# 在海外服务器上把 new-api 以 docker 容器方式部署起来，复用国内服务器
# (140.143.183.34) 上已部署好的 MySQL 8 (mysql8:3306) 与 Redis 6 (redis6:6379)
# —— 即多节点共享同一套数据库与缓存。海外节点以 slave 角色运行
# (NODE_TYPE=slave)，不跑后台迁移/调度/清理等任务（这些仍由国内 master 负责），
# 仅对外提供 relay 服务并如实计费。
#
# 用法（在海外服务器上）：
#   sudo bash deploy-newapi.sh start    # 首次部署 / 启动
#   sudo bash deploy-newapi.sh stop     # 停止（不删容器，数据保留）
#   sudo bash deploy-newapi.sh restart  # 重启（stop + start；复用现有容器，不重建）
#   sudo bash deploy-newapi.sh status   # 查看运行状态 + 接口健康 + 连接目标
#   sudo bash deploy-newapi.sh logs     # 跟随查看日志（Ctrl-C 退出）
#   sudo bash deploy-newapi.sh update   # 拉取最新镜像并重建容器（改配置后用它生效）
#
# 连接拓扑：
#   海外 new-api 容器 ──公网──▶ 国内 140.143.183.34:3306 (MySQL) / :6379 (Redis)
#   （依赖容器在国内服务器的 docker 网络里，跨机走公网 IP+端口，不用容器名解析）
#   数据持久化：海外宿主机 /data/new-api/{data,logs}
#
# 目标服务器（海外，本脚本运行机）：
#   公网 IP    : 38.226.195.219      SSH 端口: 10009
#   内网 IP    : 10.198.2.236        （同 VPC 内才可达；跨公网连接不用它）
#   区域       : ap-northeast-1（东京）
#   公网域名   : 019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz
#   ⚠️ 国内安全组须放行本机【公网】IP 38.226.195.219 访问 3306/6379
#      （跨公网流量源 IP 是公网 IP，不是内网 10.198.2.236，填错会一直连不上）。
#
# 注意：改任何可配置项后，必须用 `update`（而非 restart）重建容器才会生效——
#       restart 只是 stop+start，复用旧容器与旧环境变量。
#
# 需要以 root 身份运行（写 /data 目录、操作 docker）。

set -euo pipefail

# ---------------- 可配置项（均支持环境变量覆盖）----------------
# new-api 容器
NEWAPI_IMAGE="${NEWAPI_IMAGE:-calciumion/new-api:latest}"
NEWAPI_CONTAINER="${NEWAPI_CONTAINER:-new-api}"
NEWAPI_PORT="${NEWAPI_PORT:-3000}"          # 海外对外服务端口（默认映射宿主机 3000）

# 持久化目录（海外宿主机；与国内 /data/new-api 同级，互不影响）
NEWAPI_DATA_DIR="${NEWAPI_DATA_DIR:-/data/new-api/data}"
NEWAPI_LOG_DIR="${NEWAPI_LOG_DIR:-/data/new-api/logs}"

# 共享网络（仅用于将来在海外挂本地反代容器；不依赖任何本地 redis/mysql 容器）
SHARED_NETWORK="${SHARED_NETWORK:-newapi-net}"

# ---- 国内服务器（共享的 MySQL + Redis 在这里，跨机走公网 IP+端口）----
CHINA_HOST="${CHINA_HOST:-140.143.183.34}"
MYSQL_PORT="${MYSQL_PORT:-3306}"
REDIS_PORT="${REDIS_PORT:-6379}"

# 凭据（必须与国内 install-docker-redis-mysql.sh 一致）
MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-622851Tt.}"
MYSQL_DB="${MYSQL_DB:-new-api}"
REDIS_USER="${REDIS_USER:-root}"
REDIS_PASSWORD="${REDIS_PASSWORD:-622851Tt.}"

# ---- 多节点关键配置 ----
# 节点角色：slave=跳过后台迁移/调度/清理（仅 master 跑这些）；海外默认 slave。
#           （国内节点保持默认 master，无需改动。）
NODE_TYPE="${NODE_TYPE:-slave}"
# 节点名：审计日志标识，必须与国内节点(new-api-node-1)不同，便于区分来源
NODE_NAME="${NODE_NAME:-new-api-overseas}"
# 会话/签名密钥：多节点建议两端设同一个随机串（否则跨节点登录态/签名不互认）。
#   未设=本机每次重启随机生成 → 重启后会话失效；CRYPTO_SECRET 默认回退到此值。
#   国内节点当前未设 → 若要跨节点互认，请在国内也设同一值后 `update`（见 deploy-newapi.md）。
SESSION_SECRET="${SESSION_SECRET:-}"
# 批量更新：false=每请求即时原子写库（跨境链路下计费更稳：预扣失败即拒请求、不丢批次）；
#           true =合并写库减少跨境往返，但批次 flush 失败会丢失该批增量(少计费)。
#           两种模式均为原子增量；但跨节点计费严格性要求两端都 false（见文档「二、计费安全性」），
#           国内也建议改 false；否则一端 batch=true 时缓存回填会覆盖原子 INCRBY 导致计费偏差。
BATCH_UPDATE_ENABLED="${BATCH_UPDATE_ENABLED:-false}"

# ---- 业务参数 ----
TZ="${TZ:-UTC}"                       # 海外默认 UTC；要与国内日志对齐可改 Asia/Shanghai
ERROR_LOG_ENABLED="${ERROR_LOG_ENABLED:-true}"

# ---- 反代/会话（配合 HTTPS 域名；留空=本地 HTTP 直连 3000）----
DOMAIN="${DOMAIN:-}"
# SESSION_COOKIE_SECURE / SESSION_COOKIE_TRUSTED_URL 不作为独立配置项：
#   DOMAIN 非空时 create_container 自动强制注入（代码强绑定，缺一启动报错）。
TRUSTED_PROXIES="${TRUSTED_PROXIES:-}"
# DEBUG_BIND：留空=3000 映射宿主机 0.0.0.0（海外对外服务）；
#             127.0.0.1=仅本机可访问 3000（调试用，不对外）
DEBUG_BIND="${DEBUG_BIND:-}"

# INGRESS_PORT：DOMAIN 非空时的「外部/云反代」入口端口——new-api 直接监听宿主机该端口
#               （0.0.0.0），供云端 HTTP 代理（域名 → 本机 INGRESS_PORT）回源，TLS 由云端
#               边缘处理（无需本机证书）。留空=走本地 Caddy 容器反代（3000 仅 127.0.0.1）。
#               例：INGRESS_PORT=443 配云「HTTP 代理 内网443→域名」规则，https://域名 直达 new-api。
INGRESS_PORT="${INGRESS_PORT:-}"

# 启动后健康检查轮询参数（跨境连 DB 启动偏慢，默认给 80s）
HEALTH_WAIT_ROUNDS="${HEALTH_WAIT_ROUNDS:-40}"
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
  err "未检测到 docker。海外机请先安装 Docker：sudo bash ~/install-docker.sh（见 install-docker.md）。"
  exit 1
fi

# 组装跨境连接串（指向国内公网 IP+端口，非容器名）
sql_dsn="${MYSQL_USER}:${MYSQL_PASSWORD}@tcp(${CHINA_HOST}:${MYSQL_PORT})/${MYSQL_DB}"
redis_conn="redis://${REDIS_USER}:${REDIS_PASSWORD}@${CHINA_HOST}:${REDIS_PORT}"

# ---------------- 前置：跨境连通性自检 ----------------
# 优先用 nc，没有则用 bash 内建 /dev/tcp（带 timeout）
tcp_reachable() {
  local host="$1" port="$2"
  if command -v nc >/dev/null 2>&1; then
    nc -z -w 8 "$host" "$port" >/dev/null 2>&1
  else
    timeout 8 bash -c "printf '' >/dev/tcp/$host/$port" >/dev/null 2>&1
  fi
}

preflight_remote() {
  log "前置连通性自检：国内 ${CHINA_HOST} 的 MySQL:${MYSQL_PORT} / Redis:${REDIS_PORT} ..."
  local fail=0
  if tcp_reachable "${CHINA_HOST}" "${MYSQL_PORT}"; then
    ok "MySQL 端口可达：${CHINA_HOST}:${MYSQL_PORT}"
  else
    err "MySQL 端口不可达：${CHINA_HOST}:${MYSQL_PORT}"
    err "  → 国内云安全组未放行 ${MYSQL_PORT} 给本海外机 IP，或国内 mysql8 容器未运行。"
    fail=1
  fi
  if tcp_reachable "${CHINA_HOST}" "${REDIS_PORT}"; then
    ok "Redis 端口可达：${CHINA_HOST}:${REDIS_PORT}"
  else
    err "Redis 端口不可达：${CHINA_HOST}:${REDIS_PORT}"
    err "  → 国内云安全组未放行 ${REDIS_PORT} 给本海外机 IP，或国内 redis6 容器未运行。"
    fail=1
  fi
  if (( fail )); then
    err "前置连通性失败：先确认国内 3306/6379 对本海外机可达（见 deploy-newapi.md「四、前置：国内端口放行」）。"
    exit 1
  fi
  # 可达 ≠ 已限制来源、≠ 已加密：Docker 发布端口绕过 ufw/firewalld 的 INPUT 规则，
  # 仅云安全组能真正限制来源；且跨公网为明文。务必按文档「四」确认来源限制、按「十二」上加密隧道。
  warn "可达仅代表 TCP 能通，不代表已限定为本机 IP、也不代表已加密。"
  if [[ "${MYSQL_PASSWORD}" == "622851Tt." || "${REDIS_PASSWORD}" == "622851Tt." ]]; then
    warn "仍在使用示例密码 622851Tt.（已提交仓库、且跨公网明文传输）。生产必须改强随机密码（见文档「十一」）。"
  fi
  if [[ -z "${SESSION_SECRET}" ]]; then
    warn "SESSION_SECRET 未设置：本机每次重启会话失效；多节点建议两端设同一随机串（见文档「二、多节点配置要点」）。"
  fi
  if [[ "${BATCH_UPDATE_ENABLED}" == "true" ]]; then
    warn "BATCH_UPDATE_ENABLED=true：跨境批次 flush 失败会丢该批增量；且若国内一端 batch=true，缓存回填覆盖原子增量会导致计费偏差。建议两端都 false（见文档「二」）。"
  fi
}

# ---------------- 网络与目录 ----------------
ensure_network() {
  if docker network ls --format '{{.Name}}' | grep -qx "${SHARED_NETWORK}"; then
    log "共享网络 ${SHARED_NETWORK} 已存在。"
  else
    log "创建共享网络 ${SHARED_NETWORK}..."
    docker network create "${SHARED_NETWORK}" >/dev/null
    ok "网络 ${SHARED_NETWORK} 已创建。"
  fi
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

  # 用数组承载 docker 参数，避免字符串拼接后未加引号展开把含空格/换行的值
  # （如 TRUSTED_PROXIES 的「逗号+空格」列表、base64 形式密钥）错误分词成多个 docker 参数。
  # args 恒非空（--name 等在前），故 "${args[@]}" 在 set -u 下安全，无需空数组守卫。
  local -a args=()
  args+=("--name" "${NEWAPI_CONTAINER}" "--restart" "always" "--network" "${SHARED_NETWORK}")

  # 端口映射：
  #   DEBUG_BIND 非空 → 仅该 IP 可访问 3000（调试）
  #   DOMAIN 非空 + INGRESS_PORT 非空 → 外部/云反代模式：new-api 监听宿主机 INGRESS_PORT
  #                     （0.0.0.0），供云端 HTTP 代理（域名 → 本机 INGRESS_PORT）回源，TLS 由云端边缘处理。
  #                     例：INGRESS_PORT=443 配云「HTTP 代理 内网443→域名」规则，https://域名 直达。
  #   DOMAIN 非空 + INGRESS_PORT 空   → 本地 Caddy 容器反代：3000 仅 127.0.0.1 回源，
  #                     Caddy 在 newapi-net 内用 new-api:3000 回源（与国内 deploy-newapi.sh 同效）。
  #   两者皆空       → 0.0.0.0:3000 海外对外直连（经云「端口映射」暴露为公网端口）
  if [[ -n "${DEBUG_BIND}" ]]; then
    args+=("-p" "${DEBUG_BIND}:3000:3000")
  elif [[ -n "${DOMAIN}" ]]; then
    if [[ -n "${INGRESS_PORT}" ]]; then
      args+=("-p" "${INGRESS_PORT}:3000")
    else
      args+=("-p" "127.0.0.1:3000:3000")
    fi
  else
    args+=("-p" "${NEWAPI_PORT}:3000")
  fi

  # 会话/反代 env：DOMAIN 非空时强制 SECURE=true + TRUSTED_URL(代码强绑定,缺一启动报错)
  if [[ -n "${DOMAIN}" ]]; then
    args+=("-e" "SESSION_COOKIE_SECURE=true" "-e" "SESSION_COOKIE_TRUSTED_URL=https://${DOMAIN}")
    [[ -n "${SESSION_SECRET}"  ]] && args+=("-e" "SESSION_SECRET=${SESSION_SECRET}")
    [[ -n "${TRUSTED_PROXIES}" ]] && args+=("-e" "TRUSTED_PROXIES=${TRUSTED_PROXIES}")
  elif [[ -n "${SESSION_SECRET}" ]]; then
    # 无域名(本地 HTTP)：SESSION_SECRET 若显式提供也注入(单机也建议设)
    args+=("-e" "SESSION_SECRET=${SESSION_SECRET}")
  fi

  args+=("-e" "SQL_DSN=${sql_dsn}" "-e" "REDIS_CONN_STRING=${redis_conn}" \
         "-e" "NODE_TYPE=${NODE_TYPE}" "-e" "NODE_NAME=${NODE_NAME}" \
         "-e" "BATCH_UPDATE_ENABLED=${BATCH_UPDATE_ENABLED}" "-e" "TZ=${TZ}" \
         "-e" "ERROR_LOG_ENABLED=${ERROR_LOG_ENABLED}" \
         "-v" "${NEWAPI_DATA_DIR}:/data" "-v" "${NEWAPI_LOG_DIR}:/app/logs")

  log "启动 new-api 容器（角色=${NODE_TYPE}，节点=${NODE_NAME}，复用国内 MySQL+Redis）..."
  docker run -d "${args[@]}" "${NEWAPI_IMAGE}" --log-dir /app/logs
}

# 等待 new-api 自身 HTTP 接口就绪（跨境连 DB 启动偏慢，轮询上限见 HEALTH_WAIT_ROUNDS）
wait_healthy() {
  log "等待 new-api 就绪（最多 $((HEALTH_WAIT_ROUNDS * HEALTH_WAIT_INTERVAL)) 秒，跨境连库可能偏慢）..."
  local i=0
  until docker exec "${NEWAPI_CONTAINER}" \
      wget -q -O - http://localhost:3000/api/status 2>/dev/null \
      | grep -q '"success"[[:space:]]*:[[:space:]]*true'; do
    i=$((i+1))
    if (( i >= HEALTH_WAIT_ROUNDS )); then
      warn "new-api 未在预期时间内就绪。"
      err "  常见原因：跨境连库超时 / 凭据不匹配 / 国内防火墙中途断开。"
      err "  排查：docker logs ${NEWAPI_CONTAINER} --tail 100   看是否报 connection refused / auth failed。"
      return 1
    fi
    sleep "${HEALTH_WAIT_INTERVAL}"
  done
  ok "new-api 已就绪。"
}

cmd_start() {
  preflight_remote
  ensure_network
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
  if [[ -n "${DEBUG_BIND}" ]]; then
    ok "new-api 已启动：本地直连 http://${DEBUG_BIND}:3000（仅本机，公网不可直连）"
  elif [[ -n "${DOMAIN}" ]]; then
    if [[ -n "${INGRESS_PORT}" ]]; then
      ok "new-api 已启动：https://${DOMAIN}（本机 ${INGRESS_PORT} → 3000，云端 HTTP 代理回源，TLS 由云端边缘处理）"
    else
      ok "new-api 已启动：反代 https://${DOMAIN}（3000 仅 127.0.0.1 回源，本地 Caddy 容器回源，公网不直连）"
    fi
  else
    ok "new-api 已启动：本机 3000 就绪（复用国内 ${CHINA_HOST} 的 MySQL+Redis，角色 ${NODE_TYPE}）。"
    ok "公网访问经云控制台「端口映射」：内网 3000 → 云分配的公网端口（非 3000，本例 10016），访问 http://38.226.195.219:<公网端口>（见 deploy-newapi.md「六、3」）。"
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
  echo "---- 连接目标 ----"
  echo "  MySQL : ${MYSQL_USER}@${CHINA_HOST}:${MYSQL_PORT}/${MYSQL_DB}"
  echo "  Redis : ${REDIS_USER}@${CHINA_HOST}:${REDIS_PORT}"
  echo "  角色   : ${NODE_TYPE}   节点: ${NODE_NAME}   批量更新: ${BATCH_UPDATE_ENABLED}"
  echo "---- 容器 ----"
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
    log "跨境 DB/Redis 端口探测："
    if tcp_reachable "${CHINA_HOST}" "${MYSQL_PORT}"; then ok "MySQL ${CHINA_HOST}:${MYSQL_PORT} 可达。"; else warn "MySQL ${CHINA_HOST}:${MYSQL_PORT} 不可达。"; fi
    if tcp_reachable "${CHINA_HOST}" "${REDIS_PORT}"; then ok "Redis ${CHINA_HOST}:${REDIS_PORT} 可达。"; else warn "Redis ${CHINA_HOST}:${REDIS_PORT} 不可达。"; fi
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
  log "更新模式：拉取最新镜像并重建容器（配置/数据由卷保留；改配置后用它生效）..."
  preflight_remote
  ensure_network
  ensure_dirs

  # 先拉镜像成功再移除旧容器：避免拉取失败（Docker Hub 限流/跨境抖动/镜像下架）时
  # 把节点直接下线——旧容器一旦 rm，--restart 也救不回来。
  log "拉取最新镜像 ${NEWAPI_IMAGE}..."
  docker pull "${NEWAPI_IMAGE}"

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
  start     首次部署或启动 new-api 容器（含跨境连通性自检）
  stop      停止 new-api 容器（不删除，数据保留）
  restart   重启 new-api 容器（stop + start；复用旧容器，不改 env）
  status    查看运行状态、接口健康与跨境连接目标
  logs      跟随查看日志（Ctrl-C 退出）
  update    拉取最新镜像并重建容器（改可配置项后用它生效）

提示: 改任何可配置项后用 update（而非 restart）才会生效。
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
