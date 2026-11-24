# Spec Delta

## Purpose

为统一模型代理请求提供可选的云端调用链观测，使运维人员能够关联一次请求的渠道尝试、模型、usage 和流终止状态。该能力在默认不上传正文、资源受限和外部服务故障时不影响业务的条件下，支持向指定 Langfuse Cloud 项目输出可查询的记录。

## ADDED Requirements

### Requirement: Optional Cloud configuration
系统 SHALL 仅在显式启用且配置有效时导出追踪；默认关闭。启用时 SHALL 使用服务端配置的美国区 Cloud 地址和完整项目密钥，拒绝非 HTTPS、自带认证信息、查询或 fragment 的地址，以及遮罩或缺失的密钥。配置错误 SHALL 关闭导出并给出脱敏诊断，不得中断业务启动。

#### Scenario: Feature disabled
- **WHEN** 未配置启用开关或开关为 false
- **THEN** 不创建追踪导出网络请求，代理保持原有行为

#### Scenario: Valid US Cloud project credentials
- **WHEN** 显式启用且配置美国区地址和有效格式的项目密钥
- **THEN** 记录发送至该地址对应的 ingestion 接口，由项目密钥确定所属项目
- **AND** 不把项目控制台 URL 或项目 ID 当成 ingestion 地址或凭据

#### Scenario: Masked or incomplete configuration
- **WHEN** Secret Key 含遮罩字符、缺少必需密钥或配置地址不合法
- **THEN** 输出不含密钥的配置诊断并关闭导出，应用仍可接收代理请求

### Requirement: Deployment-owned project credentials
系统 SHALL 从部署环境读取同一 Cloud 项目的完整 Public/Secret Key，不得将用户提供的实际密钥作为代码默认值或客户端配置。通用示例 SHALL 使用占位符；专用部署说明可列出用户提供的 Public Key，但 Secret Key SHALL 始终使用占位符。系统 SHALL 不从遮罩值或 Public Key 推导 Secret Key，不把本地格式校验通过当成 Cloud 鉴权成功。

#### Scenario: Latest supplied deployment credentials
- **WHEN** 操作者按最新部署说明注入完整的配对项目密钥
- **THEN** 导出器使用新注入的配对值，不回退到历史值；程序不内置任何一个历史 Public Key
- **AND** 不混用两次创建所得的 Public/Secret Key，不把 Langfuse 项目密钥用作模型接口认证 token

#### Scenario: Original secret is no longer available
- **WHEN** 操作者只保存了控制台遮罩值且找不到完整 Secret Key
- **THEN** 部署说明指引在目标项目重新创建配对密钥并更新两项环境变量
- **AND** 不使用遮罩字符串发起网络鉴权，也不声称可以恢复该 Secret Key

#### Scenario: Syntactically valid credentials rejected by Cloud
- **WHEN** 密钥通过本地格式校验，但 Cloud 返回 401 或 403
- **THEN** 输出脱敏的鉴权失败分类，不把它视作初始化或接入验收成功
- **AND** 保持模型代理可用，按既定规则终止该批次的鉴权重试

### Requirement: Supported relay scope
系统 SHALL 对统一 relay 入口中的 OpenAI Chat Completions、Responses（含 compact）、Claude Messages、Gemini 文本生成及 Chat 转 Responses 路径提供流式和非流式追踪。系统 SHALL 保持原有请求、响应、模型转换和计费语义。

#### Scenario: Supported text request
- **WHEN** 任一支持的文本协议调用实际进入 relay 处理
- **THEN** 请求在被采样时生成对应追踪，协议转换不额外创建一个根请求

#### Scenario: Unsupported operation
- **WHEN** 请求为图片、音频、视频、embedding、rerank、异步任务或 Realtime WebSocket
- **THEN** 首期专项追踪不启用，该请求保持既有处理行为

### Requirement: Request and attempt correlation
系统 SHALL 为一次被采样的请求创建一个根 observation，为每次实际发起的上游模型调用创建一个子 generation。所有尝试 SHALL 共享有效的 trace ID，记录 request ID、从零开始的重试序号、渠道 ID 和尝试结果。没有发起上游调用时 SHALL 不虚构 generation。

#### Scenario: Retry recovers successfully
- **WHEN** 第一个上游调用失败，后续渠道调用成功
- **THEN** 同一根 observation 下显示失败和成功的子 generation，根结果为成功
- **AND** 不复制前一次尝试的 usage、错误、首响应时间或输出到下一次尝试

#### Scenario: Validation or quota rejection
- **WHEN** 进入 relay 后的验证、渠道选择或预扣费拒绝导致未发起上游调用
- **THEN** 根 observation 记录受限错误分类，且不存在虚构的上游 generation
- **AND** 不要求覆盖在 relay 入口之前就被认证或限流中间件拒绝的请求

### Requirement: Cloud ingestion compatibility
系统 SHALL 使用 Langfuse 支持的 OTLP/HTTP traces 接口，按项目密钥进行 Basic Auth 并发送 `x-langfuse-ingestion-version: 4`。每个 observation SHALL 在结束并补齐最终属性后仅导出一次，不得用重复上传同一 span 更新内容。

#### Scenario: Completed generation export
- **WHEN** 一次生成完成并进入导出批次
- **THEN** 请求发往 `/api/public/otel/v1/traces`，携带正确的鉴权及 v4 header
- **AND** observation 具有有效的 trace/span ID、父级关系、起止时间和 generation 类型
- **AND** 传输重试复用同一批次身份，不制造新的逻辑调用记录

### Requirement: Queryable metadata and model identity
系统 SHALL 在根和每个相关子 observation 上附加用于筛选的共同属性，包括 request ID、稳定操作名和环境。generation SHALL 区分客户端请求模型与实际发送上游的模型，渠道及重试信息 SHALL 使用受控元数据表示，不拼进动态操作名。系统 SHALL 不从 API token、邮箱或请求头自动构造 user/session 身份。

#### Scenario: Model mapping and child filtering
- **WHEN** 客户端模型经过渠道映射后调用另一上游模型
- **THEN** generation 的模型为实际发送上游的模型，原始模型另存元数据
- **AND** 用 request ID 或环境筛选子 observation 时仍可关联同一请求

### Requirement: Usage remains observational
系统 SHALL 读取既有协议解析链路的 usage 快照而不修改它；区分上游报告、既有估算和缺失情况，并保留可获得的缓存/推理明细及语义来源。未知或缺失值 SHALL 不伪装成上游报告的零值，不重复累计 token。首期 SHALL 不将 new-api quota 写成 Langfuse 的美元成本。

#### Scenario: Provider reports usage
- **WHEN** 成功响应含已有解析器可识别的 usage
- **THEN** generation 显示相应输入/输出 token 及可获得的明细，来源和语义可辨识
- **AND** 开启和关闭追踪所得预扣、结算、退款及原始响应保持一致

#### Scenario: Usage absent or partial
- **WHEN** 上游未返回 usage 或流在 usage 到达前中断
- **THEN** 标记 usage 缺失或沿用并标注已有估算来源，不为了追踪新增计费估算
- **AND** 已知部分可记录，未知部分不被当成精确零值

### Requirement: Streaming lifecycle and outcomes
系统 SHALL 在流实际结束后完成 generation，记录可获得的首响应耗时、总耗时、终止原因和错误分类。超时、客户端断开和处理错误 SHALL 与正常完成区分；已写出 HTTP 200 或 helper 返回 nil 不足以判定流成功。首响应 SHALL 不包括保活 ping，且不得伪称首个文本 token 时间。

#### Scenario: Normal stream
- **WHEN** 流按当前协议正常结束
- **THEN** 记录一个完成的 generation 和终止原因；不会为每个 chunk 导出 span
- **AND** chunk 次序、内容和 flush 行为不因追踪而改变

#### Scenario: Stream fails after headers
- **WHEN** HTTP 200 后发生超时、断开、scanner 错误或 handler 停止并附带错误
- **THEN** generation 记录相应失败或取消结果及受限错误分类
- **AND** 不仅依据 HTTP 状态码把请求标为成功

### Requirement: Content privacy by default
系统 SHALL 默认不捕获 prompt、completion、工具参数、媒体、原始请求头或请求/响应正文，只导出明确允许的元数据。任何模式下 SHALL 不从配置、认证头、身份字段或原始错误体提取并上传项目/渠道密钥、客户端 token、Authorization/Cookie、邮箱或未经筛选的错误正文；导出故障日志亦遵守此限制。自由文本捕获遵守下一项的显式启用约束。

#### Scenario: Request contains secrets in excluded fields
- **WHEN** 请求或错误包含认证头、URL key 参数、原始错误正文或 RelayInfo 中的秘密字段
- **THEN** 这些字段不出现在导出 payload 或导出故障日志中
- **AND** 默认模式下输入和输出正文不出现在任何 observation 属性中

### Requirement: Explicit bounded text capture
系统 SHALL 仅在操作者显式开启文本捕获时提取支持协议中可识别的文本字段；捕获内容 SHALL 按 UTF-8 安全截断并标记截断。不支持的 opaque 内容及媒体 SHALL 跳过并标记，不得整包序列化作为兜底。操作者 SHALL 获知任意自由文本可能仍含敏感内容。

#### Scenario: One MiB content capture
- **WHEN** 操作者显式配置 LANGFUSE_MAX_CONTENT_BYTES=1048576 并启用正文捕获
- **THEN** 输入和输出各自支持最多 1 MiB 的 UTF-8/JSON 转义后字节预算，超出仍安全截断
- **AND** 默认值保持 8192；更大的正文使用缩小的有界队列和单 observation 导出批次
- **AND** 该上限表示字节而非模型 token 数，配置调整不回填历史记录

#### Scenario: Million-token context capture
- **WHEN** 操作者显式配置 33554432 字节正文预算并启用正文捕获
- **THEN** 输入和输出各支持 32 MiB 预算，实际 tokenizer 确认的百万 token 用例在预算内完整导出且无截断标记
- **AND** 大正文使用 gzip、单 observation 批次及随正文预算缩小的有界队列，队列满时仍不阻塞模型代理
- **AND** 不修改模型上下文限制或计费，不把所有 tokenizer 的 token 数与字节数当成固定换算

#### Scenario: Oversized text output
- **WHEN** 显式开启捕获且模型输出超过配置的字节上限
- **THEN** 仅保留有界、有效 UTF-8 的文本并设置截断标记
- **AND** 转发给客户端的输出保持完整，不为捕获累积无限流缓冲

#### Scenario: Unsupported or multimodal payload
- **WHEN** 输入或输出包含二进制、base64、签名 URL 或未支持的原始结构
- **THEN** 跳过相关内容并标记 capture 状态，继续导出允许的元数据

### Requirement: Whole-request sampling
系统 SHALL 在请求入口作一次采样决策，并对根及所有重试沿用。采样率 SHALL 为 0 到 1 的有限数；非法值使导出安全关闭。未采样请求 SHALL 不执行正文捕获或额外大对象序列化。

#### Scenario: No sampling
- **WHEN** 采样率为 0
- **THEN** 不导出 observation，且不因正文捕获产生额外大对象副本

#### Scenario: Sampled retry chain
- **WHEN** 请求被采样且发生渠道重试
- **THEN** 根和所有实际上游调用采用同一采样结果，不留下单独被随机采样的孤立重试

### Requirement: Bounded export and failure isolation
系统 SHALL 使用有界后台队列、受限属性大小、请求超时和有界重试。Cloud 返回 401/403/429/5xx、DNS/TLS 故障、超时或队列满时 SHALL 不等待 Cloud 结果再返回业务响应，不触发模型重试、不修改扣费，并提供限频且脱敏的导出失败或丢弃诊断。

#### Scenario: Exporter unavailable
- **WHEN** Cloud 超时、限流、拒绝鉴权或队列满
- **THEN** 业务请求及 SSE 写入继续，按规则重试或丢弃 telemetry
- **AND** 不无限创建 goroutine、积累 payload 或打印凭据

### Requirement: Bounded shutdown and verification
系统 SHALL 在 HTTP 排空后使用独立超时上下文完成可用的追踪 flush/shutdown；已取消的客户端上下文 SHALL 不阻止完成记录导出。进程异常终止时不承诺内存队列无损。交付 SHALL 包含离线协议测试和使用目标项目真实密钥的人工 Cloud 验收说明。

#### Scenario: Graceful shutdown
- **WHEN** 进程收到正常退出信号且 HTTP 排空结束
- **THEN** 尝试在明确截止时间内导出剩余记录，然后继续退出
- **AND** Cloud 不可达不会导致无限等待

#### Scenario: Missing real credentials during delivery
- **WHEN** 仅有遮罩的 Secret Key
- **THEN** 离线测试能够完成，Cloud 写入验收明确标记待执行，不宣称已在目标项目看到 trace

### Requirement: First real trace acceptance
Cloud 接入验收 SHALL 先通过现有模型代理接口完成一次真实、非流式文本调用，再在目标 Cloud 项目检索对应 request ID 的根 observation 和 generation。环境配置存在、agent skill 已安装或 ingestion 返回成功均 SHALL 不单独构成验收通过；不存在完整凭据时 SHALL 允许完成开发和离线验证，单独标记 Cloud 验收待执行。

#### Scenario: First trace visible in the intended project
- **WHEN** 正确配置凭据、启用追踪、采样率为 1，并完成一次非流式 Chat Completions 调用
- **THEN** 在调用结束及导出后的观察窗口内，目标项目可按 request ID 找到正确关联的根和 generation
- **AND** 可核对模型、调用结果及可获得的 usage，默认不含对话正文

#### Scenario: No LLM call has occurred
- **WHEN** 仅启动服务、安装 skill、调用健康检查或查询模型列表
- **THEN** 不要求产生模型 generation，且不能据此宣称真实追踪已验收

#### Scenario: Export accepted but not queryable
- **WHEN** 导出返回成功，但指定观察窗口内在目标项目找不到对应 request ID
- **THEN** Cloud 验收保持未通过，并检查项目/区域、凭据配对、采样、筛选条件及导出诊断
- **AND** 不自动重新发送模型请求或把控制台可见延迟视为扣费失败
