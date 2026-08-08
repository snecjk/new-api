# deploy/china — 大陆服务器部署全套脚本与文档

在大陆云服务器（Ubuntu/Debian）上部署 new-api 全链路：Docker + Redis + MySQL →
自建镜像 → new-api → Caddy 域名 HTTPS 反代。

## 部署顺序

| 步骤 | 脚本 | 文档 | 产出 |
|------|------|------|------|
| 1 | [`install-docker-redis-mysql.sh`](install-docker-redis-mysql.sh) | [`deploy-docker-redis-mysql.md`](deploy-docker-redis-mysql.md) | Docker、`redis6`、`mysql8` 容器 |
| 2 | [`build-image.sh`](build-image.sh) | [`build-image.md`](build-image.md) | 自建镜像 `new-api-custom:latest`（含本仓库定制代码） |
| 3 | [`deploy-newapi.sh`](deploy-newapi.sh) | [`deploy-newapi.md`](deploy-newapi.md) | `new-api` 容器，加入 `newapi-net` 网络 |
| 4 | [`deploy-domain.sh`](deploy-domain.sh) | [`deploy-domain.md`](deploy-domain.md) | `caddy` 容器，域名 HTTPS 入口 |

> 步骤 2 说明：公共镜像 `calciumion/new-api:latest` 不含本仓库定制代码，生产部署应自建镜像；
> 未构建时 `deploy-newapi.sh` 可用 `NEWAPI_IMAGE=calciumion/new-api:latest` 临时覆盖。

## 最终拓扑

```
浏览器 ──80/443──▶ caddy（Let's Encrypt 自动证书、HTTP→HTTPS）
                     │ newapi-net（docker 内容器名互通）
                     ▼
                  new-api:3000 ──▶ mysql8 / redis6
```

## 端口与数据一览

| 项 | 值 |
|----|----|
| 公网入口 | `80`/`443`（caddy，`443/udp` 可选，HTTP/3） |
| 内部端口 | `3000`（new-api）、`3306`（mysql8）、`6379`（redis6） |
| 数据持久化 | `/data/new-api/{data,logs}`、`/data/mysql`、`/data/redis`、`/data/caddy` |

> 生产环境建议安全组只放行 `80`/`443`（与 SSH），`3000`/`3306`/`6379` 不对公网开放；
> Caddy 经 docker 内网回源，不受影响。

## 大陆服务器注意事项

- **ICP 备案**：大陆云服务器对未备案域名的 `80/443` 访问会被拦截（按 Host/SNI 识别），
  表现为证书签发验证失败或访问被跳转备案提示页。未备案域名请改用境外节点或 CDN 边缘。
  详见 [`deploy-domain.md`](deploy-domain.md) 第二、八节。
- **镜像加速**：安装脚本默认使用腾讯云内网镜像源，其他云可改脚本顶部 `DOCKER_*_MIRROR`。

## 关键运维约定

- 重建/更新 `new-api` 容器后，执行 `sudo docker restart caddy` 刷新上游 IP 缓存，
  否则 502。
- `deploy-newapi.sh update` 自动拉 fork 最新代码并在本机构建镜像（服务器需能连
  GitHub）：本地 push 后，服务器跑 `update` + `restart caddy` 即完成更新；
  GitHub 连不上时用 [`build-image.md`](build-image.md) 的手动兜底流程。
- `mysql8` / `redis6` 容器承载全部业务数据，任何维护操作不得删除其容器或
  `/data/mysql`、`/data/redis` 目录。
