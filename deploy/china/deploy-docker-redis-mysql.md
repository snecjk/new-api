# 服务器部署 Docker + Redis + MySQL 教程

本教程在 Ubuntu/Debian 服务器上一键完成以下部署：

1. 安装 Docker 与 docker compose 插件
2. 启动 **Redis 6.0** 容器（账号 `root` / 密码 `622851Tt.`）
3. 启动 **MySQL 8.0** 容器（账号 `root` / 密码 `622851Tt.`）

配套脚本：[`install-docker-redis-mysql.sh`](install-docker-redis-mysql.sh)

---

## 一、环境要求

| 项目 | 要求 |
|------|------|
| 操作系统 | Ubuntu 18.04+ / Debian 10+（也兼容 CentOS/RHEL yum 系） |
| 权限 | root（`sudo`） |
| 端口 | Redis `6379`、MySQL `3306`（云厂商安全组需放行） |
| 架构 | x86_64 / ARM64 |

---

## 二、默认配置

| 服务 | 容器名 | 端口 | 账号 | 密码 | 数据持久化目录 |
|------|--------|------|------|------|----------------|
| Redis 6.0 | `redis6` | `6379` | `root`（ACL 用户） | `622851Tt.` | `/data/redis` |
| MySQL 8.0 | `mysql8` | `3306` | `root` | `622851Tt.` | `/data/mysql` |

> 说明：Redis 6+ 才支持 ACL 用户名。脚本在启动前**预写 `users.acl` 作为唯一鉴权源**，同时启用 `default` 与 `root` 两个用户、密码相同。注意：不与 `--requirepass` 混用——两者共存会导致 Redis 6.0 启动崩溃、容器进入 restart 循环；MySQL 使用标准 `root` 用户。

如需修改端口、密码、版本、镜像源等，编辑脚本顶部的“可配置项”区块即可（含 Docker apt/yum 源镜像与 Docker Hub 镜像加速，默认腾讯云内网镜像，国内访问更稳）。

---

## 三、开始部署

### 1. 上传脚本到服务器

在**本地终端**执行（把脚本拷贝到服务器）：

```bash
ssh ubuntu@<服务器IP> 'rm -f ~/install.sh'
scp deploy/china/install-docker-redis-mysql.sh ubuntu@<服务器IP>:~/install.sh
```

> 若 SSH 提示 `REMOTE HOST IDENTIFICATION HAS CHANGED`（重装/更换服务器后常见），先执行 `ssh-keygen -R <服务器IP>` 清掉旧主机密钥再重连。

### 2. 在服务器上执行

```bash
sudo bash ~/install.sh
```

> 普通用户执行 `docker` 命令需要 `sudo`。若想免 `sudo`，一次性把当前用户加入 docker 组后**重新登录**生效：`sudo usermod -aG docker ubuntu`。

脚本流程：

- 检测并以 root 身份运行（非 root 报错退出）
- 检测包管理器（apt / yum）
- 安装 Docker（已装则跳过），并 `systemctl enable --now docker`；配置 Docker Hub 镜像加速（默认腾讯云内网镜像）
- 拉取 `redis:6.0` / `mysql:8.0` 镜像
- 预写 Redis ACL 文件（`default` + `root` 两个用户，密码相同）
- 启动两个容器（同名旧容器先 `rm -f` 再重建，可安全重跑）
- 轮询等待 Redis / MySQL 就绪
- 创建 MySQL 业务库 `new-api` 并按需开放 `root@%` 远程登录
- 自检：`redis-cli PING` + `mysql SELECT VERSION()`

执行成功后，终端会打印连接信息汇总。

---

## 四、连接验证

### Redis

```bash
# 在服务器上
docker exec -it redis6 redis-cli -a 622851Tt.
# 或用 ACL 用户名
docker exec -it redis6 redis-cli --user root -a 622851Tt.
```

远端连接（需放行 6379）：

```bash
redis-cli -h <服务器IP> -p 6379 --user root -a 622851Tt.
127.0.0.1:6379> PING
PONG
```

### MySQL

```bash
# 在服务器上
docker exec -it mysql8 mysql -uroot -p622851Tt.
```

远端连接（需放行 3306）：

```bash
mysql -h <服务器IP> -P 3306 -uroot -p622851Tt.
```

---

## 五、常用运维命令

```bash
# 查看运行中的容器
docker ps

# 查看日志
docker logs redis6
docker logs mysql8

# 重启
docker restart redis6 mysql8

# 停止 / 启动
docker stop redis6 mysql8
docker start redis6 mysql8

# 进入容器
docker exec -it redis6 redis-cli -a 622851Tt.
docker exec -it mysql8 mysql -uroot -p622851Tt.
```

---

## 六、数据与重置

- 数据持久化在宿主机 `/data/redis` 与 `/data/mysql`，容器删除后数据仍在。
- 完全重置（会清空所有数据）：

  ```bash
  docker rm -f redis6 mysql8
  rm -rf /data/redis /data/mysql
  sudo bash ~/install.sh
  ```

---

## 七、安全建议

生产环境请至少做以下调整：

1. **不要用示例密码**：把 `622851Tt.` 改成强随机密码。
2. **不要直接暴露公网端口**：用安全组/防火墙限制 `3306`/`6379` 来源 IP，或仅绑定 `127.0.0.1` 后通过 SSH 隧道访问。
3. **Redis 禁用高危命令**（按需）：在启动参数追加 `--rename-command FLUSHALL ""` 等。
4. **MySQL 建独立业务账号**：不要用 `root` 连接业务应用，`CREATE USER` + 最小权限授权。
5. **定期备份**：`/data/mysql` 可用 `mysqldump` 或直接快照卷。

---

## 八、故障排查

| 现象 | 排查 |
|------|------|
| `docker: command not found` | 脚本未装上，检查网络/源，手动 `apt-get install docker-ce` |
| `permission denied ... docker.sock` | 普通用户无权访问 docker socket，命令前加 `sudo`，或 `sudo usermod -aG docker ubuntu` 后重新登录 |
| `Connection reset by peer`（拉 GPG key） | 国内访问 `download.docker.com` 不稳定，脚本已默认走腾讯云镜像；确认 `DOCKER_APT_MIRROR` 未被改回官方源 |
| Redis 容器反复 `Restarting` | 多为 `--requirepass` 与 `--aclfile` 共存导致 6.0 启动崩溃（脚本已规避）。`sudo docker logs redis6 --tail 40` 看真实原因；必要时 `rm -f /data/redis/users.acl` 后重跑让脚本重写 |
| 端口无法远端连接 | 云安全组未放行 / 服务器 `ufw`/`firewalld` 未放行 |
| MySQL 启动超时 | `docker logs mysql8` 看初始化日志；确认 `/data/mysql` 可写、磁盘充足 |
| 主机密钥验证失败（SSH） | `ssh-keygen -R <服务器IP>` 后重连，并确认服务器确为你本人所有 |
