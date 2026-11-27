# 订阅 RPM 与加购

- 套餐 `rpm_limit` 默认 5，范围 1–1000000；购买时快照为订阅 `base_rpm`。管理员修改套餐不追溯影响已有订阅。
- 套餐 `rpm_tiers` 为加购档位数组：`id`、`rpm`、`price_amount`、`enabled`，以及可选的 `stripe_price_id`、`creem_product_id`、`waffo_pancake_product_id`。档位价格范围 0.01–9999，币种 USD；第三方商品应配置为一次性购买，售价须与后台一致。
- 管理员沿用 `POST /api/subscription/admin/plans`、`PUT /api/subscription/admin/plans/:id` 的 `{"plan": {...}}` 请求配置上述字段；更新时提交完整档位数组，保留已有档位的 ID，移除数组中的档位即删除。
- 用户套餐查询仅返回启用档位；订阅查询返回 `base_rpm`、`added_rpm`、`total_rpm`。
- 加购复用 `/api/subscription/{balance,epay,stripe,creem,waffo-pancake}/pay`，提交 `{"subscription_id": 123, "rpm_tier_id": 456}`；易支付额外提交 `payment_method`。服务端确定价格与增量，不接受客户端报价。
- 加购不延长有效期，随绑定订阅到期/取消失效，可多次购买叠加，总 RPM 不超过 1000000。订单记录 `purpose=rpm` 及目标订阅、档位和增量快照。支付成功仅发放一次；付款完成时订阅已失效或叠加超过上限，订单记为 `refund_pending`，保留支付记录供管理员对账退款。
- 实际选中计费订阅后执行 60 秒滚动窗口限流，跨 API 令牌共享计数；超限返回 429，撤销本次预扣，不调用上游。钱包单独计费及原全局/分组限流保持原行为。

## 部署与回滚

先部署后端迁移，再部署前端。已有套餐/订阅补齐 5 基础 RPM、0 加购 RPM，上线前需告知现有用户限流变化。Redis 模式支持多实例，内存模式只用于单实例。加购后直接读取最新订阅权益，无缓存生效延迟。回滚使用上一个版本程序，保留新增列、档位和订单记录；已付款及待退款订单须继续对账，不删除账务数据。


## 验证与回放

在仓库根目录运行：

~~~sh
go test ./model ./controller ./service ./middleware -count=1 -timeout 120s
cd web
bun test src/features/subscriptions/lib/__tests__/rpm-validation.test.ts src/features/subscriptions/components/__tests__/rpm-purchase.test.tsx
bun run typecheck
bun run build
~~~

TestSubscriptionRPMBillingPurchaseAndExpiryReplay 使用独立测试数据及 Redis 测试实例，回放配置档位、余额购买订阅、两个令牌合计 5 次请求、第六次返回 429、余额加购后立即放行、订阅到期及钱包独立计费。断言订阅预扣退还、令牌额度恢复、无额外上游调用且超限不回退钱包。

订单测试覆盖重复/并发回调、重复购买叠加、改价后按原订单发放、过期付款记为待退款。界面测试覆盖基础默认值、档位增删和启停、价格确认、绑定订阅参数、支付成功刷新与失效订阅隐藏购买入口。

迁移在 SQLite 上执行重复验证；MySQL/PostgreSQL 使用同一 GORM 迁移与行锁封装，仍需部署环境验收。第三方支付代码沿用原验签链路，自动测试不触发真实付款；上线前须用各渠道测试商品检查一次性购买、币种、金额和回调，并对待退款订单人工对账。订阅计费请求受此限制；不经过计费选订阅的免费模型请求沿用现有全局/分组限流。
