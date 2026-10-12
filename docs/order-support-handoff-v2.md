# 商城订单自动客服交付 v2

需求已于 2026-10-12 确认开发。本文描述实现契约，不代表生产已发布或真实浏览器验收已经完成。

## 目标与边界

客户点击一次“生成单号”，原订单成功落库后自动打开当前访客的客服会话，自动发送订单号、商品名称和处理请求。只有接收端消息已保存、同身份会话已读取并渲染该消息，才展示“订单已发送给客服”。正常路径不访问剪贴板；复制只保留为客户主动选择的备用操作。

保留服务端定价、推荐人归属、订单付款状态、人工客服、历史消息和草稿。新会话继续使用已经部署的微信二维码欢迎规则，不另造欢迎逻辑。不新增消息中间件，不承诺页面关闭后仍在后台自动投递。

## 状态与权限

1. 生成 UUID 请求键并将必要输入快照持久化到本地后，才能发起订单写请求。
2. `/api/orders/session` 首次签发匿名所有者 Cookie；后续请求回传有效 Cookie 后才返回 `ready:true` 和 CSRF 值。不能通过首次响应“自证”Cookie 已保存。
3. 服务端在同一事务中保存订单、所有者＋请求键映射和客服交付记录。相同 owner/key/input 返回原单；输入冲突拒绝；另一次明确购买操作使用新 key。
4. SDK 确认真实身份、历史和会话路由 ready。网站发送端通过 Chatwoot 的认证 capability 再核对访客绑定，首次以 CAS 固定绑定，不接受客户端 contact/conversation ID 作为权限。
5. 接收侧在真实 PostgreSQL 事务和唯一约束下保存稳定订单 UUID → 消息回执。响应丢失后重试同 key 返回同 message ID，跨访客或内容冲突拒绝。
6. 网站保存回执，再通过 SDK 确认当前 generation/binding 中该消息确实可见。显示确认失败只恢复读取，不重新发送已确认保存的消息。

| 服务端状态 | 界面/恢复要求 |
|---|---|
| `awaiting_session` | 原单已保存，等待客服身份 |
| `sending` | 有持久发送 lease；并发请求不同时发送 |
| `sent` | 已保存真实回执；前端仍须确认可见 |
| `retry_wait` | 明确可重试故障，前台有界恢复 |
| `reconciling` | 结果不明，使用接收侧同 key 对账/恢复 |
| `action_required` | 保留订单和可见重试入口，不假装已送达 |

前台一次自动恢复最多 3 次、总预算 30 秒。刷新后按原 owner＋请求键恢复；未知订单 ID 不构成重新下单的理由。owner 丢失/过期、跨设备、清除站点数据后不承诺自动找回，也不能只凭订单号查询或补发。客户主动关闭客服或离开页面会取消当前运行，不在迟到回执后强行重开。

生产 owner Cookie 为 `__Host-fp_order_owner`（Secure、HttpOnly、Path=/，有效期 30 天）；本地非 HTTPS 测试用 `fp_order_owner`。凭据由已有 `COOKIE_SECRET` 签名，数据库只保存 owner 摘要，不保存明文客服 token。现代写接口要求允许的同源 Origin、owner Cookie 和 `X-Order-CSRF`；查询响应 `no-store`。

## 接口契约

| 接口 | 契约 |
|---|---|
| `GET /api/orders/session` | `{ready:false}` 或 `{ready:true,csrfToken}` |
| `POST /api/orders` | `Idempotency-Key`、`X-Order-CSRF`；输入 `productSlug,recommenderWechatId`；201 首次/200 重放 |
| `POST /api/orders/recover` | 同 owner/CSRF，body `{requestKey}`；找回原请求，不创建订单 |
| `GET /api/orders/{id}/support-delivery` | 仅所有者可读，返回 `supportDelivery` |
| `POST /api/orders/{id}/support-delivery` | 同 owner/CSRF；body `{expectedBindingId}`；发送/恢复原单，不创建订单 |

订单响应保留 `order` 和旧兼容字段 `supportMessageStatus`，新增 `supportDelivery:{status,bindingId?,messageId?,conversationId?,attempts?,errorCode?}`。未绑定或无回执时省略相应字段；`sent` 必须有正 message/conversation ID 和 binding。无 `Idempotency-Key` 的旧客户端保留旧接口行为，不承诺它具备本协议的恢复能力。

主要错误：`ORDER_NOT_FOUND` 404、`ORDER_OWNER_REQUIRED` 401、`ORDER_CSRF_INVALID` 403、`IDEMPOTENCY_CONFLICT` / `SUPPORT_BINDING_MISMATCH` 409。异主请求返回不泄露的不存在结果，不暴露订单归属。

Chatwoot 4.17.1 固定上游 `b354a9550e1fb59fa537a9c384232cb076213e72` 增加：

- 认证 `GET /api/v1/widget/freedompost_handoff`：协议 1、HMAC binding、接收端幂等 capability。
- 专用 `POST /api/v1/widget/freedompost_handoff/messages`：必须携带 `message.freedompost_delivery_key`，追加可验证的 `freedompost_delivery` 回执；普通手工消息保持原入口。即使能力检查后客服回滚为旧版，专用入口也只会拒绝，不得回退到会忽略去重字段的旧消息入口。
- SDK `ensureConversation({timeoutMs,signal})` 与 `confirmMessage({messageId,generation,bindingId,conversationId?,timeoutMs,signal})`。postMessage 校验版本、requestId、origin、source 与 generation；旧 SDK 不允许退回盲发。

## 测试矩阵

| 验收/风险 | 可执行验证 | 证据层与限制 |
|---|---|---|
| 正常路径不复制/不事后 setUser，状态在 dialog 外 | `market-order-regression.test.ts`、`order-handoff-portal.test.ts` | 源码回归及实际模板/portal 的 DOM 测试 |
| 首次 owner 确认、CSRF、异主、输入冲突 | `internal/httpapi/order_handoff*_test.go` | 实际 HTTP handler；数据库场景使用隔离 PostgreSQL |
| 同请求并发只一单、响应丢失恢复 | `internal/repository/order_handoff_integration_test.go` | 真实 PostgreSQL 唯一约束/事务；不是 mock |
| CAS 绑定、lease 恢复、陈旧回执不覆盖 | 同上 | 真实 PostgreSQL 并发 |
| 超时/坏回执/重定向/身份冲突 | `internal/chatwoot/handoff_test.go` | 真实 TLS HTTP 边界替身，不是真实 Rails |
| owner 丢失不重建、断网重试原单、sent 不重发、取消 | `order-handoff.test.ts` | 定时器、网络与 SDK 边界单元测试 |
| 消息可见确认、来源校验、切身份和关闭 | Chatwoot 固定源码 SDK/App 测试 | Vue/router/store/DOM；不等于浏览器全链路 |
| 接收端并发/响应丢失/跨访客隔离 | `freedompost-chatwoot/deploy/order-handoff/integration_test.rb` | 实际固定 Rails 路由、模型、回调和 PostgreSQL |
| 旧客服版本不能先上线新网站 | `deploy/chatwoot-handoff-preflight.test.mjs` | metadata 是发布顺序门禁；发送时仍核验认证 capability |
| 新访客点击→同会话消息→二维码、历史/草稿、390px | 隔离浏览器手工/自动化验收 | 必须记录真实观察；不能用接口成功代替 |

实际命令、计数、耗时、Red→Green 和 NOT RUN 记录到本次测试报告；不得因有测试文件就认定已通过。

## 发布、迁移与回滚

先发布兼容的 Chatwoot 协议、接收侧存储与 SDK，再发布网站及 `0014_order_support_handoff.sql`。遵循确认方案中的既有受控 main 流程：FreedomPost 通过受保护 main 发布；Chatwoot 遵循自身既有 reviewed PR、全部相关 CI、main 完整 SHA、手动 production 工作流与回滚点流程。开发分支不能直接部署。metadata 的 `supportHandoffProtocol:1` 只是必要条件，不替代实际认证路由和迁移检查。

治理核对（2026-10-12）：网站 main 的平台保护已启用；私有 Chatwoot 仓库没有平台分支保护，其 GitHub 计划不支持该能力。原始已确认方案写的是“既有受控 main 完整 SHA 流程”，没有新增客服平台保护前置条件；先前把“两仓库受控”扩写为“两仓库受保护”属本文归纳错误，现予更正。没有修改仓库可见性、计费或访问权限，也不把人工审查等同于平台强制保护。

网站新增 `affiliate_order_requests` 与 `affiliate_order_support_deliveries`；不重写既有订单/价格/付款状态。客服迁移只增加接收映射，不删除历史消息。上线前保存配置和旧镜像回滚点；回滚应用时保留新增表、原订单、交付状态和已保存消息，不通过删除幂等记录“重置”。

记录两个运行时完整 SHA、迁移核验、网站 binding、受控 QA 订单号及脱敏消息回执。不得记录 Cookie、访客 token 或管理凭据。生产发布和生产真实消息验收须另有明确授权。
