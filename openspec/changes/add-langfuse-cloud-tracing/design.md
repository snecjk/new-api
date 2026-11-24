# Design

## Context

动机与首期范围见 `proposal.md`，行为契约见 `specs/relay-observability/spec.md`。以下为本次只读检查确认的现状：

| 位置 | 已有行为与设计依据 |
| --- | --- |
| `main.go` | `InitResources` 加载 `.env`；SIGINT/SIGTERM 后执行 `srv.Shutdown`，已有 HTTP 排空阶段 |
| `controller/relay.go` | `Relay` 完成验证、RelayInfo、预扣费、渠道重试与协议分发，成功后可能直接 return |
| `middleware/request-id.go` | 生成 request ID，放入 Gin 与 request context 并返回响应头 |
| `relay/common/relay_info.go` | 已有 RequestId、OriginModelName、UpstreamModelName、RetryIndex、FirstResponseTime、StreamStatus 等；同一对象用于重试，另含大量秘密字段，不能整包序列化 |
| 文本、Responses、Claude、Gemini helper | `DoResponse` 完成后取得 `*dto.Usage` 并进入计费；部分路径调用 `PostAudioConsumeQuota`，不能只在文本结算里埋点 |
| `relay/chat_completions_via_responses.go` | 存在 Chat 转 Responses 的独立路径，不能遗漏或重复记录同一上游调用 |
| `relay/channel/*` 响应解析器 | 普通响应与 SSE 内容已在这些位置解析，优先复用解析结果，避免再次消费 Body |
| `relay/helper/stream_scanner.go`、`relay/common/stream_status.go` | 已有首响应打点、终止原因和软错误；`IsNormalEnd` 对部分 handler_stop 返回 true，需同时判断 EndError |
| `relaykit/dto/openai_response.go` | Usage 包含 UsageSource、UsageSemantic、缓存/推理字段；计费还可能使用 BillingUsage |
| `go.mod` | OTEL API/trace v1.34.0 为间接依赖，未发现 relay Langfuse 初始化或 exporter |

目标查看项目为 `cmwtkpt3c024dad0dvz4xhvtx`，Cloud 主机为 `https://us.cloud.langfuse.com`。项目 ID 仅用于验收定位，不参与 ingestion URL 构造；须使用这个项目的密钥。遮罩的 Secret Key 无法执行真实联调。

本次更新以用户最近提供的 `pk-lf-30306af3-2e67-4762-8868-5c316f076099` 作为专用部署示例，替代前一轮提供的 Public Key；该值不是程序默认值，也不能据此验证新密钥确实属于上述项目。Cloud 验收须确认项目归属。最近提供的 Secret Key 仍是遮罩展示，本设计不保存其片段或声称具备完整密钥。

## Goals / Non-Goals

**Goals:** 使用根模块内、可关闭且有资源上限的追踪设施；一次请求串联所有实际尝试；对 SSE 和计费保持旁路；把 Cloud 协议映射集中起来。

**Non-Goals:** 不改变渠道配置或把模型请求改发 Langfuse Gateway；不修改计费表达式、数据库结构或 relaykit；不注入 upstream tracing/baggage header；不承诺异常退出、网络持续故障时追踪无损。

## Decisions

### 1. 使用 Go 官方 OTEL SDK + OTLP/HTTP 直连 Cloud

链路：`调用方 → new-api relay → 现有上游模型`；观测旁路：`已完成 span → 有界 batch processor → Langfuse Cloud`。无需 Python/Node sidecar、Collector 或自托管 Langfuse。与新增每个供应商专用 SDK 相比，直接覆盖网关真实控制的边界，避免更换请求栈。

建议新增 `pkg/observability`，管理配置、TracerProvider、Langfuse 属性映射、不可变快照、采样、文本上限和限频错误报告；它不导入 controller/relay/service。relay 层将白名单标量和通用快照传入，避免包循环依赖。必要的每次尝试状态在根模块 RelayInfo 或其 request context 中持有；不把根模块依赖下沉到 `relaykit/`。

使用专有 provider/tracer，不全局替换其他可能存在的 OTEL provider。SDK/exporter 与已有 OTEL API 选择相容版本并锁定，先考虑同一稳定版本系列；不为本功能执行无关依赖升级。

### 2. 固定正确的美国区 ingestion 边界

- 应用读取 `LANGFUSE_BASE_URL`，规范化尾部 `/` 后拼接 `/api/public/otel/v1/traces`；不是控制台的 `/project/.../traces` URL。仅接受 origin（无 path，或单个 `/`），强制 HTTPS 和正常证书验证，不继承 relay 的跳过 TLS 验证开关。
- exporter 使用 HTTP/protobuf；Cloud 不支持 OTLP/gRPC。端点参数要传完整 traces URL，避免 SDK 再拼接错误路径。
- 后端构造 `Authorization: Basic base64(public_key:secret_key)` 与 `x-langfuse-ingestion-version: 4`；不要求操作者另设 OTEL headers，也不输出 header 值。Cloud 401/403 不重试，429/可重试 5xx 按 SDK 限时退避；固定实例出口，拒绝跨 origin 重定向携带凭据。
- 正式配置只面向用户指定的 Cloud；离线测试通过注入内存 exporter、RoundTripper 或 TLS 测试服务验证，不为生产设置增加 insecure 开关。
- 当前官方文档已将 legacy trace/observation ingestion 迁移至 OTLP；不采用旧 `/api/public/ingestion` 或未核验兼容性的社区 Go SDK。

### 3. 环境变量与启动策略

用户提供的 `LANGFUSE_SECRET_KEY`、`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_BASE_URL` 是官方配置约定，但当前 new-api 尚无读取它们并导出追踪的实现；只添加这三项不会自动生成 trace。以下启用、采样等其余变量为本 change **拟新增**的 new-api 配置：

| 变量 | 默认/约束 |
| --- | --- |
| `LANGFUSE_ENABLED` | `false`；必须显式启用 |
| `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` | 无默认值；校验前缀、非空及非遮罩；始终仅后端可见 |
| `LANGFUSE_BASE_URL` | `https://us.cloud.langfuse.com` |
| `LANGFUSE_ENVIRONMENT` | `production`；受限字符串，演示配置用 `development` |
| `LANGFUSE_SAMPLE_RATE` | `1`；有限数，闭区间 `[0,1]`；生产可调整为 `0.1` |
| `LANGFUSE_CAPTURE_CONTENT` | `false` |
| `LANGFUSE_MAX_CONTENT_BYTES` | `8192`；输入/输出各自上限；有效范围 `256..65536` |
| `LANGFUSE_EXPORT_TIMEOUT_SECONDS` | `5`；有效范围 `1..30`，覆盖本次导出及其重试 |
| `LANGFUSE_FLUSH_TIMEOUT_SECONDS` | `5`；有效范围 `1..30` |

禁用时不创建 exporter；启用但缺少密钥、包含 `•`/`*` 遮罩、地址或数值非法时，限频输出脱敏原因并降级关闭。错误配置不打断网关启动，不静默把非法采样率改成全量。网络不可达由后台导出处理，不进行阻塞启动的 Cloud 探测。

上述本地检查不验证密钥有效性或项目归属；只有真实 Cloud 请求及项目内查询能证明它们。若完整 Secret Key 未保存，应在目标项目重新创建一对 Public/Secret Key 并同时更新部署值，不根据 Public Key 或遮罩片段尝试重建。通用 `.env.example` 只保留占位符，实际 `.env` 或 Secret 由操作者配置，不在 OpenSpec 实施中自动创建或覆盖。

初始化在 `.env` 和 logger 加载后、接流量前完成。env 生效遵循现有部署方式；不为前端添加环境变量或 API 配置返回值。

### 4. 请求与实际调用分层，重试各自独立

```text
relay-request                      [type=span，根]
├── generate-response              [type=generation，retry_index=0，失败]
└── generate-response              [type=generation，retry_index=1，成功]
```

`Relay` 内先按支持的协议/操作判断是否启用，再创建根 span，注册覆盖所有早退的结束逻辑。根生命周期包含进入该函数后的验证、预扣费和重试。根结束处读取最终业务错误及流状态，不抛观测错误；若发生 panic，完成失败记录后维持原 panic/recovery 行为。入口前的鉴权和分发拒绝不纳入首期契约。

每轮建立新的 attempt 状态；在 helper 中参数转换成功、真正调用 `adaptor.DoRequest` 前开始 generation，持续到该调用的响应/SSE 处理结束。覆盖 `compatible_handler.go`、`responses_handler.go`、`claude_handler.go`、`gemini_handler.go` 的文本分支和 Chat 转 Responses 独立调用处；转换前失败不伪造 generation。统一记录函数不拥有模型重试逻辑。

开始时保存本轮渠道/模型/起始时间。结束时同步拷贝本轮 usage、内容摘要和终止状态，再让 controller 进入下一轮；不能把可变 RelayInfo/Gin context 指针交给后台读取。每轮从根 context 派生并在结束后恢复，防止重试成为上一次 generation 的子节点。Chat 转 Responses 只由实际调用点创建一条 generation。

不使用通用 Gin response-body recorder：它会对所有路由增加成本、保存潜在秘密，还可能破坏 SSE 的 Flusher/Hijacker 行为。

### 5. Langfuse v4 映射与模型/usage 口径

| 数据 | 映射/处理 |
| --- | --- |
| 根/子类型 | `langfuse.observation.type=span/generation` |
| 稳定名称 | 根 `relay-request`，子 `generate-response`，动态值仅存 metadata |
| 公共筛选字段 | 每条 span 设置 `langfuse.trace.name`、`langfuse.environment`、`langfuse.observation.metadata.request_id`；需要 trace 级筛选时同时设置 `langfuse.trace.metadata.request_id` |
| 模型 | generation 设置 `langfuse.observation.model.name` 为实际发送上游的模型；原始/上游模型、协议转换链另存显式 metadata |
| 渠道/重试 | `langfuse.observation.metadata.channel_id`、`retry_index`、受控 provider/channel 类型；不含原始 URL、渠道密钥或名称自由文本 |
| Token | `langfuse.observation.usage_details` 为 JSON 字符串；明确的输入/输出总量与可获得的 cache/reasoning 明细；另存 `usage_source` 和 `usage_semantic` |
| 输入/输出 | 仅 opt-in 后写 `langfuse.observation.input/output`；根记录请求文本/最终返回文本，子记录对应调用可识别的文本 |
| 流信息 | `first_response_ms`、`stream_end_reason`、受控错误码/分类、耗时；有可靠时间再写 `langfuse.observation.completion_start_time` |
| 结果 | 设置 OTEL status 和对应 Langfuse level/status message；status message 只含受控分类，不放上游原始错误 |

共同属性显式附在每个相关 span 上；首期不使用对外传播的 baggage，防止元数据随模型调用流出。OTEL 自动生成合法 trace/span ID；业务 request ID 另存，不把任意 request ID 字符串强塞为 OTEL trace ID。

usage 在 helper 的现有 `DoResponse` 返回处按值拷贝，早于 `PostTextConsumeQuota`/`PostAudioConsumeQuota`，保留 UsageSource/UsageSemantic，不读取被计费重新解释后的值替代实际模型 usage。具体口径按现有协议语义映射：OpenAI 的缓存通常是 input 的子集；Anthropic 读/写缓存口径不同。保留原始数值为有界 metadata、输出不重复相加的 Langfuse usage 分类，使用固定 fixture 断言，不能统一盲加。未到达 usage 的失败尝试不填伪造 token。JSON 处理统一用 `common.Marshal/Unmarshal`。

Langfuse 可按识别模型计算自己的参考成本，但该值不代表 new-api 的分组倍率、优惠或最终扣费。首期不发送 `cost_details`，不把 quota 当 USD；需要对账时另立计费成本能力，不改本次范围。

### 6. SSE 与正文捕获使用已解析字段

现有 `FirstResponseTime` 来自上游 data 处理，命名保持“首响应”，不承诺 TTFT。重试各自保存首响应状态，保活 ping 不计入；无可靠值则省略。生成结束时同时读取 `StreamStatus.EndReason`、EndError 和软错误数，不能单用 `IsNormalEnd()`；其中 EOF 的正常/异常含义由既有协议完成标记和解析结果决定，不重新定义业务协议。

正文默认完全跳过。显式开启且被采样时，才在已解析的请求/响应上提取白名单文本：主要 OpenAI、Responses、Claude、Gemini 解析器与转换响应路径附加有界收集点，不替换 reader/writer、不重复读取请求体、不修改原解析对象。兼容其他上游但不能识别的内容使用 `capture_status=unsupported`，仍输出 metadata。

SSE 只向固定上限的字节缓冲追加允许文本；达到上限后停止收集并保持转发。采用 UTF-8 安全截断，记录 `input_truncated/output_truncated`；序列化使用有界文本片段，再由 SDK 发送，不对整个请求调用 Marshal 后截断。排除思考/工具参数/任意 metadata、媒体字节、base64、资源 URL 和原始 upstream 错误体。opt-in 不能保证自由文本自动脱敏，文档必须明确。

### 7. 资源与故障隔离

batch processor 初始建议队列 512 spans、批次 64、间隔 1 秒，队列满非阻塞丢弃。另对 span 设置属性数/值长度硬上限（例如 64 个属性，单个值上限结合编码后正文预算），防止只有队列数量受限而单 span 无界。正文 raw 字节限制和 JSON 转义膨胀后的最终 attribute 限制都必须覆盖，截断标记不能丢失。

后台 exporter 使用独立 HTTP transport；不复用携带上游密钥的 relay client 或依赖模型重试设置。错误 hook 只打印本地受控原因和状态码，不能直接记录可能含 URL/header/payload 的原始 exporter error。对连续失败限频诊断；使用带丢弃计数的 processor/exporter 观测或 SDK 相应接口暴露队列满丢弃，禁止每次请求另开 goroutine 执行同步 HTTP 上报。

HTTP drain 结束后，以 `context.Background()` 派生独立 `LANGFUSE_FLUSH_TIMEOUT_SECONDS` 上下文执行 provider shutdown（包含待完成 batch flush），不复用可能已超时的 drain/request context。对仍未结束的超长请求不承诺捕获完整；SIGKILL、崩溃或断网时允许丢失。

## Risks / Trade-offs

- [文本会离开应用所在区域并发往美国区] → 默认关闭内容；只传白名单元数据；由操作者显式选择正文捕获。
- [每轮复用 RelayInfo 引起串数据] → attempt 独立状态、结束时拷贝，回归覆盖失败后成功与全部失败。
- [流已返回 200 但中途异常] → 联合流终止原因/错误判定，测试客户端断开与 usage 未到达。
- [采样、队列丢弃使 Cloud 与请求总量不一致] → 文档解释观测用途；保留本地计费与日志作为业务依据，提供丢弃诊断。
- [模型别名与不同供应商 usage 口径] → 原始/实际模型与来源分离，精确 fixture 验证缓存明细；Cloud 成本不作为账单。
- [供应商过多，不能首期复原全部正文] → 统一元数据先覆盖约定文本协议，正文仅支持已实现解析器，其余标记 unsupported。
- [尚无可用 Secret Key] → 用内存/TLS fixture 验证协议；真实 Cloud 验收保持待执行，不把模拟成功写成云端成功。

## Migration Plan

1. 实施后默认 `LANGFUSE_ENABLED=false` 发布；无需迁移数据库、渠道或前端。
2. 在测试实例后端配置最近创建的同一对完整项目密钥、美国区地址、`development` 环境、100% 采样，内容保持关闭。Secret Key 仅在部署环境提供；缺失时先完成开发与离线测试，把 Cloud 联调单独保留待办。容器部署通过实际 env 注入，修改 `.env.example` 本身不会配置容器。
3. 先通过现有 `/v1/chat/completions` 发起一次真实非流式请求，使用独立的 new-api token 和实例实际可用模型，保存响应的 `X-Oneapi-Request-Id`（与 `common.RequestIdKey` 一致）。等模型请求完成、batch 导出后，在指定项目按 request ID 查找根和 generation，核对模型、结果、可获得的 usage 与正文默认不上传。以 60 秒作为本项目人工验收观察窗口，不把它描述成 Langfuse Cloud 的可用性保证；找不到记录则保留失败状态并检查配置、筛选和诊断，不盲目重发模型调用。
4. 第一条 trace 验收通过后，再发起四种协议的流式/非流式测试；包括 Chat 转 Responses、一个可控的渠道重试和一个中断流案例，逐一核对 request ID 下的关联关系。
5. 仅在需要看对话文本时开启正文捕获，用非敏感输入验证截断及多字节文本；不要使用生产密钥或私人数据作为测试 prompt。
6. 生产按流量设置采样，观察业务响应、SSE、CPU/内存及 exporter 失败诊断；关闭 Cloud 或返回故障时验证业务不受其结果控制。
7. 回滚只需关闭开关并重启/滚动实例；不需回滚数据库。开关不支持首期热更新。

专用部署配置示例（仅为实施完成后的使用方式；Public Key 使用用户本轮提供值，Secret Key 必须替换为与之配对的完整值；通用 `.env.example` 仍使用占位凭据）：

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

官方 onboarding 的 agent skill 安装提示属于编码代理工具配置，不是 Go tracing SDK。实施时可依官方说明安装并阅读 Langfuse skill，结合 Go/OTLP 适用文档实施；应用运行和离线测试不依赖该 skill，不额外引入 Python/Node 服务。不把安装 skill、创建 API Key、启动服务或健康检查成功写成“第一条 trace 已出现”。

## References

2026-11-24 查阅的官方资料；具体包版本以实施时锁定及测试结果为准：

- [Tracing get started](https://langfuse.com/docs/observability/get-started)：Cloud 区域、项目凭据与直接 OTEL 接入。
- [Native OpenTelemetry](https://langfuse.com/integrations/native/opentelemetry)：HTTP endpoint、Basic Auth、v4 header、属性映射与多 span 筛选。
- [Custom ingestion migration to v4](https://langfuse.com/integrations/native/opentelemetry/migration-to-v4)：完整不可变 span、observation 输入/输出与共同属性传播。
- [Trace best practices](https://langfuse.com/docs/observability/best-practices)：单工作单元、稳定名称及每次模型调用一个 generation。
- [Official agent skills](https://github.com/langfuse/skills)：辅助 coding agent 的工作指导，不是应用运行时 tracing SDK；安装本身不会产生 trace。
