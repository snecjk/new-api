# 海外节点 New-API 部署脚本使用说明（复用国内 MySQL + Redis）

本脚本在**海外服务器**上把 **new-api** 以 docker 容器方式部署起来，并**跨公网复用
国内服务器（`140.143.183.34`）上已部署好的 `mysql8` / `redis6`**——即多节点共享同一套
数据库与缓存。海外节点以 **slave** 角色（`NODE_TYPE=slave`）运行，不跑后台迁移/调度/
清理任务（仍由国内 master 负责），仅对外提供 relay 服务并如实计费。

配套脚本：[`deploy-newapi.sh`](deploy-newapi.sh)

> 前置条件：国内服务器已按 [`deploy-docker-redis-mysql.md`](../china/deploy-docker-redis-mysql.md)
> 部署好 `redis6` / `mysql8`，且已按 [`deploy-newapi.md`](../china/deploy-newapi.md) 跑起国内
> new-api（master）。海外机已按 [`install-docker.md`](install-docker.md) 安装 Docker。

---

## ⚠️ 重要：先读完这两节再动手

### A. 跨境延迟与可靠性

海外节点 → 国内 MySQL/Redis 走**跨境公网**，每个 API 请求都要往返国内查库 + 操作 Redis：

- 单次 TCP RTT 通常 **150–300ms**（视两地线路与运营商绕路而定；本例海外机在 ap-northeast-1 东京区域，到国内通常偏低，仍以下方实测为准），一次请求涉及多次查表 + 多次 Redis，
  实测**每请求额外增加 0.5–2s** 不等。AI 上游调用本身通常数秒，叠加后体感变慢但多数场景可用。
- 跨境链路抖动/丢包会让 new-api 启动连库偏慢、偶发查询超时。本脚本已把健康检查放宽到 80s，
  并在启动前做 TCP 连通性自检，但**长期稳定性依赖两国间线路质量**。

**动手前先实测延迟**（在海外机上）：

```bash
# 测跨境到国内 DB/Redis 的 RTT（任选其一，能通即可）
nc -vz 140.143.183.34 3306          # MySQL 端口
nc -vz 140.143.183.34 6379          # Redis  端口
ping -c 5 140.143.183.34            # 看基础 RTT
```

> 若实测 RTT 过高或丢包严重，建议改用“海外本地 Redis + 远程国内 MySQL”，或干脆在海外
> 部署独立 DB（见末尾「十三、若跨境太慢：备选方案」）。

### B. 安全：明文跨公网传输

MySQL/Redis 默认**不走 TLS**，跨公网传输的是**明文**（含密码 `622851Tt.` 与全部业务数据）。
即便把国内防火墙只放行给海外机 IP，传输内容仍可被中间链路嗅探。因此：

1. **国内防火墙/云安全组必须只放行给海外机公网 IP**（见第四节），**严禁对 0.0.0.0/0 开放 3306/6379**。
2. **必须换掉示例密码 `622851Tt.`**（已提交在仓库、跨公网明文传输，等同 root 凭据泄露）：国内 `install-docker-redis-mysql.sh` 与本脚本同步改强随机密码，并重建 redis/mysql 容器。
3. **对传输有加密要求时**，用 SSH 隧道 / WireGuard 把 DB+Redis 包进加密通道（见第十二节），
   此时可只开国内 SSH 端口、3306/6379 完全不对公网暴露。

---

## 一、连接拓扑

海外 new-api 容器跨公网指向国内公网 IP + 端口（**不用容器名**，因为依赖容器在国内的 docker 网络里）：

| 项 | 值 |
|------|------|
| 海外 new-api 容器名 | `new-api` |
| 镜像 | `calciumion/new-api:latest` |
| 海外对外端口（宿主机） | `443`（`INGRESS_PORT=443` 默认，docker-proxy 绑 0.0.0.0:443；云端 HTTP 代理回源到此） |
| 公网访问 | `https://019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz`（云「HTTP 代理 内网443→域名」规则 `518`，边缘 TLS，无需本机证书） |
| 海外机公网 IP | `38.226.195.219`（SSH 端口 `10009`） |
| 海外机内网 IP | `10.198.2.236`（同 VPC 内可达，跨公网不用） |
| 海外机区域 | `ap-northeast-1`（东京） |
| 海外机公网域名 | `019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz` |
| 共享网络 | `newapi-net`（仅用于将来挂本地反代容器，不依赖本地 redis/mysql） |
| 数据持久化（海外） | `/data/new-api/data` → `/data` |
| 日志持久化（海外） | `/data/new-api/logs` → `/app/logs` |
| 节点角色 | `slave`（`NODE_TYPE=slave`） |
| 节点名 | `new-api-overseas`（审计日志标识，区别于国内 `new-api-node-1`） |
| SQL_DSN | `root:622851Tt.@tcp(140.143.183.34:3306)/new-api` |
| REDIS_CONN_STRING | `redis://root:622851Tt.@140.143.183.34:6379` |

> 国内 `mysql8` 的 `root@%` 远程登录与 `redis6` 的 ACL 用户（`root`/`default`，密码相同）均由
> `install-docker-redis-mysql.sh` 在部署时已配好，海外侧无需再建库/建号，只要国内防火墙放行即可。

---

## 二、多节点配置要点（必读）

海外节点是**第二个 new-api 实例**，与国内共用同一套 DB+Redis。以下配置保证多节点正确性：

| 配置 | 海外取值 | 为什么 |
|------|----------|--------|
| `NODE_TYPE` | `slave` | slave 跳过后台任务（DB 迁移、auth 清理、系统任务调度器、authz 内置策略**播种**、订阅重置、codex 凭证刷新等），避免与国内 master 重复执行。slave **仍主动跑**周期性 authz 策略**重载**（`StartPolicySync`）与系统实例上报（`StartSystemInstanceReporter`，让本机出现在「系统信息」多实例列表）——这两项本就该每节点都跑。relay 服务与计费不受影响，slave 正常对外服务。 |
| `NODE_NAME` | `new-api-overseas` | 审计日志区分来源；与国内 `new-api-node-1` 不同即可。 |
| `SESSION_SECRET` | **两端设同一随机串** | 多节点共享 DB：登录态/签名若要跨节点互认，两端必须用同一个密钥。`CRYPTO_SECRET` 默认回退到 `SESSION_SECRET`，设它即同时统一两端签名密钥。 |
| `BATCH_UPDATE_ENABLED` | `false`（默认） | 跨境链路下每请求即时原子写库：预扣费失败即拒请求（不产生免费调用），无批次 flush 丢失风险。两种模式均为原子增量；但跨节点计费严格性要求两端都 false（见下「计费安全性」）。 |

### 计费安全性说明

new-api 的所有配额写入（token/user/channel 的 `quota`/`used_quota`/`request_count`）都走
**原子增量** `UPDATE ... SET col = col + ?`，Redis 配额缓存走原子 `INCRBY`。两个节点各自把
自己服务的请求增量写回同一库，**不会重复计费、不会丢更新**。slave 关掉批量更新后走即时
原子写，预扣费（请求前扣）失败会直接拒绝请求，结算（请求后补差）失败只丢单笔增量并记日志——
跨境场景下这是比“批量 flush 失败丢一整批”更稳妥的默认。

> ⚠️ **跨节点计费严格性取决于两端 batch 配置**：原子 DB 增量本身跨节点安全（不重计、不丢更新），
> 但在 `BATCH_UPDATE_ENABLED=true`（或一端 true 一端 false）时，配额预扣的判定走 Redis 缓存软校验，
> 缓存缺失回填会从**滞后**的 DB 重读整条 token 覆盖原子 `INCRBY`（batch 节点 DB 比缓存最多滞后 5s），
> 可能让预扣通过在偏高的额度上 → 少扣/超用（`remain_quota` 可被 `remain_quota - ?` 推到负值）。
> **两端都 `BATCH_UPDATE_ENABLED=false` 时无此问题**（DB 即时写、缓存回填值正确）。海外默认 false；
> **国内也建议改为 false**：
>
> ```bash
> # 国内服务器上（即时原子写，跨节点计费严格）
> sudo BATCH_UPDATE_ENABLED=false bash ~/deploy-newapi.sh update
> ```

### SESSION_SECRET 与国内节点对齐

国内节点当前 `SESSION_SECRET` 未设（每次重启随机），意味着**国内自己重启后会话就失效**
（既有问题）。要让海外与国内跨节点互认（例如同一域名下两节点轮询，或希望国内登录在海外也生效）：

1. 生成一段随机串（任选其一）：

   ```bash
   openssl rand -hex 32
   # 或
   head -c 32 /dev/urandom | base64
   ```

2. 海外启动时传入：

   ```bash
   sudo SESSION_SECRET='<同一段随机串>' bash ~/deploy-newapi.sh update
   ```

3. 国内也设**同一个值**并重建容器（国内脚本同样支持 `SESSION_SECRET` 环境变量）：

   ```bash
   sudo SESSION_SECRET='<同一段随机串>' bash ~/deploy-newapi.sh update
   ```

> 若海外与国内各自独立域名、用户分别登录，`SESSION_SECRET` 不一致也不影响功能（只是登录态不跨节点）。
> 但即便如此，也建议两端都设固定值，避免各自重启后会话失效。

---

## 三、可配置项

脚本顶部「可配置项」区块可按需修改（均支持环境变量覆盖）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `NEWAPI_IMAGE` | `calciumion/new-api:latest` | 镜像地址 |
| `NEWAPI_CONTAINER` | `new-api` | 容器名 |
| `NEWAPI_PORT` | `3000` | 直连模式（`DOMAIN=` 空时）宿主机端口；默认走 `INGRESS_PORT=443`，此项不生效 |
| `NEWAPI_DATA_DIR` | `/data/new-api/data` | 海外数据持久化目录 |
| `NEWAPI_LOG_DIR` | `/data/new-api/logs` | 海外日志持久化目录 |
| `SHARED_NETWORK` | `newapi-net` | 海内共享网络（不挂本地 redis/mysql） |
| `CHINA_HOST` | `140.143.183.34` | 国内服务器公网 IP |
| `MYSQL_PORT` / `REDIS_PORT` | `3306` / `6379` | 国内 MySQL/Redis 端口 |
| `MYSQL_USER` / `MYSQL_PASSWORD` / `MYSQL_DB` | `root` / `622851Tt.` / `new-api` | MySQL 连接凭据（须与国内一致） |
| `REDIS_USER` / `REDIS_PASSWORD` | `root` / `622851Tt.` | Redis ACL 用户凭据（须与国内一致） |
| `NODE_TYPE` | `slave` | 节点角色（海外默认 slave） |
| `NODE_NAME` | `new-api-overseas` | 审计日志节点名 |
| `SESSION_SECRET` | （空） | 会话/签名密钥；多节点建议两端设同一随机串 |
| `BATCH_UPDATE_ENABLED` | `false` | 批量更新；跨境默认 false（计费更稳） |
| `TZ` | `UTC` | 时区；要与国内日志对齐改 `Asia/Shanghai` |
| `ERROR_LOG_ENABLED` | `true` | 错误日志 |
| `DOMAIN` | `019fbc…a8g1v3.xyz` | 绑定域名（默认本海外机云域名）；显式 `DOMAIN=` 留空=本地 HTTP 直连 3000 |
| `TRUSTED_PROXIES` | （空） | 可信代理网段（反代场景） |
| `DEBUG_BIND` | （空） | 留空=交 `DOMAIN`/`INGRESS_PORT` 分支（默认 443）；`127.0.0.1`=仅本机调试 |
| `INGRESS_PORT` | `443` | `DOMAIN` 非空时本机监听端口（外部/云反代模式，默认 443 配云 HTTP 代理规则让 `https://域名` 直达）；显式留空=本地 Caddy 容器反代 |

> ⚠️ MySQL/Redis 密码必须与国内 `install-docker-redis-mysql.sh` 一致，否则认证失败。
> ⚠️ MySQL DSN 格式 `user:pass@tcp(host:port)/db` 中，密码若含 `@` `:` `/` 会破坏解析——
> 示例密码 `622851Tt.` 安全；换密码时避开这些字符或改用 URL 编码。

---

## 四、前置：国内端口放行与来源限制（关键安全步骤）

国内 `mysql8`/`redis6` 容器已用 `-p 3306:3306` / `-p 6379:6379` 发布到宿主机 `0.0.0.0`，**默认对公网全开**。
海外节点要跨公网连它们，就必须把这两个端口的来源**限定为海外机公网 IP**——否则任何人都能
`mysql -h 140.143.183.34 -uroot -p<密码>` 拿到 root，明文跨公网。

### ⚠️ 重要：Docker 发布端口会绕过 ufw/firewalld

`-p 3306:3306` 让 Docker 在 nat 表插入 DNAT（`0.0.0.0:3306` → 容器 IP），包进入 FORWARD 链
（被 Docker 的 DOCKER 链 ACCEPT），**根本不经过 ufw/firewalld 管理的 INPUT 链**。因此：

- **ufw / firewalld 的「放行 3306/6379 给某 IP」对这两个 Docker 端口是无效的**——端口仍对 `0.0.0.0/0` 开放。
- 能真正限制来源的只有：
  1. **云厂商安全组**（国内服务器所在云控制台，inbound `3306`/`6379` 来源限定为海外机 IP）——
     仅当国内机在云上、且启用了 SG 时有效；
  2. **把容器端口改绑 `127.0.0.1` + 走加密隧道**（见「十二、加密隧道」），3306/6379 不再上公网接口，
     海外机经 SSH/WireGuard 到国内 `127.0.0.1` 访问——**非云主机唯一可靠做法，且传输加密**。

> **强烈推荐直接走「十二、加密隧道」**：国内把 `mysql8`/`redis6` 改绑 `127.0.0.1`（在
> `install-docker-redis-mysql.sh` 里把 `-p 3306:3306` 改成 `-p 127.0.0.1:3306:3306`，6379 同理，重建容器），
> 关闭公网 3306/6379，只开 SSH 端口给海外机；海外机经隧道访问 `127.0.0.1` 的 3306/6379。全程加密、来源锁死。
> 仅当国内机在云上、且你接受明文跨公网时，才用下方「直连模式」并只靠云安全组限制来源。

### 直连模式（明文，仅靠云安全组限制来源）

本海外机公网 IP 为 `38.226.195.219`（内网 `10.198.2.236` 仅同 VPC 内可达，跨公网不参与路由）。

在**国内服务器的云控制台**把 inbound `3306`/`6379` 来源限定为 `38.226.195.219`（**仅云安全组有效**；
ufw/firewalld 对 Docker 发布端口无效，不必配）。**海外机**默认走云「HTTP 代理 内网443→域名」规则（HTTPS，见「十」路 A）；明文 3000→公网端口 非默认，需显式 `DOMAIN=` 切回（见「六、3」末）。

> ⚠️ 国内安全组放行的是海外机**公网** IP `38.226.195.219`，**不是内网 `10.198.2.236`**——
> 跨公网流量源 IP 是公网 IP，填成内网 IP 会一直连不上 3306/6379。

验证跨境连通（避免密码进 `ps`/历史：mysql 交互输入、redis 用 `REDISCLI_AUTH` 环境变量）：

```bash
mysql -h 140.143.183.34 -P 3306 -uroot -p -e "SELECT VERSION();"        # 回车后输入密码
REDISCLI_AUTH='<密码>' redis-cli -h 140.143.183.34 -p 6379 --user root PING   # 期望 PONG
```

---

## 五、命令一览

```bash
sudo bash deploy-newapi.sh start    # 首次部署 / 启动（含跨境连通性自检）
sudo bash deploy-newapi.sh stop     # 停止（不删容器，数据保留）
sudo bash deploy-newapi.sh restart  # 重启（stop + start；复用旧容器，不改 env）
sudo bash deploy-newapi.sh status    # 查看容器状态 + 接口健康 + 跨境连接目标
sudo bash deploy-newapi.sh logs      # 跟随查看日志（Ctrl-C 退出）
sudo bash deploy-newapi.sh update    # 拉取最新镜像并重建容器（改配置后用它生效）
```

`start` 流程：

- 跨境 TCP 连通性自检：国内 `140.143.183.34:3306` / `:6379` 是否可达，不可达直接报错退出
  > ⚠️ 自检「可达」仅代表 TCP 能通，**不代表已限定为本机 IP、也不代表已加密**——Docker 端口绕过
  > ufw/firewalld，须按「四」确认来源限制、按「十二」上加密隧道。脚本在密码仍为示例值时会额外告警。
- 创建共享网络 `newapi-net`（已存在则跳过）
- 创建 `/data/new-api/{data,logs}` 目录
- 拉取 `calciumion/new-api:latest` 镜像
- 启动 new-api 容器（`--restart always`，角色 slave）
- 轮询 `http://localhost:3000/api/status` 直到就绪（最多 80s）

重跑安全：容器已在运行则跳过，已存在但停止则 `docker start`，不存在则新建。

> **改配置生效**：`restart` 只是 stop+start，**复用旧容器与旧环境变量**，改任何「可配置项」后
> 必须用 `update`（rm + 重建）才会真正应用新 env。

---

## 六、部署到海外服务器

海外机：公网 `38.226.195.219`，SSH 端口 `10009`。

> 前置：海外机须先装好 Docker。未装则先按 [`install-docker.md`](install-docker.md) 装好再继续，
> 否则 `start` 会报 `未检测到 docker`。

### 1. 上传脚本

在**本地终端**执行：

```bash
scp -P 10009 deploy/overseas/deploy-newapi.sh root@38.226.195.219:~/deploy-newapi.sh
```

> 若提示主机密钥变更，先 `ssh-keygen -R "[38.226.195.219]:10009"`。

### 2. 在海外服务器上启动

```bash
ssh -p 10009 root@38.226.195.219 'sudo bash ~/deploy-newapi.sh start'
```

> 普通用户操作 docker 需 `sudo`。若已 `sudo usermod -aG docker <用户>` 并重新登录，可免 `sudo`。

### 3. 访问

默认走域名 HTTPS：new-api 听本机 `443`（`INGRESS_PORT=443` 默认），云控制台「HTTP 代理 内网443→域名」规则（`518`，已生效）把 `https://域名` 在边缘解 TLS 后转明文到本机 443 → new-api。无需本机证书、无需手动加映射规则。

- 公网访问（HTTPS，默认）：`https://019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz`

`sudo bash ~/deploy-newapi.sh start`（或 `update`）即默认此模式，不必再传 `DOMAIN`/`INGRESS_PORT`。

首次进入用默认管理员账号登录（与国内共用同一套用户库，已有管理员可直接用国内账号登录）。

> 旧的明文 IP 回退（`http://38.226.195.219:10016`）已不是默认：需在云控制台保留「端口映射 内网3000→公网10016」规则，并显式 `sudo DOMAIN= bash ~/deploy-newapi.sh update` 切回直连 3000 模式。明文跨网，仅临时排查用。

---

## 七、常用运维

```bash
# 查看状态（含跨境连接目标 + DB/Redis 可达性）
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

# 改配置后生效（重建容器）
sudo NODE_TYPE=slave NODE_NAME=new-api-overseas bash ~/deploy-newapi.sh update

# 进入容器
sudo docker exec -it new-api sh
```

---

## 八、数据与重置

- 海外数据持久化在宿主机 `/data/new-api/data` 与 `/data/new-api/logs`，容器删除后仍在。
- **业务数据（用户/令牌/渠道/日志）全在国内 MySQL**，海外节点不持久化业务数据，重置海外容器不影响数据。
- 完全重置海外节点（仅清海外本地缓存/日志，不动国内库）：

  ```bash
  sudo bash ~/deploy-newapi.sh stop
  sudo docker rm -f new-api
  sudo rm -rf /data/new-api/data /data/new-api/logs
  sudo bash ~/deploy-newapi.sh start
  ```

---

## 九、故障排查

| 现象 | 排查 |
|------|------|
| `MySQL 端口不可达：140.143.183.34:3306` | 国内防火墙/云安全组未放行 3306 给海外机 IP；或国内 mysql8 容器停了（国内 `docker ps`）。见第四节 |
| `Redis 端口不可达：140.143.183.34:6379` | 同上，针对 6379 与 redis6 容器 |
| `connection refused ... 3306/6379` | 国内容器未运行或端口映射异常；国内 `docker ps` 看 mysql8/redis6，`ss -tlnp \| grep -E '3306\|6379'` 看宿主机监听 |
| MySQL 认证失败 | 海外 `MYSQL_PASSWORD` 与国内 `MYSQL_ROOT_PASSWORD` 不一致；或国内未开放 `root@%`（默认已开，国内 `docker exec mysql8 mysql -uroot -p... -e "SELECT user,host FROM mysql.user WHERE user='root'"` 应有 `root@%`） |
| Redis 认证失败 | 海外 `REDIS_PASSWORD`/`REDIS_USER` 与国内 ACL 文件 `/data/redis/users.acl` 不一致（国内用户 `root` 或 `default`，密码相同） |
| `new-api 未在预期时间内就绪` | `sudo docker logs new-api --tail 100`；跨境连库超时最常见，其次凭据不匹配。可临时调大 `HEALTH_WAIT_ROUNDS=60` |
| 接口慢 / 请求延迟高 | 跨境 RTT 高（`ping 140.143.183.34`）；考虑本地 Redis 或独立 DB（见第十三节） |
| 偶发 `failed to batch update` 日志 | 若 `BATCH_UPDATE_ENABLED=true`，跨境 flush 失败丢批次增量；改 `BATCH_UPDATE_ENABLED=false` 走即时写 |
| `docker: command not found` | 海外机未装 Docker，先安装 |
| `permission denied ... docker.sock` | 普通用户无权访问 docker socket，命令前加 `sudo`，或加入 docker 组后重新登录 |

---

## 十、配合域名反代（HTTPS）

设 `DOMAIN` 后脚本自动强制注入 `SESSION_COOKIE_SECURE=true` + `SESSION_COOKIE_TRUSTED_URL=https://<DOMAIN>`
（代码强绑定，缺一启动报错），让 new-api 在 HTTPS 后端正确签发 Secure cookie。本海外机自带公网域名
`019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz`，且云控制台已有一条「HTTP 代理 内网443→域名」
规则（云端边缘做 TLS，**无需本机证书**）。两条路：

### 路 A：云端 HTTP 代理（默认，免装 Caddy、免证书）

`DOMAIN` + `INGRESS_PORT=443` 已是脚本默认：new-api 直接监听宿主机 `443`（0.0.0.0），云端「HTTP 代理 内网443→域名」
规则把 `https://域名` 的流量在边缘解 TLS 后转明文 HTTP 到本机 443 → new-api。无需本机证书、无需 Caddy。直接：

```bash
sudo bash ~/deploy-newapi.sh update
```

（即默认 `DOMAIN=019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz INGRESS_PORT=443`；要换域名/端口再显式传 env 覆盖。）
访问 `https://019fbc8cdaf070d99719a571e184014b.ap-northeast-1.a8g1v3.xyz`。

> 切到路 A（默认）后，本机不再发布 3000，之前那条「端口映射 内网3000→公网10016」规则会指向空端口（打不通），
> 可在云控制台删除它（只保留域名这一条入口）。临时明文回退：`sudo DOMAIN= bash ~/deploy-newapi.sh update`（切回直连 3000 + 10016）。
> `TRUSTED_PROXIES` 留空默认信任 loopback/RFC1918/docker 网段；若云端代理回源 IP 不在此范围
> （登录跳 http、或日志里 client IP 全是代理 IP），显式设 `TRUSTED_PROXIES=<云端代理回源 IP/CIDR>`。

### 路 B：本地 Caddy 容器反代（需自己的域名 + 证书）

`DOMAIN` 非空、`INGRESS_PORT` 留空：3000 仅 `127.0.0.1` 回源，需在海外自行部署 Caddy 容器加入 `newapi-net`、
用容器名 `new-api:3000` 回源，并由 Caddy 签 Let's Encrypt 证书。云厂自带 `a8g1v3.xyz` 域名无 DNS 权限、
签不了证书，故路 B 须换你自己的域名。

```bash
sudo DOMAIN=api-overseas.example.com \
     SESSION_SECRET='<同国内一致的随机串>' \
     bash ~/deploy-newapi.sh update
```

> 路 A 利用了云自带域名的边缘 TLS，是本海外机最省事的 HTTPS 路径；路 B 仅在你要用自己的域名时才需要。

---

## 十一、安全建议

生产环境请至少做以下调整：

1. **必须换掉示例密码**：`622851Tt.` 改强随机密码（已提交仓库 + 跨公网明文 = root 凭据泄露；国内 `install-docker-redis-mysql.sh` + 本脚本同步改，重建 redis/mysql）。
2. **3306/6379 只放行海外机 IP**：见第四节，**绝不对公网全开**。
3. **跨公网加密**：用 SSH 隧道或 WireGuard 包住 DB+Redis 链路（见第十二节），可彻底关闭公网 3306/6379。
4. **设 `SESSION_SECRET`**：两端同一随机串，避免重启会话失效、实现跨节点互认（见第二节）。
5. **海外入口来源限制**：默认 `443` 经云 HTTP 代理（域名 HTTPS，来源由云控）；`DOMAIN=` 直连模式时为 `3000`，需云安全组/防火墙限制来源。
6. **MySQL 建独立业务账号**：不用 `root` 连业务应用，`CREATE USER` + 最小权限授权（国内建号，海外 DSN 改用该号）。
7. **定期备份**：业务数据在国内 MySQL `/data/mysql`，用 `mysqldump` 或卷快照备份。

---

## 十二、加密隧道（可选但强烈推荐）

若不希望 3306/6379 明文跨公网，用加密隧道把它们包起来，**国内只需开 SSH 端口**。
两种常见做法（任选其一）：

### 方案 1：SSH 隧道（autossh，最简单）

在**海外机**上用 autossh 建立到国内的长隧道，把国内 `127.0.0.1` 的 3306/6379 转到海外本机端口。
（前提：国内 `mysql8`/`redis6` 已按「四」改绑 `127.0.0.1`，3306/6379 不再上公网接口。）

```bash
# 海外机安装 autossh
sudo apt-get install -y autossh        # 或 yum install -y autossh

# 先取 newapi-net 网关 IP（本脚本默认把 new-api 挂在 newapi-net，其网关不是 docker0 的 172.17.0.1）
GW=$(docker network inspect newapi-net -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}')
echo "$GW"   # 例如 172.18.0.1

# 测试隧道（前台跑，确认能通）：本地转发绑到 newapi-net 网关 IP，仅该网段可达
# 注意：-p 是【国内机】的 SSH 端口（你登国内机用的端口）。10009 是海外机自己的 SSH 端口，别混用。
autossh -M 0 -N \
  -p <国内SSH端口> \
  -o "ServerAliveInterval=30" -o "ServerAliveCountMax=3" \
  -o "ExitOnForwardFailure=yes" \
  -L ${GW}:13306:127.0.0.1:3306 \
  -L ${GW}:16379:127.0.0.1:6379 \
  root@140.143.183.34
# 另开终端验证：nc -z ${GW} 13306 && nc -z ${GW} 16379
```

确认能通后写成 systemd 服务（开机自启、断线重连）。new-api 跑在**容器**里，`127.0.0.1` 指容器自身，
故用上方网关 IP。两种接法：

- **接法 A（推荐，默认）**：容器在 `newapi-net` 上，经该网桥网关 IP 访问宿主机隧道端口。
  隧道按上面 `-L ${GW}:13306:... -L ${GW}:16379:...` 绑定；启动：
  ```bash
  sudo CHINA_HOST=${GW} MYSQL_PORT=13306 REDIS_PORT=16379 bash ~/deploy-newapi.sh update
  ```
  （`${GW}` 用上面 `docker network inspect` 取到的值，如 `172.18.0.1`。）
- **接法 B（host 网络）**：new-api 用宿主机网络栈，直接访问宿主机 `127.0.0.1` 隧道端口：
  ```bash
  sudo SHARED_NETWORK=host CHINA_HOST=127.0.0.1 MYSQL_PORT=13306 REDIS_PORT=16379 bash ~/deploy-newapi.sh update
  ```
  注意：`--network host` 下 `-p` 端口发布被 Docker 忽略，容器直接监听宿主机 `0.0.0.0:3000`，
  需在宿主机/云层面对 `3000` 做来源限制（或用 `DOMAIN` 反代并限制反代端口）。

> 隧道方案下，国内只保留 SSH 端口对海外机开放（端口用你登国内机的实际端口；10009 是海外机自己的），3306/6379 完全不对公网暴露，传输全程加密。

### 方案 2：WireGuard（站点到站点 VPN，更稳更通用）

在海外机与国内机间建 WireGuard 隧道，两端拿到隧道内网 IP（如国内 `10.8.0.1`）后：

```bash
sudo CHINA_HOST=10.8.0.1 bash ~/deploy-newapi.sh update
```

国内防火墙关公网 3306/6379，只放行 WireGuard UDP 端口给对端。WireGuard 比 SSH 隧道更稳、
断线恢复更快，适合长期多节点互联。

---

## 十三、若跨境太慢：备选方案

若实测跨境延迟/丢包严重影响可用性，可考虑：

1. **海外本地 Redis + 远程国内 MySQL**：在海外用 [`install-docker-redis-mysql.sh`](../china/install-docker-redis-mysql.sh)
   起一个本地 `redis6`，`REDIS_CONN_STRING` 指本地、`SQL_DSN` 仍指国内。Redis 操作低延迟，
   只剩 MySQL 跨境。（各节点本地 Redis 避免跨节点缓存争用，但共享 MySQL 下计费严格性仍要求
   两端都 `BATCH_UPDATE_ENABLED=false`——否则一端 batch 的滞后 DB 写会让另一端缓存回填偏高、可能超用。）
2. **海外独立部署（不共享）**：海外另起一套 MySQL+Redis+new-api，与国内完全独立（两套用户/计费）。
3. **读副本 / 就近库**：国内 MySQL 配主从，海外就近接入从库（需自行处理写路由，超出本脚本范围）。

> 共享国内 DB+Redis 的核心价值是**单一数据源、统一计费与用户体系**；若跨境链路代价过高，
> 方案 1（本地 Redis）是性价比最高的折中。
