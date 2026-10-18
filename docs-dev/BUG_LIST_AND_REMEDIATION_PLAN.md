# FreedomPost 全栈审计 Bug 清单与分批修复方案 (Bug List & Remediation Plan)

> **审计范围**：Go 后端服务 (`services/api`、`services/paid-access`)、前端应用 (`apps/admin`、`apps/public-reader`)、共享包 (`packages/*`)、数据库迁移 (`deploy/migrations/*`)、反向代理与 CI/CD (`deploy/*`、`.github/workflows/*`)  
> **执行原则**：先记录、定方案、分批修复、每批通过自动化测试验证、最后执行全量 Code Review，确保零遗漏、零回归。

---

## 批次总览与进度追踪 (Batch Tracker)

| 批次 | 领域 | 包含 Bug 编号 | 状态 | 验证方式 |
| :--- | :--- | :--- | :--- | :--- |
| **Batch 1** | 安全控制、鉴权、限流与密码学修复 (Backend & Shared Security) | `BUG-SEC-01` ~ `BUG-SEC-06` | `[x] 已修复并验证` | `go test ./...` (`services/api`, `services/paid-access`) + `npm test` (`packages/*`) |
| **Batch 2** | 后端 API 契约、并发安全、存储与数据库仓储修复 (Backend API & DB) | `BUG-API-01` ~ `BUG-DB-03` | `[x] 已修复并验证` | `go test ./...` (`services/api`, `services/paid-access`) |
| **Batch 3** | 管理后台与公共读者端前端修复 (`apps/admin` & `apps/public-reader`) | `BUG-FE-01` ~ `BUG-FE-05` | `[x] 已修复并验证` | `npm test` + `npm run build` |
| **Batch 4** | 基础设施、反代 CSP、部署脚本、CI/CD 与 Schema 同步 (`deploy` & `packages/db`) | `BUG-INFRA-01` ~ `BUG-INFRA-02` | `[x] 已修复并验证` | `node --test deploy/*.test.mjs` + 全仓构建与单测 |

---

## Batch 1：安全控制、鉴权、限流与密码学修复

### `[x]` BUG-SEC-01：`POST /api/orders` 传统路径绕过订单归属、CSRF 与 Origin 校验，且 `orderOriginAllowed` 误判合法跨端口/跨子域源
- **严重程度**：Critical (P0)
- **位置**：
  - `services/api/internal/httpapi/affiliates.go:244-307` (`createOrder`)
  - `services/api/internal/httpapi/order_handoff.go:83-147` (`orderOriginAllowed`, `createOwnedOrder`)
- **根因分析**：
  1. `createOrder` 仅在请求头包含 `Idempotency-Key` 或 `X-Order-CSRF` 时才进入受保护的 `s.createOwnedOrder(w, r)`。只要攻击者不传这两个 Header，请求就会直接落入下方 legacy 分支（`affiliates.go:249-307`），不校验 `Origin`、不校验 CSRF、不绑定 `fp_order_owner` 会话，却依然创建订单并消费 `cw_conversation` Cookie 向 Chatwoot 客服会话发送订单消息。
  2. `orderOriginAllowed` (`order_handoff.go:88`) 同时要求 `u.Host == r.Host && s.cfg.AllowedOrigins()[origin]`，当公网读者端与 API 域名/端口不同（但已在 `AllowedOrigins()` 白名单内）时会被错误拒绝。
- **修复方案**：
  1. 在 `createOrder` 入口统一拦截 `Sec-Fetch-Site: cross-site` 请求；当请求携带 `Origin` 头时，必须通过 `s.orderOriginAllowed(r)` 校验（允许无 `Origin` 的同源/服务端测试调用以保持向后兼容，但浏览器跨站请求必带 `Origin` 或 `Sec-Fetch-Site: cross-site`）。
  2. 修复 `orderOriginAllowed`：先校验 `Sec-Fetch-Site` 不为 `cross-site`；若 `Origin` 精确命中 `s.cfg.AllowedOrigins()[origin]` 或与当前 `r.Host` 同源且在白名单内，则放行。

---

### `[x]` BUG-SEC-02：企业微信–Chatwoot 客服桥接 (`channelbridge`) 多行回复正则失效导致跨客户消息串线，以及超长消息触发 503 死循环
- **严重程度**：Critical (P0)
- **位置**：
  - `services/api/internal/channelbridge/bridge.go:26, 149-155, 198-216`
  - `services/api/internal/httpapi/channelbridge.go:162-167`
- **根因分析**：
  1. `conversationReference = regexp.MustCompile(`^\s*FP-CW:(\d+)\s+(.+?)\s*$`)` 未启用 `(?s)`（dot-all 模式），`.` 不匹配换行符 `\n`。当客服在企业微信发送带 `FP-CW:101` 的多行回复时，`FindStringSubmatch` 返回 `nil`。
  2. 在 `allowUnprefixedReplies == true`（默认开启）时，解析失败会落入 `else if s.allowUnprefixedReplies` 分支，将原本发给会话 `101` 的回复错误投递给最近活跃的另一个客户会话（如 `202`），造成严重的跨客户隐私泄露；在 `allowUnprefixedReplies == false` 时，消息被静默丢弃且返回 HTTP 200。
  3. `HandleChatwootWebhook` (`bridge.go:149-155`) 未对发往企业微信的文本做 2048 字节上限截断，访客发送长消息时 `wecom.Client.SendText` 报错，触发 Chatwoot 503 无限重试。
- **修复方案**：
  1. 将 `conversationReference` 正则改为 `(?s)^\s*FP-CW:(\d+)\s+(.+?)\s*$`。
  2. 若消息以 `FP-CW:` 开头但格式不合法（例如只有 `FP-CW:101` 没有正文），直接报错拒绝，**绝不**回退到 `allowUnprefixedReplies` 的最近会话。
  3. 在 `HandleChatwootWebhook` 构造企业微信消息时，按 UTF-8 字符边界安全截断，确保最终字节数 `<= 2048`。

---

### `[x]` BUG-SEC-03：Go Markdown 渲染器允许任意外部 `<iframe>`、Go/TS 清洗器丢失下划线 `<u>`，以及 `packages/security` 存在 `data:` URI 与高危附件类型放行
- **严重程度**：High (P0)
- **位置**：
  - `services/api/internal/renderer/renderer.go:107, 189-209, 353, 388-389, 439-448`
  - `packages/security/src/index.ts:13-40, 155-238`
  - `packages/renderer/src/index.ts:45, 167-185`
- **根因分析**：
  1. `services/api/internal/renderer/renderer.go:107` 启用了 `ghtml.WithUnsafe()`，而 `buildPolicy()` (`lines 388-389`) 对 `iframe` 的 `src` 未限制域名，允许嵌入任意 `https://evil.example` 页面。
  2. 管理后台编辑器工具栏支持下划线 `<u>`（产生 `<u>text</u>`），但 Go `buildPolicy()` 和 TS `sanitizeArticleHtml` 均未将 `"u"` 列入允许标签，导致保存或渲染后下划线全部丢失。
  3. `packages/security/src/index.ts:213` 在全局 `allowedSchemes` 中包含 `"data"`（未限制在 `img`），允许 `<a href="data:text/html;base64,...">`；同时 `allowedUploadExtensions` 与 `allowedAttachmentMimeTypes` 包含 `.html`、`.svg`、`.js`、`text/html`、`image/svg+xml`、`application/javascript`。
  4. `packages/security/src/index.ts:155-212` (`sanitizeArticleHtml`) 未放行受控 YouTube `iframe` 与 `a[download]`，二次清洗已渲染 HTML 时会破坏视频嵌入与附件卡片。
  5. `headingIDTransformer` (`renderer.go:189-209`) 仅遍历顶层 `*ast.Text` 节点，忽略 `**加粗**` 或 `` `代码` `` 内的文本，导致生成的标题锚点 ID 与目录（TOC）不匹配；`renderAttachmentCards` 在 Go 与 TS 中对文件名进行了二次 HTML 转义。
- **修复方案**：
  1. 在 `services/api/internal/renderer/renderer.go:389` 对 `iframe` 的 `src` 增加正则白名单 `^https://www\.youtube-nocookie\.com/embed/[A-Za-z0-9_-]{11}(?:\?start=\d+)?$`。
  2. 在 Go `buildPolicy()` 和 TS `sanitizeArticleHtml` 中均允许 `"u"` 标签。
  3. 在 `packages/security/src/index.ts` 中将 `"data"` 移出全局 `allowedSchemes`（仅在 `allowedSchemesByTag.img` 保留受控图片或移除全局 `data`），从上传白名单中移除 `.html`/`.htm`/`.svg`/`.js`/`.mjs`/`.cjs`/`.ts`/`.tsx`/`.jsx`/`.sh` 及对应可执行/脚本 MIME，并在 `sanitizeArticleHtml` 中对齐受控 YouTube `iframe` 与 `a[download]` 规则。
  4. 修复 `headingIDTransformer` 递归提取所有子节点文本；修复 Go 与 TS `renderAttachmentCards` 避免对已转义属性重复转义。

---

### `[x]` BUG-SEC-04：评论限流可被客户端伪造字段绕过、内存限流器跨窗口误驱逐，以及 IPv6 / 代理 IP 提取与分销端 CSRF 防护缺陷
- **严重程度**：High (P0)
- **位置**：
  - `services/api/internal/httpapi/comments.go:96-104`
  - `services/api/internal/ratelimit/limiter.go:71-127`
  - `services/paid-access/internal/ratelimit/limiter.go:20-74`
  - `services/api/internal/httpapi/server.go:229-241`
  - `services/api/internal/httpapi/middleware.go:74, 97-106, 210-223`
- **根因分析**：
  1. `comments.go:96` 将客户端可控的 `fingerprint` 和 `localId` 与 IP 拼在一起做 SHA256 作为限流 Key。攻击者每次请求随机换一个 `localId` 即可彻底绕过每篇 5 条/天、3 条/5 分钟的限制。
  2. `services/api` 和 `services/paid-access` 的内存限流器 `Memory.Allow` 在清理满额桶时使用 `now.Sub(v.started) >= window`（即当前调用者的 `window` 参数！）。当一个 1 分钟窗口的接口触发清理时，会把尚未过期的 15 分钟登录封禁桶、24 小时评论限流桶、10 分钟内部请求防重放 Nonce 桶全部清空。
  3. `server.go:236` 用 `strings.LastIndex(addr, ":")` 剥离端口，导致无端口 IPv6 地址被截断、带端口 `[::1]:8080` 保留方括号 `[::1]`，且未用 `net.ParseIP` 校验 `X-Forwarded-For`；`middleware.go:97` (`realIP`) 完全忽略 `TrustProxy` 配置。
  4. `middleware.go:210-223` 仅保护 `/api/admin` 写接口，未对 `/api/affiliate/*` 写接口校验非白名单 `Origin` 或 `Sec-Fetch-Site: cross-site`。
- **修复方案**：
  1. 在 `comments.go` 中除了校验 `subjectKey` 外，同时对纯服务端派生的 `ipKey := security.HashSubject(slug, s.clientIP(r))` 施加限流，防止通过伪造 `localId`/`fingerprint` 绕过。
  2. 在两个 `ratelimit/limiter.go` 的 `bucket` 结构体中记录 `expiresAt time.Time`（`now.Add(window)`），判断过期与清理时统一使用 `!now.Before(v.expiresAt)`。
  3. 在 `server.go:229-241` 与 `middleware.go:97` 中使用 `net.SplitHostPort` 与 `net.ParseIP` 规范化提取 IP，并将 `TrustProxy` 传入 `requestLogger`。
  4. 扩展 `adminOriginProtection`（或在中间件中）覆盖 `/api/affiliate/` 下的写操作请求。

---

### `[x]` BUG-SEC-05：`mediaimport` 重定向丢失 HTTPS 升级、HTTP Transport 连接池泄漏与 UTF-8 文件名截断乱码
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/mediaimport/importer.go:93-98, 142-205, 270-272`
- **根因分析**：
  1. `CheckRedirect` (`line 179`) 调用 `_, err := parsePublicHTTPSURL(...)` 丢弃了返回的新 URL（其中 `http` 已被升级为 `https`），导致 `request.URL.Scheme` 仍为 `"http"`，在跟随 `http://` 重定向时连接 80 端口失败或向 443 端口发送明文 HTTP。
  2. `newSafeHTTPClient` 每次下载图片都新建 `*http.Transport` 且未设置 `DisableKeepAlives: true` 或关闭空闲连接。
  3. `Filename` (`line 270`) 使用字节切片 `name[:128]` 截断中文文件名，可能截断在多字节 UTF-8 字符中间产生非法字节序列。
- **修复方案**：
  1. 在 `CheckRedirect` 中接收 `parsed, err := parsePublicHTTPSURL(...)` 并赋值给 `*request.URL = *parsed`。
  2. 在 `newSafeHTTPClient` 的 `http.Transport` 中启用 `DisableKeepAlives: true`。
  3. 使用 `[]rune(name)` 按字符数安全截断文件名。

---

### `[x]` BUG-SEC-06：HMAC 时间戳减法整数溢出绕过与 Benefit 密文 Cookie 校验不严
- **严重程度**：Medium (P1)
- **位置**：
  - `services/paid-access/internal/httpapi/handler.go:569-572`
  - `services/api/internal/security/security.go:96-103`
  - `services/api/internal/channelbridge/chatwoot.go:196-205`
  - `services/api/internal/benefit/util.go:42-47`
  - `services/api/internal/benefit/cipher.go:163-204`
- **根因分析**：
  1. 传入极小负数时间戳（如 `math.MinInt64 + now`）时，`now - ts` 发生 `int64` 有符号溢出变为 `math.MinInt64`，在 `paid-access` 中 `abs(math.MinInt64)` 仍为负数（`< 300`），绕过有效期检查。
  2. `benefit/util.go:42` (`hmacEqual`) 忽略了 `hex.DecodeString` 的 `err`，允许签名末尾追加任意非十六进制字符而不报错。
  3. `benefit/cipher.go:163-204` 解密 Cookie 时未校验 `p.K`（64 位十六进制指纹）格式与长度。
- **修复方案**：
  1. 在三处时间戳校验中先判断 `timestamp <= 0` 直接拒绝，再计算时间差。
  2. 在 `benefit/util.go:42` 检查 `if err != nil { return false }`，并在 `benefit/cipher.go` 中校验 `len(p.K) == 64` 且为合法 hex。

---

## Batch 2：后端 API 契约、并发安全、存储与数据库仓储修复

### `[x]` BUG-API-01：管理后台“重置读者密码”与“重置推广员查询密码”前后端契约断裂，以及 `paidaccess.Client` 吞掉下游 4xx 状态码
- **严重程度**：High (P0)
- **位置**：
  - `services/api/internal/httpapi/admin.go:646-680, 798-822`
  - `services/api/internal/paidaccess/client.go:43-47, 129-131`
  - `services/paid-access/internal/httpapi/handler.go:447-471`
  - `apps/admin/src/main.tsx:1967-1973, 2037-2043`
- **根因分析**：
  1. `apps/admin` 点击“重置读者密码”时发送无请求体的 `POST /api/admin/reader-accounts/{id}/reset-password` 并期望返回 `{ temporaryPassword }`。但 `admin.go:806` 强制 `decodeJSON` 解析 `{"password": "..."}`，空请求体直接返回 `400 INVALID_REQUEST`；即使传入密码，`admin.go` 用 bcrypt 哈希后发给 `paid-access`，而 `paid-access` 完全忽略请求体并自行生成 Argon2id 随机密码返回 `{ temporaryPassword }`。
  2. `apps/admin` 点击“重置推广员查询密码”时同样发送空请求体的 `POST /api/admin/affiliates/{id}/reset-password` 并期望返回 `{ queryPassword }`。但 `admin.go:653` 强制要求请求体提供 `password` 且仅返回 `{"ok": true}`。此外 `validAffiliatePassword` 对密码 `TrimSpace` 后检查长度，但 `bcryptHashReal` 却对未 `TrimSpace` 的原字符串做哈希，若包含前后空格长度超 72 字节则报错 `500`。
  3. `paidaccess.Client.do` (`client.go:129-131`) 将下游 `paid-access` 返回的 400/404/409 等业务错误全部转为普通 Go `error`，导致 `admin.go` 统一返回 `500 INTERNAL_ERROR`。
- **修复方案**：
  1. 修改 `adminResetReaderPassword`：移除冗余的请求体解析与 bcrypt 哈希，直接调用 `s.paidAccess.ResetReaderPassword(r.Context(), id)` 并透传返回 `{ temporaryPassword }`。
  2. 修改 `adminResetAffiliatePassword`：允许空请求体或未提供 `password` 字段；当未提供 `password` 时，调用 `generateAffiliatePassword()` 自动生成密码，完成哈希更新后返回 `{"ok": true, "queryPassword": password}`；当请求体显式提供了 `password` 时，对 `strings.TrimSpace(input.Password)` 校验并哈希，返回 `{"ok": true, "queryPassword": password}`，兼顾管理后台交互与现有单测。
  3. 在 `paidaccess.Client` 中定义 `UpstreamError{StatusCode, Code, Message}`，在 `admin.go` 中将下游 4xx 状态码与错误码原样透传给前端。

---

### `[x]` BUG-API-02：未配置 `Turnstile` 或 `Opus8` 时调用 `/api/benefit/claim` 触发空指针 Panic，且 `FailBenefitClaim` 存在状态覆盖竞态
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/httpapi/benefit.go:46, 98, 161, 227`
  - `services/api/internal/repository/postgres.go:1417-1426`
- **根因分析**：
  1. 当环境变量未配置 `TURNSTILE_SECRET_KEY` 或 `OPUS8_INTEGRATION_BASE_URL` 时，`b.Turnstile` 或 `b.Opus8` 为 `nil`。此时若活动处于开启状态，请求 `/api/benefit/claim` 会在 `benefit.go:161` (`b.Turnstile.Verify`) 或 `benefit.go:227` (`b.Opus8.RegisterOrRecoverBenefit`) 触发 nil pointer panic，且数据库中的领取记录会永久卡在 `'provisioning'` 状态。
  2. `FailBenefitClaim` (`postgres.go:1418`) 执行 `UPDATE benefit_claims SET status = 'failed' WHERE id = $1`，未加 `AND status = 'provisioning'` 条件，若慢请求超时回滚时另一个恢复请求已将该记录置为 `'ready'`，会把已成功的记录覆盖为 `'failed'`。
- **修复方案**：
  1. 在 `benefitInfo` 中仅当 `b.Turnstile != nil && b.Opus8 != nil` 时才将 `enabled` 置为 `true`；在 `benefitClaim` 入口检查 `b.Turnstile == nil || b.Opus8 == nil`，若为空则返回 `503 BENEFIT_UNAVAILABLE`。
  2. 在 `FailBenefitClaim` 的 SQL `WHERE` 子句中增加 `AND status = 'provisioning'`。

---

### `[x]` BUG-DB-01：商品库存 (`stock`)、销量 (`sold_count`) 与外链 (`link_url`) 在 PostgreSQL 仓储层未生效
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/repository/postgres.go:674-748, 1103-1220`
  - `deploy/migrations/`
- **根因分析**：
  1. `createAffiliateOrderTx` (`postgres.go:1126`) 使用 `FOR SHARE OF a, p` 查询商品，从未扣减有限库存（`stock > 0`），导致库存为 1 的商品可被无限并发下单；订单完成时也从未累加 `products.sold_count`。
  2. `postgres.go:674-748` 中商品 CRUD 硬编码了 `'' AS link_url`，忽略了 `Product.LinkURL` 的持久化与读取。
- **修复方案**：
  1. 在 `createAffiliateOrderTx` 中使用 `FOR UPDATE OF p` 锁定商品行；当 `product.Stock > 0` 时执行 `UPDATE products SET stock = stock - 1, updated_at = NOW() WHERE id = $1 AND stock > 0`。
  2. 在 `UpdateAffiliateOrderStatus` 中放到事务内执行：查询旧状态与 `product_id`，当状态从非 `'completed'` 变为 `'completed'` 时将 `products.sold_count` 加 1；在 `packages/db/src/schema.ts` 与迁移脚本中补齐 `link_url` 字段支持（并在 `postgres.go` 中通过 `COALESCE` 兼容读取与写入）。

---

### `[x]` BUG-DB-02：`post_slug_aliases` 历史别名路由失效、生成 Slug 缺少 `p_` 前缀、阅读量步进/初值不一致、评论并发路径冲突与 `0008` 迁移事务异常
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/repository/postgres.go:23, 149-214, 289-320, 394-556, 1449-1464`
  - `services/api/internal/security/security.go:33-39`
  - `deploy/migrations/0008_ascii_post_slugs.sql:10-18, 68`
  - `services/api/cmd/server/main.go:148-174, 218-219, 378-384`
- **根因分析**：
  1. `0008_ascii_post_slugs.sql` 在第 18 行 `BEGIN;` 之前已有 `INSERT`/`UPDATE` 语句，且第 68 行包含显式 `COMMIT;`，破坏外层 `migrate.Run` 的事务包裹。
  2. `GetPostBySlug` 未关联查询 `post_slug_aliases`；`UpdatePost` 修改文章 slug 时未将旧 slug 写入 `post_slug_aliases`；`security.GenerateSlug()` 生成 `p1a2b3c4d5` 而非 `0008` 规定的 `p_1a2b3c4d5` 格式。
  3. `CreatePost` 将 `view_count` 初始化为 `0`（应与 `0011_post_view_count_step.sql` 的默认值 `100` 一致），且 `postViewIncrement` 为 `5`（应为 `100`）；`RecordView` 的去重插入与计数增加未放在同一事务或原子 CTE 中。
  4. `CreateComment` 在开启事务 `Begin(ctx)` 之前查询现有评论计算 `path`，并发评论同一篇文章或同一父评论时会生成重复的 `path`（如两个 `0001.0001`）。
  5. `main.go` 创建了 3 个独立的 Redis 连接池，且后台启动了无人写入的 `viewBuf.StartFlusher`。
- **修复方案**：
  1. 修复 `0008_ascii_post_slugs.sql`，使 `BEGIN;` 位于文件开头。
  2. 在 `GetPostBySlug` 中支持通过 `post_slug_aliases` 回退查找文章；在 `UpdatePost` 中当旧 slug 与新 slug 不同时将旧 slug 归档至 `post_slug_aliases`；更新 `security.GenerateSlug()` 返回 `p_` + 10 位小写字母数字。
  3. 将 `CreatePost` 的初始 `view_count` 设为 `100`、`postViewIncrement` 设为 `100`，并将 `RecordView` 改为单条原子 CTE SQL。
  4. 在 `CreateComment` 中先开启事务并对所属文章行 `SELECT id FROM posts WHERE slug = $1 FOR UPDATE` 加锁，再在事务内查询同级评论计算 `path`。
  5. 在 `main.go` 中复用同一个 `*redis.Client` 实例并移除未使用的 `viewBuf.StartFlusher`。

---

### `[x]` BUG-DB-03：`LocalAdapter.Delete` 根目录误删风险、OSS/R2 公共 URL 校验缺失、订单客服投递回执丢失及 UUID/外键错误处理
- **严重程度**：Medium (P1)
- **位置**：
  - `services/api/internal/storage/storage.go:174-184`
  - `services/api/internal/httpapi/post_image_policy.go:30`
  - `services/api/internal/config/config.go:293-318`
  - `services/api/internal/repository/order_handoff.go:135-148`
  - `services/api/internal/httpapi/admin.go:158-313, 550-600`
  - `services/api/internal/httpapi/affiliates.go:68`
- **根因分析**：
  1. `LocalAdapter.Delete` (`storage.go:178`) 判断 `clean != a.root && !strings.HasPrefix(...)`，当传入空字符串 `""` 或 `"."` 时，`clean == a.root` 通过校验并调用 `os.Remove(a.root)`。
  2. 当 `STORAGE_DRIVER=oss` 且未配置 `ALIYUN_OSS_PUBLIC_BASE_URL` 时，上传图片返回默认 `https://<bucket>.oss-<region>.aliyuncs.com/...`，保存文章时被 `unmanagedArticleImageHosts` 当作未导入外链拦截（报 `400 EXTERNAL_IMAGE_NOT_IMPORTED`）；当 `STORAGE_DRIVER=r2` 且未配置 `R2_PUBLIC_BASE_URL` 时生成非法相对路径。
  3. `FinishOrderSupportDelivery` (`order_handoff.go:142`) 强校验 `attempts = $3`，若发送成功前租约过期被并发 worker 增加 `attempts`，会导致 `sent` 状态与 `support_message_id` 无法写入，进而引起重复发信。
  4. `affiliates.go:68` 在并发注册相同邮箱+名称命中唯一索引冲突时直接返回 `500`；`admin.go` 多处对非法 UUID 或外键/唯一约束冲突直接返回 `500`。
- **修复方案**：
  1. 在 `LocalAdapter.Delete` 中当 `clean == a.root || !strings.HasPrefix(clean, a.root+string(os.PathSeparator))` 时直接返回错误。
  2. 在 `post_image_policy.go` 中将 OSS 默认域名 `https://<bucket>.oss-<region>.aliyuncs.com` 纳入受管主机白名单；在 `config.go` 中对 `r2` 驱动强制要求 `R2_PUBLIC_BASE_URL`。
  3. 在 `FinishOrderSupportDelivery` 中当 `status == "sent"` 时允许更新尚未标记为 `'sent'` 的订单（不受 `attempts` 递增阻断）。
  4. 在 `affiliates.go` 与 `admin.go` 中识别 PostgreSQL 错误码 `23505`/`23503`/`23514`/`22P02` 并转化为正确的幂等查询或 `400`/`409` 响应。

---

## Batch 3：管理后台与公共读者端前端修复 (`apps/admin` & `apps/public-reader`)

### `[x]` BUG-FE-01：`ReaderShell.astro` 内联 JSON `</script>` 注入 XSS 与前端 DOM 渲染未校验 URL 协议 (`javascript:`)
- **严重程度**：Critical (P0)
- **位置**：
  - `apps/public-reader/src/layouts/ReaderShell.astro:29-41, 193`
  - `apps/admin/src/main.tsx:2283-2297`
  - `apps/admin/src/editor-media.ts:19-23`
  - `apps/public-reader/src/scripts/portal.ts:604-612`
  - `apps/public-reader/src/scripts/reader.ts:991-1010`
- **根因分析**：
  1. `ReaderShell.astro:193` 使用 `<script id="initial-reader-data" type="application/json" set:html={initialPayload}></script>`，而 `initialPayload = JSON.stringify(...)` 未转义 `<`。任何评论、文章或商品中包含 `</script><script>...` 都会直接闭合标签执行任意 JS。
  2. `apps/admin/src/main.tsx:2291` (`markdownFragmentToEditorHtml`)、`editor-media.ts:19` (`editorImageHtml`)、`portal.ts:609` (`renderToolCard`)、`reader.ts:991` (`renderCommentAttachment`) 直接将 URL 拼入 `href`/`src` 而不校验协议，存在 `javascript:` XSS 风险。
- **修复方案**：
  1. 在 `ReaderShell.astro` 中对 `JSON.stringify` 结果执行 `.replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/&/g, "\\u0026")`。
  2. 在上述 4 处前端渲染函数中增加 `safeUrl` 校验（仅允许 `http://`、`https://` 或单斜杠相对路径 `/...`）。

---

### `[x]` BUG-FE-02：管理后台异步粘贴图片、保存文章与上传封面时的闭包陈旧状态 (Stale Closure) 导致跨文章/跨商品数据错乱
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:375-465, 507-547, 1492-1500, 1708-1719, 1828-1831, 1863-1874`
- **根因分析**：
  1. `pasteHtmlWithImages` 与 `savePost` 包含耗时网络请求（导入外链图片），期间若用户切换了当前选中文章，`await` 恢复后仍使用旧闭包中的 `activePost` 或向已切换的 `editorRef.current` 注入内容，造成文章内容串台覆盖。
  2. `ProductWorkspace.uploadCover` 与 `ToolWorkspace.uploadCover` 在 `await uploadFile(file)` 后使用旧闭包的 `selected` 对象更新状态，覆盖上传期间用户修改的标题/价格等字段。
- **修复方案**：
  1. 使用 `activePostIdRef` 实时跟踪当前激活文章 ID；`importRemoteImages` 显式接收 `targetPostId`；在 `pasteHtmlWithImages` 和 `savePost` 的每个 `await` 后检查 `activePostIdRef.current === targetPostId`，若已切换文章则中止 DOM 注入或编辑器同步。
  2. 在商品和工具的 `uploadCover` 中先捕获 `targetId = selected.id`，上传完成后通过函数式更新 `items => items.map(item => item.id === targetId ? { ...item, coverUrl: uploaded.url } : item)` 合并状态。

---

### `[x]` BUG-FE-03：管理后台富文本编辑器 Markdown 序列化与反序列化损坏代码段、嵌套格式与图片 URL
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:776-835, 2283-2289, 2429-2451, 2523-2539, 2567-2589`
- **根因分析**：
  1. `formatInlineMarkdown` (`lines 2523-2539`) 先生成 `<code>...</code>` 再全局执行 `**`、`~~`、`*` 正则替换，导致行内代码中的 `*`（如 `` `a * b * c` ``）被破坏成 `<code>a <em> b </em> c</code>`。
  2. `nodeToMarkdown` (`lines 2429-2451`) 在顶层遇到 `STRONG`/`EM`/`U`/`CODE`/`A` 等行内节点时直接走 `default` 分支丢弃标签，仅保留纯文本；图片正则 `!\[([^\]]*)\]\((.+)\)` 使用贪婪匹配，同一行有多张图片或括号时会吞掉后续内容。
  3. `insertCallout` (`lines 795-818`) 当光标 `range.startContainer === editor` 时回退到 `editor.lastElementChild`，导致插入的提示框总是跑到文章末尾而非光标所在位置。
- **修复方案**：
  1. 在 `formatInlineMarkdown` 中先提取行内代码和链接为占位符，完成加粗/斜体/删除线替换后再还原占位符。
  2. 在 `nodeToMarkdown` 中将顶层行内标签委托给 `inlineNodeToMarkdown` 处理，并将图片正则改为非贪婪匹配 `!\[([^\]]*)\]\(([^)\s]+)\)`。
  3. 在 `insertCallout` 中当 `node === editor` 时通过 `editor.childNodes[range.startOffset]` 定位光标处的块级节点。

---

### `[x]` BUG-FE-04：管理后台工作区导航标签不一致、登出状态残留与剪贴板异常中断密码展示
- **严重程度**：Medium (P2)
- **位置**：
  - `apps/admin/src/main.tsx:292-300, 1151-1157, 1724-1729, 1879-1884, 1978, 2037-2043, 2061-2088, 2123-2125`
- **根因分析**：
  1. 切换到“商品”或“工具”工作区后，顶部导航栏缺少“付费”标签；“付费”工作区缺少“工具”标签。
  2. `logout()` 未清空 `articleOrders`、`readerAccounts`、`activeId` 并重置 `workspace`。
  3. `resetAffiliatePassword` 中 `await navigator.clipboard.writeText(result.queryPassword)` 未捕获异常，若剪贴板权限受限会直接跳入 `catch`，导致已重置的新密码既未复制也无法在界面上看到；`formatMoney` 未校验货币代码合法性，输入不完整时触发 `RangeError` 白屏。
- **修复方案**：
  1. 统一所有工作区顶部导航栏渲染完整的 5 个标签（`文章`、`商品`、`工具`、`分销`、`付费`）。
  2. 在 `logout()` 中完整清理所有敏感状态。
  3. 用 `try/catch` 包裹 `navigator.clipboard.writeText`，确保即使复制失败也在界面展示新生成的查询密码；在 `formatMoney` 中校验 3 位大写字母货币码并增加 `try/catch` 兜底。

---

### `[x]` BUG-FE-05：公共读者端 `hydrateAffiliatePricing` 监听器指数级泄漏、`openArticle` 异步竞态、搜索框自动切文与 `benefit.ts` 重试卡死
- **严重程度**：High (P1)
- **位置**：
  - `apps/public-reader/src/scripts/portal.ts:286-331, 708-711, 820-824, 851, 867, 890-920, 933-939, 1085-1087`
  - `apps/public-reader/src/scripts/reader.ts:235-242, 373-375, 404-477, 506-511, 866-965, 1037-1103`
  - `apps/public-reader/src/scripts/paid-access.ts:92-97, 222-239`
  - `apps/public-reader/src/scripts/benefit.ts:91-119, 264-276`
- **根因分析**：
  1. `portal.ts:286-331` (`hydrateAffiliatePricing`) 在每次点击“应用到已选商品”后递归调用自身，并在持久 DOM 按钮 `#applyAffiliateDefault` 和 `#applyAffiliateSelected` 上重复调用 `addEventListener`，导致点击 $N$ 次后触发 $2^N$ 次并发请求，且重绘表格时丢失复选框选中状态。
  2. `portal.ts:851` 调用 `new URL(shareUrl)` 未传 `location.origin`，当 `shareUrl` 为相对路径时抛出 `TypeError`；`#affiliateLogout` 未清空已渲染的分销面板 DOM。
  3. `reader.ts:404` (`openArticle`) 无请求序号保护，快速点击两篇文章时慢响应会覆盖新文章；切换文章时未重置 `replyParentId` 与待上传附件；`searchInput` (`line 1054`) 每输入一个字符都调用 `openArticle(..., { push: true, countView: true })` 污染浏览器历史并刷高阅读量；`updateCommentCounts` 在已购付费文章下回退到未解锁缓存，覆盖掉已解锁正文。
  4. `paid-access.ts:92-97` 在付费文章激活时全局拦截 `copy`/`cut` 事件，未放行评论输入框 (`<input>`/`<textarea>`)；`turnstileLoader` 脚本加载失败后未置空导致无法重试。
  5. `benefit.ts:264-276` (`retry()`) 当初始 `start()` 失败（`this.campaign === null`）或轮询网络中断时，点击“重试”不会重新拉取活动状态或恢复轮询。
- **修复方案**：
  1. 将 `#applyAffiliateDefault` 和 `#applyAffiliateSelected` 的事件绑定移至只执行一次的初始化逻辑中，重绘定价表格时保留已勾选的 `productId` 集合。
  2. 修复 `new URL(shareUrl, window.location.origin)`，避免重复调用 `productShareUrl`，并在退出分销登录时清理面板 DOM 与表单。
  3. 在 `reader.ts` 中为 `openArticle` 增加递增请求序号 `openArticleSeq`；切换文章时重置 `replyParentId = ""` 和 `pendingCommentFiles = []`；在 `searchInput` 中将 `push` 和 `countView` 设为 `false`；在 `updateCommentCounts` 中优先保留当前 `activeArticle` 的评论数更新。
  4. 在 `paid-access.ts` 的 `copy`/`cut` 拦截器中放行 `HTMLInputElement`/`HTMLTextAreaElement`，并在 Turnstile `onerror` 时重置 `turnstileLoader = null`。
  5. 在 `benefit.ts` 的 `retry()` 中，若 `!this.campaign` 或存在未完成的领取轮询，则调用 `void this.start()` 重新初始化。

---

## Batch 4：基础设施、反代 CSP、部署脚本、CI/CD 与 Schema 同步

### `[x]` BUG-INFRA-01：Caddy 与 Nginx 的 CSP `frame-src` 拦截 YouTube 嵌入视频
- **严重程度**：High (P1)
- **位置**：
  - `deploy/caddy/Caddyfile:21, 101`
  - `deploy/nginx/freedompost.conf:12`
- **根因分析**：
  - 代码支持将 YouTube 链接渲染为 `https://www.youtube-nocookie.com/embed/...` 的 `<iframe>`，但 `Caddyfile` 与 `freedompost.conf` 的 `Content-Security-Policy` 中 `frame-src` 仅配置了 `'self' https://challenges.cloudflare.com`，浏览器会直接拦截所有文章内的 YouTube 视频。
- **修复方案**：
  - 在 `deploy/caddy/Caddyfile` 与 `deploy/nginx/freedompost.conf` 的 `frame-src` 中加入 `https://www.youtube-nocookie.com`。

---

### `[x]` BUG-INFRA-02：CI/CD 与部署脚本缺陷（`deploy.yml` 缺失 `PUBLIC_SITE_URL`、`remote-deploy.sh` 丢失环境变量与回滚循环中断、`benefit-campaign.yml` 使用 `curl -k`、`backup-db.sh` 非原子写入与 `packages/db` Schema 漂移）
- **严重程度**：High (P1)
- **位置**：
  - `.github/workflows/deploy.yml:121-128`
  - `deploy/remote-deploy.sh:21-28, 136-157, 173-230`
  - `.github/workflows/benefit-campaign.yml:68`
  - `deploy/scripts/backup-db.sh:7-10`
  - `packages/db/src/schema.ts`
- **根因分析**：
  1. `.github/workflows/deploy.yml` 构建前端静态产物时未注入 `PUBLIC_SITE_URL`，导致生产构建产物中的 `robots.txt`、`sitemap.xml` 与 Canonical 链接全部回退为 `http://localhost:4321`。
  2. `deploy/remote-deploy.sh` 未将 `CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES` 写入远程 `.env.production`；`set_env` 使用 `awk -v value="$value"` 会破坏含 `\` 或 `&` 的密钥；回滚快照循环中若某个服务容器尚未创建（如首次引入新服务），`test -n "$container"` 在 `set -e` 下直接退出导致部署中断。
  3. `.github/workflows/benefit-campaign.yml:68` 使用了 `curl -kfsS`，违反仓库治理规范（严禁使用 `curl -k` 绕过证书校验）。
  4. `deploy/scripts/backup-db.sh` 未设置 `umask 077` 且直接重定向到目标文件，若 `pg_dump` 失败会留下损坏的半成品并继续清理旧备份。
  5. `packages/db/src/schema.ts` 缺少 `0010`～`0014` 迁移新增的表与字段。
- **修复方案**：
  1. 在 `.github/workflows/deploy.yml` 的构建步骤注入 `PUBLIC_SITE_URL: ${{ vars.PUBLIC_SITE_URL || 'https://freedompost.thinderbox.uk' }}`。
  2. 在 `deploy/remote-deploy.sh` 中持久化 `CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES`，修复 `set_env` 通过环境变量 `ENV_KEY`/`ENV_VALUE` 避免 `awk -v` 转义丢失，并在回滚快照循环中当容器不存在时跳过而非报错退出。
  3. 将 `.github/workflows/benefit-campaign.yml:68` 的 `curl -kfsS` 改为 `curl --cacert deploy/trust/cloudflare-origin-ca-rsa.pem -fsS`。
  4. 在 `deploy/scripts/backup-db.sh` 中增加 `umask 077` 并先写入临时文件 `.tmp.$$` 成功后 `mv`。
  5. 同步更新 `packages/db/src/schema.ts` 使其与最新 SQL 迁移保持一致。

---

## 全量 Code Review 与验证结论 (Final Code Review Report)

### 1. 静态检查与自动化测试结果
- **Go 后端 (`services/api`)**：`go test ./...` & `go vet ./...` 全部通过（含新增的跨站订单拦截测试、客服桥接多行 `FP-CW:` 及超长截断测试、渲染器 `iframe` 白名单与 `<u>` 标签测试、`paidaccess` 上游错误透传测试）。
- **Go 付费访问服务 (`services/paid-access`)**：`go test ./...` & `go vet ./...` 全部通过（含内存限流器逐桶 `expiresAt` 生命周期修复与 HMAC 负时间戳上溢防御）。
- **TypeScript 类型检查与构建 (`npm run typecheck` & `npm run build`)**：7 个共享包 (`@freedompost/*`)、`apps/public-reader` (`astro check` 0 errors / 0 warnings / 0 hints) 及 `apps/admin` 全部通过类型检查与生产构建。
- **工作区单元测试 (`npm test --workspaces --if-present`)**：全部通过（含编辑器第三轮回归测试、商品保存 UI 测试、分销与订单移交测试、安全与渲染包测试）。
- **部署与治理预检测试 (`node --test deploy/*.test.mjs`)**：31/31 项测试全部通过。

### 2. 关键安全与工程质量复核要点
1. **边界防护一致（Defense-in-Depth）**：
   - 订单创建接口、管理后台接口与推广员写接口统一执行 `Sec-Fetch-Site: cross-site` 与 `AllowedOrigins` 校验。
   - Go 渲染器 (`bluemonday`)、TS 渲染器 (`sanitize-html`)、管理后台编辑器媒体插入与公共读者端 DOM 模板统一限制 URL 协议并收紧 `iframe` 至 `https://www.youtube-nocookie.com/embed/...`，同时在 `Caddyfile` 与 `nginx/freedompost.conf` 的 CSP `frame-src` 中精确放行该域名。
2. **并发与事务一致性**：
   - 评论路径生成 (`CreateComment`)、商品库存扣减 (`createAffiliateOrderTx`)、订单成交累计销量 (`UpdateAffiliateOrderStatus`) 与阅读量去重累加 (`RecordView`) 均在 PostgreSQL 行锁或原子 CTE 事务内完成，消除 TOCTOU 竞态。
3. **前后端异步生命周期与状态隔离**：
   - 管理后台通过 `activePostIdRef` 与目标实体 ID 绑定，彻底消除跨文章粘贴/保存与跨商品/跨工具上传封面的陈旧闭包覆盖问题；公共读者端消除了分销定价按钮监听器的重复绑定与快速切文竞态。

