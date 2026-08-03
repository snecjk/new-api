# New-API Docker 部署脚本使用说明

本脚本在服务器上把 **new-api** 以 docker 容器方式部署起来，并连接同一台机器上已由
[`install-docker-redis-mysql.sh`](install-docker-redis-mysql.sh) 部署好的 `redis6` / `mysql8` 容器。

配套脚本：[`deploy-newapi.sh`](deploy-newapi.sh)

> 前置条件：先按 [`deploy-docker-redis-mysql.md`](deploy-docker-redis-mysql.md) 完成 Docker、Redis、MySQL 的安装部署，确保 `redis6`、`mysql8` 两个容器已正常运行。

---

## 一、连接拓扑

new-api 容器与 `redis6` / `mysql8` 共享 docker 网络 `newapi-net`，DSN 中直接用**容器名**解析，无需宿主机 IP：

| 项 | 值 |
|------|------|
| new-api 容器名 | `new-api` |
| 镜像 | `calciumion/new-api:latest` |
| 对外端口 | `3000` |
| 共享网络 | `newapi-net` |
| 数据持久化 | `/data/new-api/data` → `/data` |
| 日志持久化 | `/data/new-api/logs` → `/app/logs` |
| SQL_DSN | `root:622851Tt.@tcp(mysql8:3306)/new-api` |
| REDIS_CONN_STRING | `redis://root:622851Tt.@redis6:6379` |

> 说明：脚本启动时会自动创建 `newapi-net` 网络，并把已有的 `redis6`、`mysql8` 容器挂到该网络（`docker network connect`，幂等操作，不影响现有容器与数据）。new-api 容器也跑在该网络，因此能用容器名互通。

---

## 二、可配置项

脚本顶部「可配置项」区块可按需修改：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `NEWAPI_IMAGE` | `calciumion/new-api:latest` | 镜像地址 |
| `NEWAPI_CONTAINER` | `new-api` | 容器名 |
| `NEWAPI_PORT` | `3000` | 对外端口 |
| `NEWAPI_DATA_DIR` | `/data/new-api/data` | 数据持久化目录 |
| `NEWAPI_LOG_DIR` | `/data/new-api/logs` | 日志持久化目录 |
| `SHARED_NETWORK` | `newapi-net` | 共享 docker 网络 |
| `REDIS_CONTAINER` | `redis6` | Redis 容器名（依赖） |
| `MYSQL_CONTAINER` | `mysql8` | MySQL 容器名（依赖） |
| `MYSQL_USER` / `MYSQL_PASSWORD` / `MYSQL_DB` | `root` / `622851Tt.` / `new-api` | MySQL 连接凭据 |
| `REDIS_USER` / `REDIS_PASSWORD` | `root` / `622851Tt.` | Redis ACL 用户凭据 |
| `TZ` | `Asia/Shanghai` | 时区 |
| `NODE_NAME` | `new-api-node-1` | 节点名（审计日志标识） |

> ⚠️ MySQL / Redis 的密码必须与 `install-docker-redis-mysql.sh` 中一致，否则连接失败。

---

## 三、命令一览

```bash
sudo bash deploy-newapi.sh start    # 首次部署 / 启动
sudo bash deploy-newapi.sh stop     # 停止（不删容器，数据保留）
sudo bash deploy-newapi.sh restart  # 重启（stop + start，重新应用配置）
sudo bash deploy-newapi.sh status    # 查看容器状态 + 接口健康
sudo bash deploy-newapi.sh logs      # 跟随查看日志（Ctrl-C 退出）
sudo bash deploy-newapi.sh update    # 拉取最新镜像并重建容器
```

`start` 流程：

- 创建共享网络 `newapi-net`（已存在则跳过）
- 把 `redis6` / `mysql8` 挂到 `newapi-net`（已挂则跳过）
- 创建 `/data/new-api/{data,logs}` 目录
- 拉取 `calciumion/new-api:latest` 镜像
- 启动 new-api 容器（`--restart always`）
- 轮询 `http://localhost:3000/api/status` 直到就绪

重跑安全：容器已在运行则跳过，已存在但停止则 `docker start`，不存在则新建。

---

## 四、部署到服务器

### 1. 上传脚本

在**本地终端**执行：

```bash
scp deploy/china/deploy-newapi.sh ubuntu@<服务器IP>:~/deploy-newapi.sh
```

### 2. 在服务器上启动

```bash
ssh ubuntu@<服务器IP> 'sudo bash ~/deploy-newapi.sh start'
```

> 普通用户操作 docker 需要 `sudo`。若已 `sudo usermod -aG docker ubuntu` 并重新登录，则可免 `sudo`。

### 3. 访问

启动成功后访问 `http://<服务器IP>:3000`（云厂商安全组需放行 `3000`）。

---

## 五、常用运维

```bash
# 查看状态
sudo bash ~/deploy-newapi.sh status

# 查看日志
sudo bash ~/deploy-newapi.sh logs
# 或直接
sudo docker logs -f --tail 200 new-api

# 停止 / 启动 / 重启
sudo bash ~/deploy-newapi.sh stop
sudo bash ~/deploy-newapi.sh start
sudo bash ~/deploy-newapi.sh restart

# 升级到最新镜像（数据由卷保留）
sudo bash ~/deploy-newapi.sh update

# 进入容器
sudo docker exec -it new-api sh
```

---

## 六、数据与重置

- 数据持久化在宿主机 `/data/new-api/data` 与 `/data/new-api/logs`，容器删除后数据仍在。
- Redis 数据在 `/data/redis`，MySQL 数据在 `/data/mysql`（由 `install-docker-redis-mysql.sh` 创建）。
- 完全重置 new-api（会清空 new-api 业务数据，不影响 redis/mysql）：

  ```bash
  sudo bash ~/deploy-newapi.sh stop
  sudo docker rm -f new-api
  sudo rm -rf /data/new-api/data /data/new-api/logs
  sudo bash ~/deploy-newapi.sh start
  ```

---

## 七、安全建议

生产环境请至少做以下调整：

1. **改掉示例密码**：`622851Tt.` 为示例值，改成强随机密码（同步修改 `install-docker-redis-mysql.sh` 与本脚本，并重建 redis/mysql 容器）。
2. **限制 3000 来源**：用安全组/防火墙限制 `3000` 来源 IP，或在前面加 Nginx/Caddy 反代 + HTTPS。
3. **多机部署设 `SESSION_SECRET`**：在脚本环境变量中加 `-e "SESSION_SECRET=<随机字符串>"`，多机必须修改。
4. **开启 Secure Cookie**（反代 HTTPS 后）：加 `-e "SESSION_COOKIE_SECURE=true"` 与 `-e "SESSION_COOKIE_TRUSTED_URL=https://your-domain"`。
5. **配置 `TRUSTED_PROXIES`**：反代场景下显式设置可信代理网段，如 `-e "TRUSTED_PROXIES=172.20.0.0/16"`。
6. **定期备份**：`/data/new-api/data`、`/data/mysql` 可用快照或 `mysqldump` 备份。

---

## 八、故障排查

| 现象 | 排查 |
|------|------|
| `依赖容器 redis6 不存在` | 先运行 `install-docker-redis-mysql.sh` 部署 Redis/MySQL |
| `connection refused mysql8:3306` | 确认 `redis6`/`mysql8` 已挂到 `newapi-net`：`docker network inspect newapi-net`；未挂则重跑 `start` 触发 `connect_deps` |
| `new-api 未在预期时间内就绪` | `sudo docker logs new-api --tail 100` 查看启动日志，常见为 DSN 密码不匹配或 MySQL 库未创建 |
| Redis 认证失败 | 确认 `REDIS_PASSWORD` 与 `redis6` ACL 文件 `/data/redis/users.acl` 一致；ACL 用户用 `root` 或 `default`，密码相同 |
| MySQL 认证失败 | 确认 `MYSQL_PASSWORD` 与 `mysql8` 的 `MYSQL_ROOT_PASSWORD` 一致，且 `root@%` 已开放远程登录 |
| 端口 3000 无法访问 | 反代模式下 3000 默认不暴露公网（由 Caddy 回源，见 [`deploy-domain.md`](deploy-domain.md)）；本地直连模式需设 `DEBUG_BIND=127.0.0.1` 才在服务器本机可访问 3000；若需公网直连调试再确认安全组/`ufw`/`firewalld` 放行 3000 |
| `docker: command not found` | 先运行 `install-docker-redis-mysql.sh` 安装 Docker |
| `permission denied ... docker.sock` | 普通用户无权访问 docker socket，命令前加 `sudo`，或加入 docker 组后重新登录 |

---

## 九、配合域名反代（HTTPS）

反代上线后（Caddy 由 [`deploy-domain.sh`](deploy-domain.md) 提供），new-api 侧需设置以下环境变量，
本脚本已自动处理（`DOMAIN` 非空时强制注入，无需手动加 `-e`）：

| 环境变量 | 作用 | 何时注入 |
|----------|------|----------|
| `DOMAIN` | 绑定域名，触发 HTTPS Secure cookie 模式 | 设了即走反代 HTTPS |
| `SESSION_COOKIE_SECURE` | 启用 Secure Cookie + OriginGuard | `DOMAIN` 非空时自动 `true` |
| `SESSION_COOKIE_TRUSTED_URL` | `https://<DOMAIN>`，与上者代码强绑定 | `DOMAIN` 非空时自动 |
| `SESSION_SECRET` | 会话密钥，单机建议设、换机迁移必须复用同一值 | 自生成随机串（如 `openssl rand -hex 32`）后传入 |
| `TRUSTED_PROXIES` | 可信代理网段（可选；留空默认信任 loopback/RFC1918，含 docker `172.x`） | 留空用默认即可 |

**端口策略**：`DOMAIN` 非空时，3000 默认**不再映射宿主机公网**（`-p` 移除），Caddy 容器在 `newapi-net` 内用容器名 `new-api:3000` 回源，公网无法直连 3000。调试可设 `DEBUG_BIND=127.0.0.1` 让服务器本机访问 3000。

> ⚠️ 重建/更新 new-api 容器（`update`，或带 `DOMAIN` 重跑 `start`）后，需执行
> `sudo docker restart caddy` 刷新 Caddy 缓存的上游 IP，否则会 502
> （详见 [`deploy-domain.md`](deploy-domain.md) 第八节）。

> 全链路部署按顺序执行三步：[`install-docker-redis-mysql.sh`](install-docker-redis-mysql.sh)
> （Docker/Redis/MySQL）→ `deploy-newapi.sh`（new-api，反代场景带 `DOMAIN`）→
> [`deploy-domain.sh`](deploy-domain.md)（Caddy HTTPS 反代）。
