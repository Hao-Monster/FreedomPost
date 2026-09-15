# FreedomPost 客服微信通道集成设计

状态：基础协议层已实现，生产通道默认关闭。

## 1. 决策结论

个人微信并不是完全不可用，但可用方式与“控制电脑上的个人微信客户端”不同。腾讯维护的 [openclaw-weixin](https://github.com/Tencent/openclaw-weixin) 通过扫码把一个微信账号授权给微信后端机器人协议，支持私聊文本、图片、语音、文件和视频。它不提供对桌面微信窗口的自动化控制，也不是 Chatwoot 原生渠道。上游仓库目前仍有发送返回成功但消息未展示、消息丢失和长回复上下文等公开问题，因此不能把它当作已经稳定的生产客服通道。

企业微信适合作为正式客服主通道：账号、客服人员、审计和权限边界更适合网站客服场景，长期运维风险也更低。它仍然需要在 Chatwoot 与企业微信之间部署桥接层，因为 [Chatwoot 当前支持的渠道列表](https://developers.chatwoot.com/self-hosted/supported-features)没有微信渠道。

本阶段不替用户选择或启用通道，采用以下可逆方案：

- 保留现有网站 Chatwoot WebWidget，不改变订单落库和订单页面交互。
- 新增独立 `@freedompost/weixin-protocol` 包，只负责腾讯 iLink 协议的安全请求、长轮询、文本发送和响应校验。
- Chatwoot Webhook 验签逻辑单独实现，后续桥接服务只接收已经验签的 `message_created` 事件。
- 生产开关保持关闭；未完成渠道凭据、二维码授权、Chatwoot API Inbox 和回滚演练前，不启动个人微信或企业微信出站。

## 2. 范围与非目标

### 本阶段范围

1. 提供严格类型的 iLink HTTP 客户端：`getupdates`、`sendmessage`、`getconfig`、`notifystart`、`notifystop`。
2. 每次请求使用 HTTPS、Bearer token、`AuthorizationType=ilink_bot_token`、`X-WECHAT-UIN`、`iLink-App-Id=bot` 和客户端版本头。
3. 限制上游响应体大小、请求超时、文本长度，并把 HTTP、网络、超时和协议错误分类；错误消息不包含 token 或上游正文。
4. 提供 Chatwoot Webhook HMAC-SHA256 验签和时间窗校验。
5. 用单元测试覆盖请求头/载荷、业务错误、超时、非法端点和 Webhook 重放/篡改。

### 明确不做

- 不把完整 OpenClaw Gateway 运行时放进订单 API。
- 不控制个人微信桌面客户端，不绕过微信风控，也不模拟剪贴板或窗口点击。
- 不在本阶段写入生产环境变量、创建个人微信会话或改变现有 Chatwoot Website Inbox。
- 不迁移订单表、库存、价格、支付状态或现有客服数据。
- 不在没有人工选择主通道前同时启用两套出站通道。

## 3. 目标架构

```text
网站访客
   │ Chatwoot WebWidget
   ▼
Chatwoot Website Inbox（现有）
   │（订单消息仍走现有 WebWidget 逻辑）
   │
   └── Chatwoot API Inbox（后续新增，专用于桥接）
           │ message_created Webhook + HMAC
           ▼
      Channel Bridge（独立进程/容器）
        ├── WeCom adapter（正式通道，推荐）
        └── Weixin iLink adapter（个人微信，实验开关）
```

如果未来要让企业微信用户直接成为 Chatwoot 外部访客，Chatwoot 的 API Inbox 流程需要先创建 contact，再用 `source_id` 创建 conversation，之后通过消息 API 发送消息；消息与附件接口分别见 [创建会话](https://developers.chatwoot.com/api-reference/conversations/create-new-conversation) 和 [创建消息](https://developers.chatwoot.com/api-reference/messages/create-new-message)。本阶段沿用网站现有 Website Inbox，桥接只处理已由 WebWidget 创建的会话；任何浏览器提交的 account、inbox、contact 或 conversation ID 都不能被当成可信权限数据。

## 4. 消息与身份映射

Redis 保存短期映射：

```text
channel:{provider}:{external_user_id} -> {
  chatwoot_contact_id,
  chatwoot_source_id,
  chatwoot_conversation_id,
  weixin_context_token,
  updated_at
}
```

映射只允许由桥接服务在服务端创建或更新，设置 TTL 并记录版本。订单号使用 `order:{order_id}:chatwoot-message` 作为幂等键，重试不能造成重复订单消息。个人微信的 `context_token` 必须随会话更新并加密存储；日志中只记录内部 ID 和状态，不记录微信用户标识、token 或完整消息正文。

## 5. Chatwoot Webhook 安全

Chatwoot Webhook 事件应至少订阅 `message_created` 和 `conversation_created`。依据 [Chatwoot Webhook API](https://developers.chatwoot.com/api-reference/webhooks/add-a-webhook)，桥接服务按以下规则处理：

1. 使用原始请求体，不使用重新序列化后的 JSON。
2. 计算 `sha256=` + `HMAC-SHA256(timestamp + "." + raw_body, webhook_secret)`。
3. 使用常量时间比较签名，并拒绝超过 5 分钟的时间戳。
4. 验签前不解析或执行事件内容；验签后再验证事件类型、会话归属和消息方向。
5. `outgoing` 消息不得再次转发到 Chatwoot，避免回环；重复 delivery 通过 `X-Chatwoot-Delivery` 或事件 ID 幂等。

## 6. 个人微信适配器边界

腾讯协议使用 HTTPS JSON。请求应携带 `Authorization: Bearer <bot_token>` 和随机 `X-WECHAT-UIN`；`getupdates` 是长轮询，`sendmessage` 文本消息使用 `message_type=2`、`message_state=2` 和单个 text item。图片、文件、语音和视频必须先通过上传接口取得 CDN 引用，再发送媒体消息；媒体加密采用协议规定的 AES-128-ECB，不能复用网站上传的 R2 URL。

个人微信适配器必须具备：

- 版本固定和功能开关，默认 `off`；
- 单一长轮询消费者，断线退避，保存 `get_updates_buf`；
- 每条入站消息的去重键和出站 `client_id`；
- 上游 `ret`/`errcode`、HTTP 状态和超时的可观测指标；
- 图片/文件大小与 MIME 限制，失败时不伪造“已发送”；
- 人工停止时调用 `notifystop`，部署时优雅取消长轮询。

由于上游目前存在公开的“返回成功但未展示”和消息丢失问题（见 [openclaw-weixin issues](https://github.com/Tencent/openclaw-weixin/issues)），在没有真实微信账号和可观测验收前，不能把发送结果当作用户已读或已送达。

## 7. 企业微信适配器边界

企业微信采用官方客服/应用凭据，桥接服务只保留服务端密钥，使用 WebSocket 或 Webhook 接收事件，并通过官方接口出站。现有 [WeCom OpenClaw 插件](https://github.com/WecomTeam/wecom-openclaw-plugin)可作为协议参考，但不把 OpenClaw 运行时作为 FreedomPost 的生产依赖。生产上线前必须由管理员创建客服应用、确认客服账号、回调地址、事件范围和 IP/域名白名单。

## 8. 配置与上线门禁

后续桥接服务配置建议：

```text
CHATWOOT_BASE_URL=https://support-freedompost.openal.uk
CHATWOOT_API_TOKEN=<server secret>
CHATWOOT_ACCOUNT_ID=5
# 当前桥接沿用现有 Website Inbox；外部访客模式才需要单独的 API Inbox
CHATWOOT_WEBHOOK_SECRET=<random secret>
CHANNEL_BRIDGE_ENABLED=false
CHANNEL_BRIDGE_PROVIDER=wecom
WECOM_BASE_URL=https://qyapi.weixin.qq.com
WECOM_CORP_ID=<server secret>
WECOM_CORP_SECRET=<server secret>
WECOM_AGENT_ID=<agent id>
WECOM_CALLBACK_TOKEN=<server secret>
WECOM_ENCODING_AES_KEY=<43-character key>
WECOM_OPERATOR_USER_ID=<enterprise member id>
WEIXIN_BASE_URL=https://ilinkai.weixin.qq.com
WEIXIN_BOT_TOKEN=<QR login secret, when provider=weixin>
```

以下条件全部满足后才允许启用生产出站：

- 主通道由产品负责人明确选择；
- 现有 Website Inbox 的 Webhook 验签和桥接在测试环境通过；若启用外部访客模式，再完成 API Inbox、contact/conversation 创建；
- 供应商凭据通过密钥管理注入，源码、前端 bundle、日志和数据库均无明文；
- 文本、图片、文件、超时、重复、重启和上游 4xx/5xx 均有集成测试；
- 有可执行的 `CHANNEL_BRIDGE_ENABLED=false` 回滚和旧 Chatwoot WebWidget 烟囱测试；
- 个人微信额外完成扫码授权、连续消息收发和“服务端返回成功但微信未展示”的告警验证。

## 9. 当前实现与下一步

当前代码已经包含三层可测试基础：个人微信 iLink 协议包、企业微信 Agent API 客户端，以及 Chatwoot↔企业微信的文本桥接核心。`services/api` 现在提供可选回调路由：

- `POST /api/integrations/chatwoot/webhook`：先验证原始请求体的 `sha256=` HMAC，再转发访客文本；
- `GET|POST /api/integrations/wecom/callback`：完成企业微信回调 URL 验证、AES-256-CBC 解密和客服回复路由。

桥接由 `CHANNEL_BRIDGE_ENABLED` 控制，默认 `false`；关闭时回调返回明确的 disabled 响应，现有 Website WebWidget 行为不变。Redis 仅保存投递去重键和短期会话映射，不保存消息正文或凭据。为防止客服回复串到其他会话，企业微信回复必须带有服务端下发的 `FP-CW:<conversation_id>` 前缀。

推荐的生产顺序是：先在 Chatwoot 为现有 Website Inbox 创建 `message_created` Webhook（未来扩展外部访客时再创建专用 API Inbox），再在企业微信创建 Agent、配置回调白名单并取得 CorpID、CorpSecret、AgentID、Token、EncodingAESKey 和客服成员 ID；在测试环境完成文本、重复投递、超时、重启和错误重试验收后，才将 `CHANNEL_BRIDGE_ENABLED` 切换为 `true`。个人微信适配器继续保持关闭状态，媒体转发和个人微信真实账号验收属于后续阶段。
