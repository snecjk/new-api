#!/usr/bin/env bash
#
# install-docker-redis-mysql.sh
#
# 在 Ubuntu/Debian 服务器上一键完成：
#   1. 安装 Docker 与 docker compose 插件
#   2. 启动 Redis 6.0 容器（账号 root / 密码 622851Tt.）
#   3. 启动 MySQL  8.0 容器（账号 root / 密码 622851Tt.）
#
# 用法（在服务器上）：
#   sudo bash install-docker-redis-mysql.sh
#
# 端口映射：
#   Redis -> 主机 6379
#   MySQL  -> 主机 3306
# 数据持久化目录：
#   /data/redis  /data/mysql
#
# 需要以 root 身份运行（安装 Docker 需要特权）。

set -euo pipefail

# ---------------- 可配置项 ----------------
REDIS_PASSWORD="622851Tt."
REDIS_USER="root"            # Redis 6+ ACL 用户名
MYSQL_ROOT_PASSWORD="622851Tt."
MYSQL_ROOT_USER="root"

REDIS_PORT=6379
MYSQL_PORT=3306

REDIS_VERSION="6.0"
MYSQL_VERSION="8.0"

REDIS_CONTAINER="redis6"
MYSQL_CONTAINER="mysql8"

REDIS_DATA_DIR="/data/redis"
MYSQL_DATA_DIR="/data/mysql"

# 业务库（供 new-api 使用；库名带连字符，SQL 中用反引号包裹）
MYSQL_DB="new-api"
# 是否开放 root 远程登录（root@%），便于本地/外部连接调试
MYSQL_ALLOW_REMOTE_ROOT="true"

# Docker apt 源镜像（国内访问 download.docker.com 不稳定，默认用腾讯云内网镜像，
# 不走公网流量且最快；公网 VM 可改为 https://mirrors.aliyun.com 或
# https://mirrors.tuna.tsinghua.edu.cn）
DOCKER_APT_MIRROR="https://mirrors.tencentyun.com"
# Docker yum 源镜像（CentOS/RHEL）
DOCKER_YUM_MIRROR="https://mirrors.tencentyun.com"
# Docker Hub 镜像加速（拉取 redis/mysql 等），留空则不配置
DOCKER_REGISTRY_MIRROR="https://mirror.ccs.tencentyun.com"
# ----------------------------------------

log()  { echo -e "\033[34m[$(date '+%H:%M:%S')]\033[0m $*"; }
ok()   { echo -e "\033[32m[OK]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*" >&2; }

if [[ $EUID -ne 0 ]]; then
  err "请以 root 身份运行：sudo bash $0"
  exit 1
fi

# 检测包管理器，兼容 Ubuntu/Debian
if [[ -x /usr/bin/apt-get ]]; then
  PKG_MGR="apt"
elif [[ -x /usr/bin/yum ]]; then
  PKG_MGR="yum"
else
  err "仅支持 Debian/Ubuntu(apt) 或 RHEL/CentOS(yum)，当前系统不支持。"
  exit 1
fi

# ---------------- 1. 安装 Docker ----------------
install_docker() {
  if command -v docker >/dev/null 2>&1; then
    ok "已检测到 Docker：$(docker --version)"
  else
    log "安装 Docker..."
    if [[ "$PKG_MGR" == "apt" ]]; then
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -y
      apt-get install -y ca-certificates curl gnupg lsb-release
      install -m 0755 -d /etc/apt/keyrings
      # 拉取 GPG key：优先镜像，失败再回退官方源
      if ! curl -fsSL "${DOCKER_APT_MIRROR}/docker-ce/linux/ubuntu/gpg" \
          | gpg --dearmor -o /etc/apt/keyrings/docker.gpg 2>/dev/null; then
        curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
          | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
      fi
      chmod a+r /etc/apt/keyrings/docker.gpg
      echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
${DOCKER_APT_MIRROR}/docker-ce/linux/ubuntu $(. /etc/os-release; echo "$VERSION_CODENAME") stable" \
        > /etc/apt/sources.list.d/docker.list
      apt-get update -y
      apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    else
      yum install -y yum-utils
      yum-config-manager --add-repo "${DOCKER_YUM_MIRROR}/docker-ce/linux/centos/docker-ce.repo"
      yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    fi
    systemctl enable --now docker
    ok "Docker 安装完成。"
  fi

  # 确保守护进程已启动
  systemctl is-active --quiet docker || systemctl start docker

  # 配置 Docker Hub 镜像加速（拉取 redis/mysql 等）
  if [[ -n "${DOCKER_REGISTRY_MIRROR}" ]] && ! grep -q "${DOCKER_REGISTRY_MIRROR}" /etc/docker/daemon.json 2>/dev/null; then
    mkdir -p /etc/docker
    cat > /etc/docker/daemon.json <<JSON
{"registry-mirrors":["${DOCKER_REGISTRY_MIRROR}"]}
JSON
    systemctl restart docker
  fi
}

# ---------------- 2. 启动 Redis 6.0 ----------------
start_redis() {
  log "拉取 Redis ${REDIS_VERSION} 镜像..."
  docker pull redis:"${REDIS_VERSION}"

  # 兼容已有同名容器：存在则先删除重建
  if docker ps -a --format '{{.Names}}' | grep -qx "${REDIS_CONTAINER}"; then
    log "移除已存在的容器 ${REDIS_CONTAINER}..."
    docker rm -f "${REDIS_CONTAINER}" >/dev/null
  fi

  mkdir -p "${REDIS_DATA_DIR}"

  # 预写 ACL 文件（Redis 6.0）：同时启用 default 与 root 两个用户，密码相同。
  # 以 aclfile 作为唯一鉴权来源，不再与 --requirepass 混用——两者共存会导致
  # Redis 6.0 启动崩溃、容器进入 restart 循环。已存在则不覆盖，保留运行期改动。
  if [[ ! -f "${REDIS_DATA_DIR}/users.acl" ]]; then
    cat > "${REDIS_DATA_DIR}/users.acl" <<ACL
user default on >${REDIS_PASSWORD} ~* +@all
user ${REDIS_USER} on >${REDIS_PASSWORD} ~* +@all
ACL
  fi

  log "启动 Redis 容器..."
  docker run -d \
    --name "${REDIS_CONTAINER}" \
    --restart always \
    -p "${REDIS_PORT}:6379" \
    -v "${REDIS_DATA_DIR}:/data" \
    redis:"${REDIS_VERSION}" \
    redis-server \
      --appendonly yes \
      --aclfile /data/users.acl

  # 等待 Redis 就绪
  local i=0
  until docker exec "${REDIS_CONTAINER}" redis-cli -a "${REDIS_PASSWORD}" --no-auth-warning PING >/dev/null 2>&1; do
    i=$((i+1))
    if (( i >= 15 )); then
      err "Redis 启动超时，用 docker logs ${REDIS_CONTAINER} 查看日志。"
      exit 1
    fi
    sleep 1
  done

  ok "Redis 已启动：端口 ${REDIS_PORT}，账号 ${REDIS_USER} / default，密码均为 ${REDIS_PASSWORD}（ACL 文件已落盘）"
}

# ---------------- 3. 启动 MySQL 8.0 ----------------
start_mysql() {
  log "拉取 MySQL ${MYSQL_VERSION} 镜像..."
  docker pull mysql:"${MYSQL_VERSION}"

  if docker ps -a --format '{{.Names}}' | grep -qx "${MYSQL_CONTAINER}"; then
    log "移除已存在的容器 ${MYSQL_CONTAINER}..."
    docker rm -f "${MYSQL_CONTAINER}" >/dev/null
  fi

  mkdir -p "${MYSQL_DATA_DIR}"

  log "启动 MySQL 容器..."
  docker run -d \
    --name "${MYSQL_CONTAINER}" \
    --restart always \
    -p "${MYSQL_PORT}:3306" \
    -e MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD}" \
    -v "${MYSQL_DATA_DIR}:/var/lib/mysql" \
    mysql:"${MYSQL_VERSION}" \
    --default-authentication-plugin=mysql_native_password \
    --character-set-server=utf8mb4 \
    --collation-server=utf8mb4_unicode_ci

  log "等待 MySQL 初始化完成（最多 60 秒）..."
  local i=0
  until docker exec "${MYSQL_CONTAINER}" mysqladmin ping -h127.0.0.1 \
      -uroot -p"${MYSQL_ROOT_PASSWORD}" --silent 2>/dev/null; do
    i=$((i+1))
    if (( i >= 30 )); then
      err "MySQL 启动超时，请用 docker logs ${MYSQL_CONTAINER} 查看日志。"
      exit 1
    fi
    sleep 2
  done

  ok "MySQL 已启动：端口 ${MYSQL_PORT}，账号 ${MYSQL_ROOT_USER} / 密码 ${MYSQL_ROOT_PASSWORD}"
}

# ---------------- 3.1 创建业务库 + 开放 root 远程登录 ----------------
setup_mysql_db() {
  log "创建业务库 \`${MYSQL_DB}\` 并配置 root 远程登录..."

  # 按需拼出“开放 root 远程登录”那段 SQL，避免在 heredoc 里嵌套子 shell/heredoc
  local remote_sql=""
  if [[ "${MYSQL_ALLOW_REMOTE_ROOT}" == "true" ]]; then
    remote_sql="
CREATE USER IF NOT EXISTS 'root'@'%' IDENTIFIED WITH mysql_native_password BY '${MYSQL_ROOT_PASSWORD}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' WITH GRANT OPTION;
"
  fi

  docker exec -i "${MYSQL_CONTAINER}" mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" <<SQL
CREATE DATABASE IF NOT EXISTS \`${MYSQL_DB}\` DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_unicode_ci;
${remote_sql}
FLUSH PRIVILEGES;
SQL
  if [[ "${MYSQL_ALLOW_REMOTE_ROOT}" == "true" ]]; then
    ok "业务库 \`${MYSQL_DB}\` 已创建；root 已开放远程登录（root@%）"
  else
    ok "业务库 \`${MYSQL_DB}\` 已创建（未开放 root 远程登录）"
  fi
}

# ---------------- 4. 自检 ----------------
verify() {
  log "自检中..."
  docker exec "${REDIS_CONTAINER}" redis-cli -u "redis://${REDIS_USER}:${REDIS_PASSWORD}@127.0.0.1:${REDIS_PORT}" \
    --no-auth-warning PING || err "Redis 连接失败"
  docker exec "${MYSQL_CONTAINER}" mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" -e "SELECT VERSION();" \
    || err "MySQL 连接失败"
  ok "全部就绪。"
}

install_docker
start_redis
start_mysql
setup_mysql_db
verify

cat <<EOF

================ 部署完成 ================
Redis 6.0
  容器名 : ${REDIS_CONTAINER}
  端口   : ${REDIS_PORT}
  数据   : ${REDIS_DATA_DIR}
  连接   : redis://${REDIS_USER}:${REDIS_PASSWORD}@<服务器IP>:${REDIS_PORT}

MySQL 8.0
  容器名 : ${MYSQL_CONTAINER}
  端口   : ${MYSQL_PORT}
  数据   : ${MYSQL_DATA_DIR}
  业务库 : ${MYSQL_DB}
  远程root: $( [[ "${MYSQL_ALLOW_REMOTE_ROOT}" == "true" ]] && echo "已开放 (root@%)" || echo "未开放" )
  连接   : mysql -h<服务器IP> -P${MYSQL_PORT} -u${MYSQL_ROOT_USER} -p${MYSQL_ROOT_PASSWORD} ${MYSQL_DB}

常用命令:
  docker ps
  docker logs ${REDIS_CONTAINER}
  docker logs ${MYSQL_CONTAINER}
  docker restart ${REDIS_CONTAINER} ${MYSQL_CONTAINER}
========================================
EOF
