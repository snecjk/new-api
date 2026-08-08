# 自建 new-api 镜像教程

公共镜像 `calciumion/new-api:latest` 不包含本仓库的定制代码（模型映射等 fork 改动可能缺失或滞后）。
大陆服务器生产部署应**从本仓库源码自建镜像**，`deploy-newapi.sh` 默认使用 `new-api-custom:latest`。

> `deploy-newapi.sh` 的 `start`/`update` 已**自动拉取 fork 最新代码并在本机构建镜像**
> （与海外版同一套机制：push 到 GitHub main 后，服务器跑 `update` 即完成更新）。
> 本教程的手动上传/构建流程仅作为**国内网络访问 GitHub 失败时的兜底**。

配套脚本：[`build-image.sh`](build-image.sh)

> 这是全链路部署的**第 2 步**（共 4 步），总览见 [`README.md`](README.md)。
> 前置：[`deploy-docker-redis-mysql.md`](deploy-docker-redis-mysql.md)（Docker 已安装）。

---

## 一、上传源码到服务器

在**本地终端**（仓库根目录）执行：

```bash
git archive HEAD --format=tar.gz --prefix=new-api-src/ -o /tmp/new-api-src.tar.gz
scp /tmp/new-api-src.tar.gz ubuntu@<服务器IP>:~/
ssh ubuntu@<服务器IP> 'mkdir -p ~/new-api-src && tar xzf ~/new-api-src.tar.gz -C ~/new-api-src --strip-components=1'
```

> 用 `git archive` 只打包已提交内容，不含 `.git` 与 `node_modules`，通常只有几 MB。
> 有未提交的改动要先 `git commit`，否则不会进入镜像。

---

## 二、构建镜像

在服务器上执行：

```bash
sudo bash build-image.sh ~/new-api-src new-api-custom:latest
```

- 默认 `GOPROXY=https://goproxy.cn,direct`（大陆可达；`proxy.golang.org` 在大陆基本不可达，
  Dockerfile 已通过 `ARG GOPROXY` 支持覆盖）。
- 构建为多阶段（bun 前端 + Go 后端），约 5-15 分钟，BuildKit 有层缓存，二次构建很快。
- 构建完成后镜像在本地：`docker images | grep new-api-custom`。

---

## 三、部署/更新 new-api 使用该镜像

```bash
sudo bash deploy-newapi.sh start    # 首次部署：克隆 fork + 构建镜像 + 启动容器
sudo bash deploy-newapi.sh update   # 代码 push 后：自动拉 fork 最新代码 + 重建镜像 + 重建容器
```

`deploy-newapi.sh` 的 `NEWAPI_IMAGE` 默认即 `new-api-custom:latest`（本机构建的 tag，不从 registry 拉取）。
`update` 先构建成功再移除旧容器，构建失败时旧容器保持在线。

> ⚠️ 重建 new-api 容器后，执行 `sudo docker restart caddy` 刷新 Caddy 缓存的上游 IP，
> 否则域名访问会 502（见 [`deploy-domain.md`](deploy-domain.md) 第八节）。

---

## 四、代码更新后的完整流程

常规路径（服务器能连 GitHub）：本地 `git push` 到 main 后，在服务器执行：

```bash
sudo bash ~/deploy-newapi.sh update
sudo docker restart caddy
sudo bash ~/deploy-newapi.sh status   # 「最新提交」应为刚 push 的提交
```

> 存量服务器上的 `~/deploy-newapi.sh` 可能是旧版（不会自动拉代码）：
> 先把仓库里新版脚本覆盖上传一次（`scp deploy/china/deploy-newapi.sh ubuntu@140.143.183.34:~/`），
> 此后每次更新只需上面两条命令。

兜底路径（国内网络连不上 GitHub）：手动上传源码包再构建：

```bash
# 本地终端（仓库根目录）
git archive HEAD --format=tar.gz --prefix=new-api-src/ -o /tmp/new-api-src.tar.gz
scp /tmp/new-api-src.tar.gz ubuntu@140.143.183.34:~/
# 服务器
rm -rf ~/new-api-src5 && mkdir -p ~/new-api-src5 && tar xzf ~/new-api-src.tar.gz -C ~/new-api-src5 --strip-components=1
sudo bash ~/new-api-src5/deploy/china/build-image.sh ~/new-api-src5 new-api-custom:latest
sudo bash ~/deploy-newapi.sh update
sudo docker restart caddy
```

数据在卷 `/data/new-api` 中，重建容器不丢数据；`mysql8`/`redis6` 全程不受影响。

---

## 五、故障排查

| 现象 | 排查 |
|------|------|
| `go mod download` 卡住/超时 | GOPROXY 未生效；确认 Dockerfile 含 `ARG GOPROXY` 且构建命令带了 `--build-arg`（脚本已处理） |
| `bun install` 慢或失败 | 国内访问 npm 官方源慢但通常可用；必要时在构建环境配置 npm 镜像 |
| `image new-api-custom:latest not found` | 未构建镜像就跑了 `deploy-newapi.sh`；先执行本教程第二步 |
| 构建成功但功能没变 | 确认 `docker ps` 中 new-api 的 IMAGE 列是 `new-api-custom:latest`；`update` 后才生效 |
| `update` 时 `git fetch` 超时/失败 | 国内网络访问 GitHub 不稳定；重试，或改用第四节兜底路径手动上传源码包 |
