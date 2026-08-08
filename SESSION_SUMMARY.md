# 会话纪要 2026-08-04 — litemall.asia 部署修复与模型映射

> 本文件为本次会话的压缩纪要。服务器：140.143.183.34（腾讯云，SSH `ubuntu`，密码同 DB：`622851Tt.`）。
> 硬约束：`mysql8` / `redis6` 容器与数据目录全程不得破坏（本次全程未重启，Up 5 days）。

## 一、域名不可用 → 已恢复

- 根因：DNS 正常（www/裸域 → 140.143.183.34），new-api :3000 健康，但 **80/443 无监听**——Caddy 反代从未部署。
- 实测排除大陆 ICP 拦截（80 端口带 Host 探测直达主机）。
- 新增 `deploy/china/deploy-domain.sh` 并部署 `caddy` 容器（`newapi-net` 内回源 `new-api:3000`，`--restart always`），Let's Encrypt 双证书（www+裸域，存 `/data/caddy`，自动续期）。
- 验证：http→https 308、TLS1.3、`https://litemall.asia(/www)/` 与 `/api/status` 200。

## 二、deploy/china 文档与脚本补齐

- 新增：`deploy-domain.md`、`README.md`、`build-image.sh`、`build-image.md`。
- 修正：`deploy-newapi.md` 对不存在的 `deploy-all.sh` 的引用；部署链路改为 **4 步**（install → build-image → deploy-newapi → deploy-domain）；步骤数引用同步。
- `deploy-newapi.sh` 默认镜像改为 `new-api-custom:latest`（自建，含 fork 定制代码；公共 `calciumion/new-api:latest` 可用 `NEWAPI_IMAGE` 覆盖回退）。
- 根 `Dockerfile` 增加 `ARG GOPROXY`（国内构建 `--build-arg GOPROXY=https://goproxy.cn,direct`，`build-image.sh` 已自动带）。

## 三、Waffo 审核材料

- `docs/legal/terms-of-service.md`、`privacy-policy.md`：旧隧道域名 → `https://litemall.asia`。
- 公开页面：`https://litemall.asia/user-agreement`、`/privacy-policy`（内容存系统设置 `legal.*`，隐私政策已贴；**用户协议需确认已重贴新版**）。
- 「产品与平台详情」表单文案已给出（35 模型、预付费积分：¥7≈50 万积分/1$=500000 quota、无订阅、目标客户开发者/AI 客户端用户）。
- 系统设置「服务器地址」用户已改为 `https://www.litemall.asia`（**建议去 www 统一裸域**）。

## 四、模型映射失效 — 两层根因与修复

通道：`#9 OpenAI`（gpt-5.6-sol/terra/luna → glm-5.2-fast-preview/glm-5.2/kimi-k2.7-code）、`#10 Anthropic`（claude-opus-4-8/4-7/4-6、claude-fable-5 → glm/glm/kimi/qwen3.8-max），均 type 17（百炼 DashScope）。

1. **请求侧**：全局透传 `global.pass_through_request_enabled=true` 使原始请求体直发上游、绕过 `model_mapping` → 上游 `model_not_found`。已改 `false`（DB options，周期同步生效；UI：系统设置→模型与路由→全局设置→透传请求）。**透传与映射互斥**。
2. **响应侧**：响应 model 字段泄漏上游真名；`/v1/messages` 走百炼 **Anthropic 兼容端点**（`/apps/anthropic/v1/messages`），claude 适配器原样透传。
   修复（未提交）：`IsModelMapped` 时回写 `OriginModelName`，覆盖 openai 适配器（流式 chunk map 回写保留附加字段/非流式/forceFormat/末尾 usage 帧）+ claude 适配器（`message_start.message.model` 嵌套/转 OpenAI 格式/非流式两格式/末尾帧）；`relay/channel/openai/model_rewrite_test.go` 5 用例。计费仍按真实上游模型。
3. 镜像：服务器原跑公共 calciumion 镜像（不含 fork 代码）→ 自建 `new-api-custom:latest`（`build-image.sh`），容器按原配置重建（含 `-p 3000:3000`），每次重建后 `docker restart caddy`（上游 IP 缓存）。

验证矩阵全绿：OpenAI 流式（含 usage 帧）/非流式 = `claude-opus-4-8`；messages 流式 message_start/非流式 = `claude-fable-5`；域名外网 `gpt-5.6-sol` = `gpt-5.6-sol`。

## 五、运维约定与待办

- 重建/更新 new-api 后必须 `docker restart caddy`。
- 代码更新流程：commit → `git archive`+scp → `build-image.sh` → `deploy-newapi.sh update` → `docker restart caddy`。
- **未提交**：relay 回写代码+测试、deploy/china 全部新增/修改、Dockerfile。
- 安全：3000/3306/6379 仍公网暴露，建议安全组只留 80/443/22。
- 未覆盖泄漏路径（当前拓扑用不到）：`/v1/responses` 透传、Gemini/Anthropic 官方渠道、realtime、任务 `upstream_model_name`、错误文案引用上游模型名。
- 旧入口 `019fbc8c….a8g1v3.xyz` 仍指向 :3000；playground 旧会话历史里残留报错文本属显示残留。
