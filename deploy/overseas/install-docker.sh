#!/usr/bin/env bash
#
# install-docker.sh  (海外节点 / overseas)
#
# 在海外服务器（Ubuntu/Debian，也兼容 RHEL/CentOS yum 系）上一键安装：
#   1. Docker CE + docker compose 插件 + containerd + buildx
#   2. 启动并开机自启 docker 守护进程
#   3.（可选）把指定用户加入 docker 组（免 sudo 跑 docker 命令）
#
# 与国内 install-docker-redis-mysql.sh 的区别：本脚本【只装 Docker，不装 Redis/MySQL】——
# 海外节点复用国内 140.143.183.34 上已部署好的 MySQL+Redis，本地不需要这两者。
# 镜像源默认用官方 download.docker.com（海外东京可直连）；Docker Hub 不配镜像加速
# （海外直连 Docker Hub 即可）。两者均可经环境变量改回国内镜像。
#
# 用法（在海外服务器上）：
#   sudo bash install-docker.sh
#   # 想让某普通用户免 sudo 跑 docker 命令（需该用户重新登录后生效）：
#   sudo DOCKER_GROUP_USER=ubuntu bash install-docker.sh
#
# 目标服务器（海外）：38.226.195.219   SSH 端口 10009   区域 ap-northeast-1（东京）
#
# 需要以 root 身份运行（安装需特权）。

set -euo pipefail

# ---------------- 可配置项（均支持环境变量覆盖）----------------
# Docker apt 源（根，不含 /linux/ubuntu）：海外默认官方 download.docker.com（全球 CDN，东京直连快）。
#   连官方慢时换国内公开镜像，填到 /docker-ce 为止，例如：
#     https://mirrors.aliyun.com/docker-ce          （公开，东京可达）
#     https://mirrors.tencentyun.com/docker-ce       （仅腾讯云内网可达）
#     https://mirrors.tuna.tsinghua.edu.cn/docker-ce
DOCKER_APT_MIRROR="${DOCKER_APT_MIRROR:-https://download.docker.com}"
# Docker yum 源（根，不含 /linux/centos）：同上。
DOCKER_YUM_MIRROR="${DOCKER_YUM_MIRROR:-https://download.docker.com}"
# Docker Hub 镜像加速：海外默认留空（直连 Docker Hub 即可）；需要时填加速器 URL。
DOCKER_REGISTRY_MIRROR="${DOCKER_REGISTRY_MIRROR:-}"
# 可选：把该用户加入 docker 组（免 sudo 跑 docker 命令）。留空=跳过。
DOCKER_GROUP_USER="${DOCKER_GROUP_USER:-}"
# ----------------------------------------

log()  { echo -e "\033[34m[$(date '+%H:%M:%S')]\033[0m $*"; }
ok()   { echo -e "\033[32m[OK]\033[0m $*"; }
warn() { echo -e "\033[33m[WARN]\033[0m $*"; }
err()  { echo -e "\033[31m[ERROR]\033[0m $*" >&2; }

if [[ $EUID -ne 0 ]]; then
  err "请以 root 身份运行：sudo bash $0"
  exit 1
fi

# 检测包管理器，兼容 Ubuntu/Debian 与 RHEL/CentOS
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
      rm -f /etc/apt/keyrings/docker.gpg
      # 拉取 GPG key：优先配置镜像，失败回退官方 download.docker.com
      if ! curl -fsSL "${DOCKER_APT_MIRROR}/linux/ubuntu/gpg" \
          | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg 2>/dev/null; then
        curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
          | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
      fi
      chmod a+r /etc/apt/keyrings/docker.gpg
      echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
${DOCKER_APT_MIRROR}/linux/ubuntu $(. /etc/os-release; echo "$VERSION_CODENAME") stable" \
        > /etc/apt/sources.list.d/docker.list
      apt-get update -y
      apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    else
      yum install -y yum-utils
      yum-config-manager --add-repo "${DOCKER_YUM_MIRROR}/linux/centos/docker-ce.repo"
      yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    fi
    systemctl enable --now docker
    ok "Docker 安装完成。"
  fi

  # 确保守护进程已启动
  systemctl is-active --quiet docker || systemctl start docker

  # 配置 Docker Hub 镜像加速（海外默认留空=不配置；显式设置时才写 daemon.json）
  if [[ -n "${DOCKER_REGISTRY_MIRROR}" ]] && ! grep -q "${DOCKER_REGISTRY_MIRROR}" /etc/docker/daemon.json 2>/dev/null; then
    mkdir -p /etc/docker
    cat > /etc/docker/daemon.json <<JSON
{"registry-mirrors":["${DOCKER_REGISTRY_MIRROR}"]}
JSON
    systemctl restart docker
  fi

  # 可选：把指定用户加入 docker 组（免 sudo 跑 docker 命令；需该用户重新登录后生效）
  if [[ -n "${DOCKER_GROUP_USER}" ]]; then
    if id "${DOCKER_GROUP_USER}" >/dev/null 2>&1; then
      usermod -aG docker "${DOCKER_GROUP_USER}"
      ok "用户 ${DOCKER_GROUP_USER} 已加入 docker 组（重新登录该用户后可免 sudo 跑 docker）。"
    else
      warn "用户 ${DOCKER_GROUP_USER} 不存在，跳过加入 docker 组。"
    fi
  fi
}

# ---------------- 2. 自检 ----------------
verify() {
  log "自检中..."
  if docker version >/dev/null 2>&1; then
    ok "docker 守护进程响应正常（$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',')）。"
  else
    err "docker 守护进程无响应，用 systemctl status docker 与 journalctl -u docker --tail 100 排查。"
    exit 1
  fi
  if docker compose version >/dev/null 2>&1; then
    ok "docker compose 插件就绪（$(docker compose version 2>/dev/null | awk '{print $NF}')）。"
  else
    warn "docker compose 插件未就绪（new-api 部署不强依赖，但建议补装 docker-compose-plugin）。"
  fi
  # 端到端冒烟：拉极小镜像跑起来（确认 daemon + 镜像拉取 + 容器运行 全链路通）
  log "端到端冒烟：docker run --rm hello-world ..."
  if docker run --rm hello-world >/dev/null 2>&1; then
    ok "hello-world 运行成功：daemon + 镜像拉取 + 容器运行 全链路正常。"
  else
    warn "hello-world 未跑通（多为 Docker Hub 拉取抖动；不影响后续部署，可用 docker pull calciumion/new-api:latest 单独验证）。"
  fi
}

install_docker
verify

cat <<EOF

================ Docker 安装完成 ================
海外机：38.226.195.219（ap-northeast-1 东京，SSH 10009）
Docker  : $(docker --version 2>/dev/null)
Compose : $(docker compose version 2>/dev/null)
apt 源  : ${DOCKER_APT_MIRROR}/linux/ubuntu
Hub 加速: ${DOCKER_REGISTRY_MIRROR:-无（直连 Docker Hub）}

下一步——上传并启动 new-api（复用国内 140.143.183.34 的 MySQL+Redis）：
  scp -P 10009 deploy/overseas/deploy-newapi.sh root@38.226.195.219:~/deploy-newapi.sh
  ssh -p 10009 root@38.226.195.219 'sudo bash ~/deploy-newapi.sh start'
（详见 deploy-newapi.md）
========================================
EOF
