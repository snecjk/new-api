# Proposal

## Why

当前 new-api 的 relay 请求已有本地日志、usage 和计费记录，但没有向用户指定的 Langfuse Cloud 项目输出调用链。需要把一次请求、渠道重试、模型、耗时和 token 使用情况关联起来，便于在云端排查调用问题，同时保证追踪故障不影响代理与扣费。

## What Changes

- Go 后端通过 OpenTelemetry Go SDK 和 OTLP/HTTP 直连美国区 `https://us.cloud.langfuse.com`，不部署 Langfuse 服务或 Collector。使用项目级 Public/Secret Key，由凭据决定写入项目。
- 采用用户最近提供的项目凭据作为部署依据，Public Key 仅出现在专用部署示例中，不写入程序默认值；Secret Key 保留占位符，必须由部署环境提供完整值。明确区分本地格式校验、Cloud 鉴权与真实调用链验收。
- 新增可关闭的环境变量配置、受限的异步批量导出、请求级采样，以及退出时有超时限制的 flush/shutdown。
- 首期覆盖进入统一 Relay 的 OpenAI Chat Completions、OpenAI Responses（含 compact）、Claude Messages 和 Gemini 文本生成链路，以及 Chat Completions 转 Responses 路径。流式与非流式均记录；一次请求一个根 span，每次实际上游调用一个 generation，重试不拆成多个请求 trace。
- 记录 request ID、协议、原始/实际模型、渠道 ID、重试序号、usage 来源、流终止原因及可获得的首响应时间。usage 只作观测快照，不改变原有归一化或计费规则。
- 默认仅输出白名单元数据；显式开启后，才捕获受大小限制的文本输入/输出。密钥、认证头、用户邮箱、原始请求头、媒体内容及不受支持的 opaque 内容不上传。
- 按当前 Langfuse v4 约定导出完整 observation，设置实时 ingestion header，并把用于筛选的共同属性附加到所有相关 span。
- 补充配置文档、脱敏与故障隔离回归测试，以及无需真实凭据的 OTLP 集成测试；Cloud 验收先通过现有 Chat Completions 接口产生第一条真实 trace，再扩展流式、多协议及重试验证，避免只验证请求已发送就宣称成功。

不在首期实现：管理后台配置页面、历史日志回填、Prompt Management、评测、音视频/图片/embedding/rerank/异步任务/Realtime WebSocket 专项追踪、业务级 agent/tool 内部链路、自动跨服务父子 trace 传播。网关只能记录它实际经手的请求，不能还原客户端内部调用。无业务 API 破坏性变更。

## Capabilities

### New Capabilities

- `relay-observability`: relay 调用链向 Langfuse Cloud 的可选导出，包括请求/尝试生命周期、文本捕获边界、usage 关联、采样、故障隔离与有界退出。

### Modified Capabilities

无。当前 `openspec list --specs` 未发现既有 capability。

## Impact

- 生命周期：`main.go` 现有 `.env` 加载、HTTP 启动与 `srv.Shutdown` 路径。
- 埋点：`controller/relay.go`、`relay/common/relay_info.go`、文本/Responses/Claude/Gemini helper、`relay/chat_completions_via_responses.go`、主要响应解析器和现有 `StreamStatus`。
- 新增独立的根模块观测包（建议 `pkg/observability`）；`go.mod` / `go.sum` 加入版本相容的 OTEL SDK 和 HTTP exporter。当前 OTEL API/trace 仅为 v1.34.0 间接依赖，尚无 relay 追踪实现。
- 配置/文档：`.env.example` 与接入说明。数据库、前端、计费表达式和 `relaykit/` 公共 API 无需修改；保持 relaykit 可独立构建。
- 外部依赖：运行实例可通过 HTTPS 访问美国区 Langfuse Cloud；真实联调需要完整 Secret Key。当前用户提供的 Secret Key 已遮罩，不能用于鉴权；不得把该遮罩值当作可用凭据或写进仓库。
- 开发辅助：官方 Langfuse agent skill 帮助编码代理查阅实践，不是应用运行时依赖；单独安装 skill、创建 API Key 或设置环境变量均不等同于已实现追踪。
