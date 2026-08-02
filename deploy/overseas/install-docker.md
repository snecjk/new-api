# 海外机 Docker 安装脚本使用说明

本脚本在**海外服务器**上安装 **Docker CE + docker compose 插件 + git**，供后续用
[`deploy-newapi.sh`](deploy-newapi.sh) 部署 new-api（new-api 容器复用国内 `140.143.183.34`
的 MySQL+Redis，海外机本地**不装 Redis/MySQL**；deploy 脚本从 fork 克隆源码并本地构建镜像，故需 git）。

配套脚本：[`install-docker.sh`](install-docker.sh)

> 与国内 [`install-docker-redis-mysql.sh`](../china/install-docker-redis-mysql.sh) 的区别：
> 本脚本**装 Docker + git**（不装 Redis/MySQL）；镜像源默认用官方 `download.docker.com`（海外东京可直连），不走腾讯云内网镜像；
> Docker Hub 不配镜像加速（海外直连即可）。三者均可经环境变量改回国内镜像。

---

## 一、环境要求

| 项目 | 要求 |
|------|------|
| 操作系统 | Ubuntu 18.04+ / Debian 10+（也兼容 CentOS/RHEL yum 系） |
| 权限 | root（`sudo`） |
| 架构 | x86_64 / ARM64 |
| 网络 | 能访问 `download.docker.com`、`registry-1.docker.io`、`github.com`（东京 ap-northeast-1 直连无碍；后者用于 deploy-newapi.sh 从 fork 克隆源码） |

---

## 二、默认配置

| 项 | 默认值 | 说明 |
|------|--------|------|
| Docker | CE 最新稳定版（`docker-ce` + `cli` + `containerd.io` + `buildx` + `compose` 插件） | 官方 apt/yum 源 |
| git | 系统源自带 `git` | deploy-newapi.sh 从 fork 克隆源码并本地构建需要 |
| apt 源根 | `https://download.docker.com` | 海外官方源，全球 CDN；连不上时换国内镜像（见下） |
| Docker Hub 加速 | （空，直连） | 海外无需加速；需要时填加速器 URL |
| 开机自启 | 是（`systemctl enable --now docker`） | |
| 普通用户免 sudo | （可选）`DOCKER_GROUP_USER=<用户名>` | 加入 docker 组，需重新登录生效 |

> 国内镜像（连官方慢时用，填到 `/docker-ce` 为止）：
> - 阿里云：`https://mirrors.aliyun.com/docker-ce`（公开，东京可达）
> - 腾讯云：`https://mirrors.tencentyun.com/docker-ce`（仅腾讯云内网可达）
> - 清华：`https://mirrors.tuna.tsinghua.edu.cn/docker-ce`
>
> 脚本按 `${DOCKER_APT_MIRROR}/linux/ubuntu` 拼路径——官方 `download.docker.com` 与上述国内镜像
> 在 `/linux/ubuntu` 下一层都一致，故填到 `/docker-ce` 为止即可（官方源直接填根 `download.docker.com`）。

---

## 三、开始部署

### 1. 上传脚本

在**本地终端**执行（海外机 SSH 端口 `10009`）：

```bash
scp -P 10009 deploy/overseas/install-docker.sh root@38.226.195.219:~/install-docker.sh
```

### 2. 在海外服务器上执行

```bash
ssh -p 10009 root@38.226.195.219 'sudo bash ~/install-docker.sh'
```

> 普通用户执行 `docker` 命令需 `sudo`。想免 `sudo` 跑日常 docker 命令：
> `sudo DOCKER_GROUP_USER=<你的用户名> bash ~/install-docker.sh`（加完重新登录该用户生效）。
> 但 `deploy-newapi.sh` 本身要求 root（`sudo`），是否加 docker 组不影响它。

脚本流程：

- 检测并以 root 身份运行（非 root 报错退出）
- 检测包管理器（apt / yum）
- 已装 Docker 则跳过安装；否则装 Docker CE + compose + containerd + buildx
- `systemctl enable --now docker` 启动并开机自启
- 按需写 `/etc/docker/daemon.json`（仅当显式设了 `DOCKER_REGISTRY_MIRROR`）
- 装 git（已装则跳过；deploy-newapi.sh 从 fork 克隆源码并本地构建镜像需要）
- 可选：把 `DOCKER_GROUP_USER` 加入 docker 组
- 自检：`docker version` + `docker compose version` + `git --version` + `docker run --rm hello-world`

---

## 四、连接验证

```bash
docker version
```

```bash
docker compose version
```

```bash
docker run --rm hello-world
```

```bash
git ls-remote https://github.com/snecjk/new-api.git HEAD
```

> `hello-world` 拉自 Docker Hub，确认能拉镜像（deploy 构建时会拉 `oven/bun`、`golang`、`debian` 等基础镜像）。
> `git ls-remote` 确认能连 `github.com`（deploy-newapi.sh 从该 fork 克隆源码并本地构建）。

---

## 五、常用运维命令

```bash
systemctl status docker
```

```bash
systemctl restart docker
```

```bash
docker ps
```

```bash
journalctl -u docker --tail 100
```

```bash
docker logs new-api
```

卸载（如需）：

```bash
apt-get purge -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
rm -rf /var/lib/docker /etc/docker /etc/apt/sources.list.d/docker.list /etc/apt/keyrings/docker.gpg
```

---

## 六、故障排查

| 现象 | 排查 |
|------|------|
| `docker: command not found` | 脚本未装上，检查网络/源；`apt-get update` 是否报错，GPG key 是否拉到 |
| `permission denied ... docker.sock` | 普通用户无权访问 docker socket，命令前加 `sudo`，或 `sudo usermod -aG docker <用户>` 后重新登录 |
| GPG key 拉取失败 / `curl: (7) Failed to connect` | `download.docker.com` 被绕路或阻断；换国内镜像重跑：`sudo DOCKER_APT_MIRROR=https://mirrors.aliyun.com/docker-ce bash ~/install-docker.sh` |
| `apt-get update` 报 `Release file ... not ready yet` / 校验和不符 | 时钟偏移；`chronyc makestep` 或 `ntpdate` 对时后重跑 |
| `docker run hello-world` 拉取失败 | Docker Hub 抖动；重试或设 `DOCKER_REGISTRY_MIRROR` 加速器重跑（deploy 构建时拉 `oven/bun`、`golang` 等基础镜像也走 Docker Hub） |
| `Failed to start docker.service` | `journalctl -u docker --tail 100` 看具体原因；常见 `/var/lib/docker` 损坏或磁盘满 |
| 主机密钥验证失败（SSH） | `ssh-keygen -R "[38.226.195.219]:10009"` 后重连，确认服务器确为你本人所有 |

---

## 七、装完之后

Docker + git 装好后，按 [`deploy-newapi.md`](deploy-newapi.md) 部署 new-api（复用国内 `140.143.183.34`
的 MySQL+Redis，海外以 `slave` 角色运行）。`deploy-newapi.sh` 会从你的 fork
`https://github.com/snecjk/new-api.git` 克隆源码并在海外机本地 `docker build`（首次约 3-8 分钟），
无需手动构建/推送镜像：

```bash
scp -P 10009 deploy/overseas/deploy-newapi.sh root@38.226.195.219:~/deploy-newapi.sh
```

```bash
ssh -p 10009 root@38.226.195.219 'sudo bash ~/deploy-newapi.sh start'
```
