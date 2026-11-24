# Tasks

应用实现与离线修复已完成；以下复选框按实际验证结果更新，真实 Cloud 联调仍待执行。Go 测试采用确定性 fixture，setup/fatal 用 require，值断言用 assert；不得使用真实密钥或随机压力/睡眠测试代替行为验证。验证命令、计费与导出故障 fixture 以及既有 Gemini 并发测试限制见 docs/langfuse-cloud-tracing.md 第 13 节。

## 1. 观测包、配置与 Cloud exporter

- [x] 1.1 在根模块新增 `pkg/observability`，锁定相容的 OTEL API/SDK/HTTP exporter 依赖；验证根模块编译且该包不依赖 controller/relay/service，`relaykit/go.mod` 与公共 API 不变。
- [x] 1.2 实现 design 配置表的默认值、范围、URL origin 和遮罩密钥校验及禁用路径；表驱动测试覆盖缺失/合法/遮罩凭据、NaN/Inf/越界采样、错误 URL 与关闭时零导出网络请求。
- [x] 1.3 实现美国区完整 traces endpoint、Basic Auth、v4 header、独立 TLS transport 与跨 origin redirect 拒绝；用 TLS/transport fixture 解码 OTLP 并断言 path/header，不在日志或断言失败输出中暴露凭据。
- [x] 1.4 配置有界队列/批次、非阻塞丢弃、attribute 上限、超时及重试；用可控 exporter 验证队列满/401/403/429/5xx/超时的行为、限频脱敏诊断和丢弃计数，不用 sleep 或随机负载判断结果。
- [x] 1.5 为上述配置补充 `.env.example` 的注释示例和接入说明初稿，明确这些变量是新增配置、默认关闭、容器须注入 env、密钥仅服务端使用；验证示例只含占位凭据并与配置默认值一致。
- [x] 1.6 验证项目密钥只取自部署配置、无内置 Public/Secret Key，通用示例使用占位符；补充密钥配对、遮罩不可用、遗失后重新创建配对密钥的说明，并测试格式通过但 Cloud 返回 401/403 时不会误报鉴权或验收成功。

## 2. 应用与请求生命周期

- [x] 2.1 在 `main.go` 的 env/logger 初始化之后接入 provider，在 HTTP drain 后以独立超时上下文 shutdown；测试启用失败不阻止启动、已取消请求不影响导出及 shutdown 截止时间。
- [x] 2.2 在 `controller/relay.go` 为支持的协议/操作创建请求根 span，保存一次采样决策，覆盖验证失败、预扣费失败、全部重试失败、成功早退与 panic；使用内存 exporter 检查根状态且不改变原有错误响应。
- [x] 2.3 使用请求范围的独立 attempt 状态，在每轮开始重置观测数据，保存 request ID 和共同筛选属性；测试父子 context、采样率 0/1、重试间无 usage/首响应/内容残留，不向上游注入认证或 baggage。
- [x] 2.4 更新接入说明中的追踪范围及生命周期，说明入口前鉴权/限流拒绝和首期排除操作不承诺追踪；对照 router 的支持路由验证文字准确。

## 3. 实际模型调用与 usage

- [x] 3.1 在 `relay/compatible_handler.go`、`relay/responses_handler.go` 实际 DoRequest 前开始 generation，覆盖 compact 和 `relay/chat_completions_via_responses.go` 的独立调用；测试转换失败不产生虚构 generation，单次实际调用只记录一条。
- [x] 3.2 在 `relay/claude_handler.go` 和 `relay/gemini_handler.go` 文本分支接入相同生命周期；用各协议非流式 fixture 验证原始/上游模型、渠道、错误及共同属性，embedding 等排除分支不启用。
- [x] 3.3 从现有 DoResponse 返回的 usage 建立只读快照并映射 Langfuse usage_details；fixture 分别覆盖 OpenAI 缓存、Anthropic 读/写缓存、Gemini、Responses、推理明细、估算来源和 usage 缺失，断言不重复计数且不修改原始 DTO。
- [x] 3.4 增加渠道先失败后成功和全部失败的端到端 fixture；断言同一 trace 下 generation 为兄弟节点、失败尝试无虚构 usage，最终根结果正确，未泄露渠道/API 认证信息。
- [x] 3.5 补充 usage/模型/Cloud 参考成本说明，明确不写 quota 为 USD；在启用/禁用追踪的固定结算 fixture 中核对相同的预扣、结算、退款和响应结果，不改现有计费计算。

## 4. SSE 与显式文本捕获

- [x] 4.1 联合既有 StreamStatus、EndError、软错误与协议完成结果完成 generation；固定流 fixture 覆盖正常、EOF、handler_stop 带错误、超时、client_gone、scanner_error 及 HTTP 200 后中断，断言不误报成功。
- [x] 4.2 在已有首响应数据点记录每次尝试的可用时间并排除 ping；fixture 验证未响应时不伪造耗时、重试不沿用前次时间、字段不被标成精确 TTFT。
- [x] 4.3 实现只在 enabled+sampled+capture_content 时工作的输入提取与有界文本收集器；测试默认无正文、UTF-8 截断、JSON 转义膨胀上限、截断标记及跳过媒体/opaque/工具参数。
- [x] 4.4 在主要 OpenAI、Responses、Claude、Gemini 响应解析器及 Chat 转 Responses 路径接入已解析文本收集，记录根最终输出与对应 generation 输出；SSE fixture 断言 chunk 内容/顺序和 Flusher 行为与禁用时一致，未支持内容明确标记。
- [x] 4.5 增加含测试用秘密标记的 metadata/header/error fixture，断言导出 payload 与 exporter 日志不含被排除字段；文档明确自由文本 opt-in 不能保证自动脱敏，并说明模型协议与 provider 正文捕获支持边界。

## 5. 故障集成与操作验收

- [x] 5.1 运行 HTTP/SSE 与 OTLP fixture 的联合用例，在 exporter 阻塞/拒绝鉴权/限流/断网模拟下断言模型调用结果、计费与重试次数不受其返回值控制；检查有界队列丢弃和 shutdown，执行相关包 `go test -race` 排查快照竞争。
- [x] 5.2 运行新增观测包、controller、relay、service、common 的相关测试及根模块构建；另执行 `cd relaykit && GOWORK=off go build ./...` 确认独立构建。记录命令和结果，区分既有失败与本次回归，不为本功能修改无关测试。
- [x] 5.3 在部署接入说明中提供可复制的各协议请求、SSE 请求、重试/中断流步骤、request ID 查询方法、采样与回滚步骤；使用占位 new-api token/模型，离线验证命令结构和真实支持路由，不把 Langfuse 项目密钥用作模型调用 token。注明仅设置三项官方变量或安装 agent skill 不会代替应用埋点；真实 `.env`/Secret 由操作者配置，实施不自动覆盖。
- [ ] 5.4 操作者注入完整配对凭据后，先用现有 Chat Completions 接口完成第一条真实非流式调用；在请求结束及导出后的 60 秒观察窗口内，用响应 request ID 在目标项目定位根与 generation，核对模型、结果、可获得的 usage 和默认无正文。查不到记录则保留本项未完成，记录脱敏诊断，不自动重发模型请求。
- [ ] 5.5 首条真实 trace 验收通过后，在指定 Cloud 项目实际验证四种文本协议、转换路径、重试与中断流，记录 request ID 对应的 Cloud trace 和预期字段；凭据未提供时明确保持本项及 5.4 未完成，不阻塞开发和离线测试，也不将离线 fixture 成功认作 Cloud 验收成功。
- [x] 5.6 执行 `openspec validate add-langfuse-cloud-tracing --strict` 并核对 scope、测试与实现一致；在有真实 Cloud 验收证据之前不宣称接入已完成。

## 6. 扩大正文采集上限（用户追加）

- [x] 6.1 支持每条输入/输出最高 1048576 字节，默认仍为 8192；覆盖配置边界、UTF-8/转义截断、实际 OTLP 大正文与缩小队列的非阻塞丢弃，更新配置说明。
- [x] 6.2 在服务器 `.env` 设置 1048576，仅更新 new-api 镜像，验证健康状态及其他容器未变；使用无敏感合成文本验证 Cloud 可查询的大正文。

## 7. 百万 token 长上下文（用户追加）

- [x] 7.1 将可配置正文预算扩大到输入/输出各 32 MiB，默认仍为 8192；百万 token 用例使用真实分词器确认，验证正文完整、gzip OTLP 与更小队列的丢弃边界，同步文档和规格。
- [x] 7.2 用无敏感合成文本验证 Cloud 读回超过百万 token 的上下文；仅替换生产 new-api 镜像和 dotenv 配置，验证健康、公网访问及其他容器未变。
