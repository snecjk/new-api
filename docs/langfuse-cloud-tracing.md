# Langfuse Cloud 追踪接入说明

本文档说明 new-api 如何向用户指定的 Langfuse Cloud 项目导出 OTLP 追踪，以及如何在部署环境中启用、配置和验收。

## 1. 概览

new-api 通过 OpenTelemetry Go SDK + OTLP/HTTP 直连美国区 `https://us.cloud.langfuse.com`，不部署 Langfuse 服务或 Collector，不引入 Python/Node sidecar。

- 追踪能力默认**关闭**，必须显式启用。
- 仅统一 Relay 入口中的 OpenAI Chat Completions、OpenAI Responses（含 compact）、Claude Messages、Gemini 文本生成及 Chat 转 Responses 路径提供追踪。
- 图片、音频、视频、embedding、rerank、异步任务、Realtime WebSocket 等非文本操作**首期不启用**专项追踪。
- 追踪故障（Cloud 不可达、鉴权失败、限流、队列满）不影响模型代理与计费，业务请求继续正常返回。

## 2. 环境变量配置

全部配置通过部署环境变量注入，程序不内置任何 Public/Secret Key 默认值。通用 `.env.example` 仅保留占位符，真实 `.env` / Secret 由操作者配置，不在实施中自动创建或覆盖。

| 变量 | 默认值 / 约束 | 说明 |
|------|--------------|------|
| `LANGFUSE_ENABLED` | `false` | 必须显式设为 `true` 才会启用导出 |
| `LANGFUSE_PUBLIC_KEY` | 无默认值 | 项目 Public Key，由部署环境提供 |
| `LANGFUSE_SECRET_KEY` | 无默认值 | 项目 Secret Key，始终仅后端可见，示例中使用占位符 |
| `LANGFUSE_BASE_URL` | `https://us.cloud.langfuse.com` | 仅接受 origin（无 path/query），强制 HTTPS |
| `LANGFUSE_ENVIRONMENT` | `production` | 部署环境标识，演示可用 `development` |
| `LANGFUSE_SAMPLE_RATE` | `1` | 有限数，闭区间 `[0,1]`；生产可调整为 `0.1` |
| `LANGFUSE_CAPTURE_CONTENT` | `false` | 是否捕获输入/输出文本，默认关闭 |
| `LANGFUSE_MAX_CONTENT_BYTES` | `8192` | 有效范围 `[256, 65536]`，输入/输出各自上限 |
| `LANGFUSE_EXPORT_TIMEOUT_SECONDS` | `5` | 有效范围 `[1, 30]` |
| `LANGFUSE_FLUSH_TIMEOUT_SECONDS` | `5` | 有效范围 `[1, 30]`，退出时 flush 截止时间 |

### 专用部署示例（仅供参考，Secret Key 必须替换为配对的完整值）

```dotenv
LANGFUSE_ENABLED=true
LANGFUSE_BASE_URL=https://us.cloud.langfuse.com
LANGFUSE_PUBLIC_KEY=pk-lf-30306af3-2e67-4762-8868-5c316f076099
LANGFUSE_SECRET_KEY=sk-lf-REPLACE_WITH_FULL_SECRET_KEY
LANGFUSE_ENVIRONMENT=development
LANGFUSE_SAMPLE_RATE=1
LANGFUSE_CAPTURE_CONTENT=false
LANGFUSE_MAX_CONTENT_BYTES=8192
```

> **注意**：上述 Public Key 仅用于说明格式，Secret Key 必须替换为与之配对的完整值。遮罩的 Secret Key 无法执行真实联调。如果完整 Secret Key 丢失，应在目标 Langfuse 项目重新创建一对 Public/Secret Key 并同时更新两项环境变量，不根据遮罩片段或 Public Key 尝试重建。

## 3. 启用策略

- 初始化在 `.env` 加载和 logger 初始化之后、网关接流量之前完成。
- 启用开关为 `false` 时不创建任何网络组件，不发送追踪请求，代理保持原有行为。
- 启用但密钥缺失、遮罩、地址非法、采样率越界等配置错误时，输出脱敏诊断并降级关闭导出，**不中断网关启动**。
- 网络不可达由后台导出处理，不进行阻塞启动的 Cloud 探测。

## 4. 追踪范围与生命周期

### 4.1 根 span

每次被采样的 relay 请求创建一个根 observation（`type=span`，名称 `relay-request`），生命周期包含进入 `Relay` 函数后的验证、预扣费和重试。根 span 上附加：

- `langfuse.trace.name` = `relay-request`
- `langfuse.environment` = 部署环境
- `langfuse.observation.metadata.request_id` = 业务请求 ID（与响应头 `X-Oneapi-Request-Id` 一致）
- `langfuse.observation.metadata.protocol` = 协议格式（openai/claude/gemini/openai_responses 等）
- `langfuse.observation.metadata.is_stream` = 是否流式

### 4.2 子 generation

每次实际上游模型调用创建一个子 observation（`type=generation`，名称 `generate-response`），重试序号从 0 开始递增。所有尝试共享同一 trace ID，根下的子 generation 为兄弟关系：

- 第一个调用失败，后续成功 → 根下显示失败和成功的子 generation，根结果为成功
- 重试不复制前一次尝试的 usage、错误、首响应时间或输出
- 验证/预扣费拒绝导致未发起上游调用时，不虚构 generation

### 4.3 入口前拒绝

入口前的鉴权和限流拒绝不进入根 span，首期不承诺追踪。

## 5. 模型与 usage 映射

| 字段 | 处理方式 |
|------|---------|
| `langfuse.observation.model.name` | 实际发送上游的模型（渠道映射后） |
| `metadata.origin_model` | 客户端请求的原始模型名 |
| `metadata.upstream_model` | 实际发送上游的模型名 |
| `metadata.channel_id` | 渠道 ID（受控，不含渠道密钥或自由文本名称） |
| `metadata.retry_index` | 从零开始的重试序号 |
| `metadata.usage_source` / `metadata.usage_semantic` | usage 来源与语义（upstream / estimated 等） |
| `metadata.stream_end_reason` | 流终止原因（done/eof/timeout/client_gone/scanner_error/handler_stop 等） |
| `metadata.first_response_ms` | 首响应耗时（有可靠值时写入，ping 不计入） |

- usage 在 `DoResponse` 返回处按值拷贝，**只读快照**，不修改原始 DTO，不读取被计费重新解释后的值。
- `langfuse.observation.usage_details` 是数值分类：缓存/推理与普通 input/output 互斥，不重复累计；原有 prompt/completion/total 总量另存 metadata。
- 未知或缺失值不伪装为上游报告的零值，不重复累计 token。
- 首期不将 new-api quota 写成 Langfuse 的美元成本，不发送 `cost_details`。

## 6. 内容隐私与文本捕获

### 6.1 默认不捕获正文

默认模式下不捕获 prompt、completion、工具参数、媒体、原始请求头或请求/响应正文，只导出明确允许的元数据。

配置、认证头、身份字段、原始错误体中的项目/渠道密钥、客户端 token、Authorization/Cookie、邮箱或未经筛选的错误正文，**不出现在导出 payload 或导出故障日志中**。

### 6.2 显式开启内容捕获

仅在 `LANGFUSE_CAPTURE_CONTENT=true` 且请求被采样时，才从已解析的请求/响应中提取白名单文本：

- 使用有界文本收集器（`LANGFUSE_MAX_CONTENT_BYTES`，UTF-8 安全截断，记录截断标记）。
- 跳过媒体字节、base64、签名 URL、工具参数、思考内容、任意 metadata。
- 转发给客户端的输出保持完整，不为捕获累积无限流缓冲。
- 未支持的 opaque 内容标记 `capture_status=unsupported`，仍输出允许的元数据。
- 输入取自验证后的客户端请求；输出取自主要 OpenAI/Responses/Claude/Gemini 解析器，不复原其他供应商的私有结构。Responses Input 仅对已知消息/文本块做常量空间扫描与有界解码；compact 的 opaque 内容不上传。文本上限同时约束 UTF-8 字节和 JSON 转义后的属性值。

> 自由文本 opt-in 不能保证自动脱敏，操作者应知悉任意自由文本可能仍含敏感内容。不要使用生产密钥或私人数据作为测试 prompt。

## 7. 故障隔离与资源上限

- 后台 exporter 使用独立 HTTP transport，**不复用**携带上游密钥的 relay HTTP client，强制 TLS 1.2+ 正常证书验证。
- 队列 512 spans、批次 64、间隔 1 秒，队列满**非阻塞丢弃**，丢弃计数可通过诊断读取。
- Cloud 返回 401/403 不重试，429/可重试 5xx 按 SDK 限时退避。
- 错误 hook 限频打印受控分类（authentication_failed/rate_limited/server_error/transport_failed），**不记录可能含 URL/header/payload 的原始 exporter error**。`Exporter.DropCount()` 与 `FailureCount()` 分别提供丢弃与导出失败计数。
- 禁止每次请求另开 goroutine 执行同步 HTTP 上报。

## 8. 退出与 flush

HTTP 排空结束后，以独立 `LANGFUSE_FLUSH_TIMEOUT_SECONDS` 上下文执行 provider shutdown（包含待完成 batch flush），不复用可能已超时的 drain/request context。Cloud 不可达不会导致无限等待。进程异常终止（SIGKILL、崩溃、断网）时允许丢失未导出的记录。

## 9. 验收步骤（离线）

### 9.1 离线协议测试

运行 `go test ./pkg/observability/ -race` 验证配置校验、导出器初始化、usage 快照、文本捕获等离线行为。不需要真实凭据。

### 9.2 服务启动

配置完整凭据后启动 new-api，确认日志中无 `langfuse observability init failed` 错误（或仅为配置错误时的脱敏诊断，业务不受影响）。

### 9.3 第一条真实 trace

1. 使用现有 `/v1/chat/completions` 接口完成一次真实非流式调用，使用独立的 new-api token 和实例实际可用模型。
2. 保存响应的 `X-Oneapi-Request-Id`。
3. 请求结束及导出后，在目标 Langfuse 项目按 request ID 定位根 observation 和 generation，核对模型、结果、可获得的 usage，默认不含对话正文。
4. 以 60 秒作为人工验收观察窗口，不把它描述为 Langfuse Cloud 的可用性保证。
5. 查不到记录则保留失败状态，记录脱敏诊断，不盲目重发模型请求。

### 9.4 扩展验证

第一条 trace 验收通过后，再发起四种协议的流式/非流式测试，包括 Chat 转 Responses、一个可控的渠道重试和一个中断流案例，逐一核对 request ID 下的关联关系。

## 10. 回滚

只需将 `LANGFUSE_ENABLED` 设为 `false` 并重启/滚动实例，不需回滚数据库。开关不支持首期热更新。

## 11. 注意事项

- 仅设置 `LANGFUSE_PUBLIC_KEY`/`LANGFUSE_SECRET_KEY`/`LANGFUSE_BASE_URL` 三项官方变量，**不会**自动生成 trace；必须还启用 `LANGFUSE_ENABLED=true` 并完成应用埋点。
- 安装 Langfuse 官方 agent skill、创建 API Key、启动服务或健康检查成功，**不等同于**已实现追踪，也不能作为 Cloud 验收通过的证据。
- 文本内容会发往美国区 Langfuse Cloud，默认关闭内容捕获，由操作者显式选择开启。
- 在有真实 Cloud 验收证据之前，不宣称接入已完成。

## 12. 可复制请求与验收记录

以下 token 是网关签发的模型接口 token，与 Langfuse 项目密钥不同。容器需通过 compose `environment` / `env_file` 或 Secret 注入 Langfuse 变量；只修改宿主机 `.env.example` 不会配置容器。

```sh
export NEW_API_URL=http://localhost:3000
export NEW_API_TOKEN=REPLACE_WITH_NEW_API_TOKEN
export MODEL=REPLACE_WITH_AVAILABLE_MODEL

# 第一条真实请求；保存 X-Oneapi-Request-Id，不要自动重复模型调用。
curl -sS -D /tmp/new-api-trace-headers -H "Authorization: Bearer $NEW_API_TOKEN" \
  -H 'Content-Type: application/json' "$NEW_API_URL/v1/chat/completions" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply hello\"}],\"stream\":false}"

# Chat SSE；可加入 --max-time 1 制造客户端中断，前提是上游仍在输出。
curl -N -sS -D /tmp/new-api-sse-headers -H "Authorization: Bearer $NEW_API_TOKEN" \
  -H 'Content-Type: application/json' "$NEW_API_URL/v1/chat/completions" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Count to 100\"}],\"stream\":true}"

# Responses；stream:true 测试 SSE。compact 使用已有可压缩会话输入。
curl -sS -H "Authorization: Bearer $NEW_API_TOKEN" -H 'Content-Type: application/json' \
  "$NEW_API_URL/v1/responses" -d "{\"model\":\"$MODEL\",\"input\":\"Reply hello\",\"stream\":false}"

# Claude Messages；选择支持 Claude 协议的实际模型，stream:true 测试 SSE。
curl -sS -H "Authorization: Bearer $NEW_API_TOKEN" -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' "$NEW_API_URL/v1/messages" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"Reply hello\"}],\"stream\":false}"

# Gemini；选择支持 Gemini 协议的实际模型。
curl -sS -H "Authorization: Bearer $NEW_API_TOKEN" -H 'Content-Type: application/json' \
  "$NEW_API_URL/v1beta/models/$MODEL:generateContent" \
  -d '{"contents":[{"role":"user","parts":[{"text":"Reply hello"}]}]}'
curl -N -sS -H "Authorization: Bearer $NEW_API_TOKEN" -H 'Content-Type: application/json' \
  "$NEW_API_URL/v1beta/models/$MODEL:streamGenerateContent?alt=sse" \
  -d '{"contents":[{"role":"user","parts":[{"text":"Reply hello"}]}]}'
```

在项目 `cmwtkpt3c024dad0dvz4xhvtx` 用响应 request ID 筛选 metadata.request_id：应有一个根、每次实际调用一个 generation。先失败后成功应为同根下兄弟节点，根为成功。重试验收使用隔离测试实例的同一模型两条渠道，第一条指向可控错误服务，第二条正常；不要修改生产渠道。转换路径使用实例既有 Chat→Responses 规则。中断 SSE 后检查取消/错误分类，不能只看 HTTP 200。

采样验收可设 `LANGFUSE_SAMPLE_RATE=0` 并重启，确认业务正常且不新增 trace；恢复 `1` 后验证完整链路。401/403 表示该批次鉴权失败，不触发模型重试，也不证明接入完成。

**当前 Cloud 联通验证：已通过本地模拟上游验证。** 完整配对凭据已注入被 Git 忽略的本地环境文件；使用当前源码启动的独立实例已将非流式和 SSE 请求上报至目标 Cloud 项目，并通过 Cloud API 读回核对，详见第 14 节。尚未调用真实模型供应商，也未完成多协议、转换、重试及中断流的完整 Cloud 验收，任务 5.4、5.5 保持未完成。


## 13. 开发验证记录（2026-11-24）

以下离线验证已通过，未向真实 Cloud 项目或模型服务发送验收请求：

    go test ./pkg/observability ./relay ./controller ./relay/channel/openai ./relay/channel/claude ./relay/channel/gemini ./service ./common
    go test -race ./pkg/observability ./relay ./relay/common ./controller ./relay/channel/openai ./relay/channel/claude
    go test -race -parallel 1 ./relay/channel/gemini
    go build ./...
    (cd relaykit && GOWORK=off go build ./...)
    git diff --check
    openspec validate add-langfuse-cloud-tracing --strict

relay_e2e_test.go 使用隔离 SQLite、真实 controller/渠道选择/HTTP 解析器及钱包和令牌结算，覆盖成功、先失败后成功、全部失败退款、预扣费拒绝和 SSE。对比追踪开关前后的预扣、余额、实际扣费、退款、响应字节、flush 和调用次数；模拟 OTLP 阻塞、401、429、断网，验证导出故障不会控制业务返回值。OTLP TLS fixture 另验证 wire 格式、403/5xx、超时、重定向拒绝、队列上限、脱敏与独立 shutdown 截止时间。文档的六条 curl 命令已离线校验 shell 和 JSON 格式。

Langfuse 根据识别模型展示的参考成本，不代表 new-api 的分组倍率、优惠或最终扣费；应以网关结算记录对账。

Gemini 既有测试并行执行 gin.SetMode 会触发包级全局变量竞态；本次未修改这些无关测试。该包使用 -parallel 1 通过竞态检查，其余上述相关包按默认并发通过；不能将此记录表述为整个仓库默认并发竞态检查通过。真实模型及完整 Cloud 5.4、5.5 验收仍待执行。

## 14. 本地启动及 Cloud 联通验证（2026-11-24）

使用当前源码编译服务，在 `http://localhost:13000` 启动；原有 3000 端口容器保持运行。测试使用独立 SQLite 数据库及 `127.0.0.1:18081` 模拟 OpenAI 上游，不调用真实模型供应商。Langfuse 使用目标美国区 Cloud 项目，environment 为 `local-validation`。

| 请求 | request ID | Cloud trace ID | 结果 |
| --- | --- | --- | --- |
| Chat Completions 非流式 | `202611240722322867400008268d9d6Y18t9bEP` | `a9a9b50a0b126bdb59642bcf7e497d56` | HTTP 200 |
| Chat Completions SSE | `202611240722323030840008268d9d6ZJwwefIe` | `c8212867863e83dd4bfd3f3d1c0ca383` | HTTP 200，收到 `[DONE]` |

通过 `/api/public/v2/observations` 读回并断言：两条记录均属于项目 `cmwtkpt3c024dad0dvz4xhvtx`；每条请求一个根 SPAN 和一个 GENERATION，父子关系及 request ID 对应正确；模型为 `gpt-4o-mini`，模拟上游 usage 为 input 10、output 2、total 12；input/output 正文均为空。`/api/status` 返回成功。上述 usage 为测试夹具数据，不代表真实模型消耗；Cloud 展示的模型参考成本也不是实际供应商费用。

测试脚本、独立数据库、服务日志、登录凭据及脱敏验证结果存放于被 Git 忽略的 `.local-tests/langfuse-live/`；登录凭据文件权限为 `0600`。本记录只确认本地网关到真实 Cloud 的上报与读回，不替代任务 5.4、5.5 的完整验收。

### 正文采集验证

按操作者要求，本地 `.env` 已设置 `LANGFUSE_CAPTURE_CONTENT=true` 并重启 13000 端口实例；Compose 已增加该变量透传，未配置时仍默认关闭。新发起的非流式 trace `3698581d0afd47fb60d3c76a97eb088b` 与 SSE trace `5b83be456085c3f473db07306e1fffda` 已通过 Cloud API 读回：根节点及 GENERATION 的 input 均包含 `local observability validation`，output 均为 `local validation ok`。之前关闭正文采集时生成的历史记录仍为空。开启后允许采集的请求/响应文本会发送到美国区 Cloud；采集范围及字节限制继续生效。
