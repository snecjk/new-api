# New-API 域名 HTTPS 反代部署说明(Caddy)

本脚本在已部署好 new-api 的服务器上,用 **Caddy** 容器为域名提供 HTTPS 反向代理:
公网 `80/443` → `newapi-net` 内的 `new-api:3000`。Let's Encrypt 证书由 Caddy 自动申请、
自动续期,无需 certbot。

配套脚本:[`deploy-domain.sh`](deploy-domain.sh)

> 前置条件:先按 [`deploy-docker-redis-mysql.md`](deploy-docker-redis-mysql.md) 与
> [`deploy-newapi.md`](deploy-newapi.md) 完成部署,确保 `new-api` 容器在 `newapi-net`
> 网络内正常运行。**本脚本只新增 Caddy 容器,不触碰 `mysql8` / `redis6` / `new-api`。**

---

## 一、连接拓扑

```
用户浏览器 ──443/80──▶ Caddy 容器(证书终结、自动续期、HTTP→HTTPS 308)
                          │  newapi-net 内用容器名回源
                          ▼
                       new-api:3000 ──▶ mysql8 / redis6
```

| 项 | 值 |
|------|------|
| Caddy 容器名 | `caddy` |
| 镜像 | `caddy:2` |
| 对外端口 | `80`、`443`(TCP)、`443`(UDP,HTTP/3 可选) |
| 共享网络 | `newapi-net`(与 new-api 同网络,容器名互通) |
| 回源地址 | `new-api:3000` |
| Caddyfile | `/data/caddy/Caddyfile`(宿主机) |
| 证书/ACME 数据 | `/data/caddy/data`(容器内 `/data`) |
| 运行时配置 | `/data/caddy/config`(容器内 `/config`) |

自动生成的 Caddyfile:

```
www.litemall.asia, litemall.asia {
	encode gzip zstd
	reverse_proxy new-api:3000
}
```

Caddy 对 OpenAI 兼容接口所需的 SSE 流式响应(`text/event-stream`)与 WebSocket
均自动适配,无需额外配置;反向代理默认透传 `X-Forwarded-For`/`X-Forwarded-Proto`,
且 new-api 默认可信代理网段覆盖 docker 的 `172.16.0.0/12`,客户端真实 IP 可正常透传。

---

## 二、前提条件(部署前逐项确认)

| # | 检查项 | 确认方式 |
|---|--------|----------|
| 1 | new-api 已运行且在 `newapi-net` 内 | `sudo docker ps \| grep new-api` |
| 2 | 域名 A 记录(裸域 + `www`)已解析到服务器公网 IP | `dig +short <域名> A` |
| 3 | 云安全组放行入站 TCP `80`、`443`(UDP `443` 可选) | 云厂商控制台 |
| 4 | 服务器可出站访问 `acme-v02.api.letsencrypt.org:443` | `curl -sI https://acme-v02.api.letsencrypt.org/directory` |
| 5 | **大陆服务器:域名已完成 ICP 备案** | 见下方说明 |

> ⚠️ **备案提示**:大陆地区的云服务器对未备案域名会在 `80/443` 上做拦截
> (按 HTTP Host / TLS SNI 识别),表现为 Let's Encrypt 验证失败、或外部访问被
> 跳转备案提示页。若域名无法/尚未备案,请改用境外节点或 CDN 边缘作为 HTTPS 入口。
> 备案前可先在服务器临时起一个 80 监听,从外部带域名 Host 请求验证是否被拦截。

---

## 三、可配置项

脚本顶部「可配置项」区块,均支持环境变量覆盖:

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `DOMAIN` | `www.litemall.asia` | 主域名 |
| `APEX_DOMAIN` | `litemall.asia` | 裸域,与主域名同一张证书反代 |
| `CADDY_IMAGE` | `caddy:2` | 镜像地址 |
| `CADDY_CONTAINER` | `caddy` | 容器名 |
| `CADDY_BASE_DIR` | `/data/caddy` | Caddyfile/证书/配置根目录 |
| `SHARED_NETWORK` | `newapi-net` | 与 new-api 共享的 docker 网络 |
| `UPSTREAM` | `new-api:3000` | 回源地址 |

---

## 四、命令一览

```bash
sudo DOMAIN=www.litemall.asia bash deploy-domain.sh start    # 首次部署(自动申请证书)
sudo bash deploy-domain.sh stop     # 停止(不删容器,证书保留)
sudo bash deploy-domain.sh restart  # 重启(重新加载 Caddyfile)
sudo bash deploy-domain.sh status   # 容器状态 + HTTPS 健康探测
sudo bash deploy-domain.sh logs     # 跟随查看日志(Ctrl-C 退出)
sudo bash deploy-domain.sh update   # 拉取最新镜像并重建容器(证书由卷保留)
```

`start` 流程:

- 校验 `newapi-net` 网络与 `new-api` 容器存在
- 生成 `/data/caddy/Caddyfile`
- 拉取 `caddy:2` 镜像并启动容器(`--restart always`,开机自启)
- 轮询日志等待 Let's Encrypt 证书签发,随后外部探测 `https://<DOMAIN>/api/status`

重跑安全:容器已在运行则跳过,已存在但停止则 `docker start`,不存在则新建。

---

## 五、部署到服务器

### 1. 上传脚本

在**本地终端**执行:

```bash
scp deploy/china/deploy-domain.sh ubuntu@<服务器IP>:~/deploy-domain.sh
```

### 2. 在服务器上启动

```bash
ssh ubuntu@<服务器IP> 'sudo DOMAIN=www.litemall.asia bash ~/deploy-domain.sh start'
```

证书通常在 10~30 秒内签发完成(Caddy 默认优先 HTTP-01,失败自动回退 TLS-ALPN-01,
两种挑战共用已放行的 80/443,无需额外配置)。

### 3. 验证

```bash
curl https://www.litemall.asia/api/status   # 应返回 success:true
curl -I  http://www.litemall.asia/          # 应 308 跳转到 https
```

---

## 六、证书机制

- 证书由 Caddy 内置 ACME 客户端向 Let's Encrypt 自动申请,裸域与 `www` 各一张
  (SAN 亦可,取决于 Caddy 分组策略),存储在 `/data/caddy/data/certificates/`。
- **自动续期**:Caddy 后台常驻续期任务,到期前约 30 天自动更换,无需 cron。
- 更换域名:改 `DOMAIN`/`APEX_DOMAIN` 后重跑 `start`(会重写 Caddyfile;
  Caddyfile 内容变更需 `restart` 或 `update` 才会生效)。

---

## 七、安全建议

反代上线后建议:

1. **收敛公网端口**:云安全组只保留 `80`/`443`(与 SSH),关闭 `3000`/`3306`/`6379`
   的公网访问——Caddy 经 docker 内网回源,关闭 3000 不影响域名访问,同时避免
   MySQL/Redis 与明文 `http://<IP>:3000` 绕过 HTTPS 直连。
2. **new-api 管理后台设置「服务器地址」**:系统设置 → 服务器地址 改为
   `https://<DOMAIN>`(影响支付回调/返回地址等外发 URL;容器无需重启)。
3. **启用 Secure Cookie**:重建 new-api 容器时带 `DOMAIN=<域名>` 环境变量
   (见 [`deploy-newapi.md`](deploy-newapi.md) 第九节),自动注入
   `SESSION_COOKIE_SECURE=true` 与 `SESSION_COOKIE_TRUSTED_URL`。
4. **API 客户端统一使用 `https://`**:HTTP 访问会被 308 重定向,不跟随重定向的
   客户端会拿到 308 而不是业务响应。

---

## 八、故障排查

| 现象 | 排查 |
|------|------|
| 证书签发失败 / 验证超时 | 依次确认:安全组 80/443 入站、DNS 已生效、大陆服务器域名已备案。查看 `sudo bash deploy-domain.sh logs` 中的 ACME 报错 |
| 验证反复失败 | **不要循环 `docker restart caddy`**:Let's Encrypt 对同一主机名有失败速率限制(每小时 5 次),频繁重试会被暂时锁定。先排除网络/备案原因再重试 |
| 外部访问跳转「备案提示页」 | 大陆服务器未备案域名被运营商/云厂商拦截,需完成 ICP 备案或迁境外节点 |
| 证书已签发但外部 https 不通 | 确认安全组放行 TCP `443`;服务器本机 `curl https://<DOMAIN>/api/status` 对比定位 |
| 重建 new-api 容器后全站 502 | Caddy 在加载配置时解析并缓存上游容器名对应的 IP,重建 new-api 后执行 `sudo docker restart caddy` 即可恢复 |
| 浏览器未走 HTTP/3 | 需安全组放行 UDP `443`;未放行时自动回退 TCP,不影响功能 |
| `docker 网络 newapi-net 不存在` | 先运行 [`deploy-newapi.sh`](deploy-newapi.sh) `start` 创建网络与 new-api |

---

## 九、与 new-api 的配合关系

- Caddy 只做 TLS 终结与转发;new-api 容器本身**无需任何改动**即可通过域名访问。
- 若要启用 new-api 的 HTTPS 相关能力(Secure Cookie、OriginGuard),按
  [`deploy-newapi.md`](deploy-newapi.md) 第九节用 `DOMAIN=<域名>` 重建 new-api,
  重建后记得 `sudo docker restart caddy`(见上表)。
- 本脚本 `stop` 后域名立即不可访问,但 `http://<IP>:3000`(若仍映射)与
  mysql/redis 不受影响。
