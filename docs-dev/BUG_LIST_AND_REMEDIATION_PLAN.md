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

---

# 第二轮深度 Code Review（技术负责人 + 白帽极客视角）问题清单与修复方案

> **审查基线**：基于第一轮修复合并后的 `main` / `codex/development` 最新代码，从**契约一致性、数据库事务边界、边缘安全绕过、并发状态机与异步闭包生命周期**五个维度进行二次穿透式审查。
> **处理原则**：先完整记录根因分析与无副作用修复方案，再按 **Round 2 Batch A / B / C** 逐批实施并通过全量回归测试。

## 第二轮修复进度总览 (Round 2 Progress Tracker)

| 批次 | 编号 | 严重程度 | 分类 | 模块 / 文件 | 问题描述 | 状态 |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **R2-Batch A** | `BUG-R2-01` | **Critical (P0)** | 前后端契约断裂 | `services/api/internal/domain/types.go`<br>`services/api/internal/httpapi/admin.go`<br>`services/api/internal/repository/postgres.go` | 管理后台保存/新建工具发送 `{ url, coverUrl }`，但 `domain.ToolInput` 仅定义 `{ linkUrl, iconUrl }`，被 `DisallowUnknownFields()` 直接拦截返回 `400 INVALID_JSON` | `[x]` |
| **R2-Batch A** | `BUG-R2-02` | **Critical (P0)** | 迁移事务越界 | `deploy/migrations/0008_ascii_post_slugs.sql` | `migrate.Run` 已在外层开启事务，但 `0008_ascii_post_slugs.sql` 内含显式 `BEGIN;` / `COMMIT;`，导致外层事务提前提交、临时表生命周期异常 | `[x]` |
| **R2-Batch A** | `BUG-R2-03` | **High (P1)** | 付费文章旧链接白屏 | `services/paid-access/internal/store/postgres.go`<br>`services/paid-access/internal/httpapi/handler.go` | `paid-access` 的 `FindArticle` 未查询 `post_slug_aliases`，通过历史别名访问付费文章返回 404 并回退到脱敏空正文；`createOrder` 未映射 `ErrInvalidState` | `[x]` |
| **R2-Batch A** | `BUG-R2-04` | **High (P1)** | 库存耗尽 DoS | `services/api/internal/repository/postgres.go` | 创建 `pending` 订单时扣减 `products.stock`，但管理员取消订单（`canceled`）时仅更新 `sold_count` 未归还 `stock`，恶意未付款订单可永久耗尽有限库存 | `[x]` |
| **R2-Batch A** | `BUG-R2-05` | **High (P1)** | 限流器 IP 伪造绕过 | `services/api/internal/httpapi/server.go`<br>`services/paid-access/internal/httpapi/handler.go` | 第一轮修复了 `middleware.go` 的 `clientIP`，但业务限流器实际调用的 `Server.remoteIP` 与 `API.remoteIP` 仍取 `X-Forwarded-For` 最左侧伪造 IP | `[x]` |
| **R2-Batch A** | `BUG-R2-06` | **Medium-High (P1)** | 客服桥接/福利/Slug 边界 | `services/api/internal/channelbridge/bridge.go`<br>`services/api/internal/httpapi/channelbridge.go`<br>`services/api/internal/httpapi/benefit.go`<br>`services/api/internal/repository/postgres.go`<br>`services/api/internal/httpapi/admin.go`<br>`services/api/cmd/server/main.go` | 企微 `FP-CW:` 大小写不一致及无会话消息触发 503 重试风暴；福利重试被自身已入库记录触发日限额拦截；`UpdatePost` 残留跨文章同 Slug 别名与长度校验缺失；优雅停机 `viewBuf.Flush` 顺序颠倒 | `[x]` |
| **R2-Batch B** | `BUG-R2-07` | **High (P1)** | 编辑器序列化丢失格式 | `apps/admin/src/main.tsx` | `nodeToMarkdown` 行内标签分支被误放在 `querySelector("...img...")` 内部；`inlineNodeToMarkdown` 读取浏览器解析后的 `img.src` 绝对地址触发外链拦截；行内 `![alt](url)` 被链接正则误匹配 | `[x]` |
| **R2-Batch B** | `BUG-R2-08` | **High (P1)** | 编辑器高亮块插入错位 | `apps/admin/src/main.tsx` | `insertCallout` 在折叠光标位于 `editor` 根容器（`startOffset > 0`）时计算 `endIndex = startIndex - 1`，导致 `selectedNodes` 为空并将高亮块跳插到文末 | `[x]` |
| **R2-Batch B** | `BUG-R2-09` | **High (P1)** | 跨文章异步串改与陈旧闭包 | `apps/admin/src/main.tsx` | `patchActivePost`、`syncEditorMarkdown`、`handleAttachmentFiles`、`handleEditorPaste`、`savePost` 及 `deletePost/Product/Tool` 存在跨条目切换时的陈旧闭包覆盖 | `[x]` |
| **R2-Batch B** | `BUG-R2-10` | **Medium (P2)** | 读者端异步竞态与异常恢复 | `apps/public-reader/src/scripts/benefit.ts`<br>`apps/public-reader/src/scripts/portal.ts`<br>`apps/public-reader/src/scripts/reader.ts` | `benefit.ts` Turnstile 同步回调状态被覆盖；`portal.ts` 分享链接 `new URL` 未捕获异常、空选加价静默成功、`popstate` 导航被丢弃；`reader.ts` 评论提交失败仍扣减本地限流配额且跨文串帖 | `[x]` |
| **R2-Batch C** | `BUG-R2-11` | **Medium (P2)** | 共享包渲染与白名单漂移 | `packages/renderer/src/index.ts`<br>`packages/security/src/index.ts` | TS 渲染器提取标题文本未剥离行内 Markdown 语法（与 Go 渲染器不一致）；`@freedompost/security` 缺少 YouTube 嵌入容器的 CSS class 白名单 | `[x]` |
| **R2-Batch C** | `BUG-R2-12` | **Medium (P2)** | 部署脚本与工作流加固 | `deploy/remote-deploy.sh`<br>`.github/workflows/benefit-campaign.yml`<br>`.github/workflows/channel-diagnostics.yml` | `remote-deploy.sh` 多容器 ID 拼接导致 `docker tag` 失败、空 `REDIS_PASSWORD` 覆盖原密码、无条件重启 Docker daemon、`pg_isready` 超时未退出；工作流缺失 `DEPLOY_PORT` 与容器判空 | `[x]` |

---

## Round 2 Batch A：Go 后端 (`services/api` & `services/paid-access`) 与数据库迁移

### `[x]` BUG-R2-01：`ToolInput` JSON 契约不一致导致管理后台无法新建或保存工具（`400 INVALID_JSON`）
- **严重程度**：Critical (P0)
- **位置**：
  - `services/api/internal/domain/types.go:384-394`
  - `services/api/internal/httpapi/admin.go:553-587`
  - `services/api/internal/repository/postgres.go:781-814`
  - `apps/admin/src/main.tsx:1952-1959`
- **根因分析**：
  - 管理后台 `saveTool` 发送的 JSON 字段为 `{ title, summary, description, category, url, coverUrl, status, sortOrder }`，且 `domain.Tool` 返回时也同时序列化了 `url`/`linkUrl` 与 `coverUrl`/`iconUrl`。
  - 但 `domain.ToolInput` 仅定义了 `IconURL string json:"iconUrl"` 和 `LinkURL string json:"linkUrl"`，未定义 `url` 和 `coverUrl`。
  - 由于第一轮加固 `decodeJSON` 启用了 `dec.DisallowUnknownFields()`，管理后台所有 `POST /api/admin/tools` 和 `PUT /api/admin/tools/{id}` 请求都会因为包含未知字段 `"url"` 和 `"coverUrl"` 而直接报错 `400 INVALID_JSON`！即便不报错，`url` 与 `coverUrl` 也会全部丢失（存为 `"#"` 和 `""`）。
- **修复方案**：
  1. 在 `domain.ToolInput` (`services/api/internal/domain/types.go:384-394`) 中同时支持 `URL`/`LinkURL` 与 `CoverURL`/`IconURL`，实现自定义 `UnmarshalJSON`（将 `url` 归一到 `LinkURL`，将 `coverUrl` 归一到 `IconURL`，同时兼容旧字段名并保持 `DisallowUnknownFields()` 严格校验）。
  2. 在 `adminCreateTool` 与 `adminUpdateTool` (`services/api/internal/httpapi/admin.go:553-587`) 中增加 `normalizeToolInput` 规范化与 URL 协议/状态校验，并在 `CreateTool`/`UpdateTool` (`postgres.go:795, 810`) 中使用 `nullStr(input.IconURL)` 保持数据库 `cover_url` 可空语义一致。
  3. 新增单元测试验证 `{ url, coverUrl }` 与 `{ linkUrl, iconUrl }` 两种载荷均可正常创建和更新工具。

---

### `[x]` BUG-R2-02：`0008_ascii_post_slugs.sql` 内嵌 `BEGIN;` / `COMMIT;` 破坏 `migrate.Run` 外层事务
- **严重程度**：Critical (P0)
- **位置**：
  - `deploy/migrations/0008_ascii_post_slugs.sql:1, 20-23, 68`
  - `services/api/internal/migrate/migrate.go:135-155`
- **根因分析**：
  - `migrate.Run` 对每个迁移文件执行 `tx, err := pool.Begin(ctx)` -> `tx.Exec(ctx, string(sql))` -> `tx.Exec("INSERT INTO go_schema_migrations ...")` -> `tx.Commit(ctx)`。
  - `0008_ascii_post_slugs.sql` 首行包含 `BEGIN;`，末行包含 `COMMIT;`。在 `tx.Exec` 中执行 `COMMIT;` 会提前提交 `migrate.Run` 的外层事务，使后续的 `INSERT INTO go_schema_migrations` 脱离原事务运行，且 `ON COMMIT DROP` 临时表与事务边界耦合脆弱。
- **修复方案**：
  - 移除 `deploy/migrations/0008_ascii_post_slugs.sql` 中的显式 `BEGIN;` 与 `COMMIT;`，将 `CREATE TEMP TABLE migration_0008_post_slugs (...) ON COMMIT DROP;` 改为在脚本末尾显式执行 `DROP TABLE migration_0008_post_slugs;`，使其无论由 `migrate.Run` 外层事务包裹还是单独在事务中执行均完全幂等、原子。

---

### `[x]` BUG-R2-03：`services/paid-access` 的 `FindArticle` 未解析 `post_slug_aliases` 导致旧链接访问付费文章白屏
- **严重程度**：High (P1)
- **位置**：
  - `services/paid-access/internal/store/postgres.go:99-108`
  - `services/paid-access/internal/httpapi/handler.go:311-319`
- **根因分析**：
  - `services/api` 的 `GetPostBySlug` 支持通过 `post_slug_aliases` 解析文章历史 Slug（如 `0008` 迁移前的中文/旧 Slug 或管理员改名后的旧 Slug）。
  - 但 `services/paid-access` 的 `FindArticle` 仅查询 `WHERE slug = $1`。当读者通过历史旧 Slug 打开付费文章时，`GET /api/paid-articles/{slug}` 返回 `404 ARTICLE_NOT_FOUND`，前端 `reader.ts` 捕获错误后回退到 `GET /api/posts/{slug}`（返回为防泄漏已清空的 `html: ""`），导致页面既无正文也无付费墙，呈现白屏。
  - 此外，`services/paid-access/internal/httpapi/handler.go:311` 的 `createOrder` 未处理 `domain.ErrInvalidState`，当免费文章被调用下单接口时会落入 `default:` 返回 `500 INTERNAL_ERROR`。
- **修复方案**：
  1. 将 `services/paid-access/internal/store/postgres.go:99-108` 的 `FindArticle` 查询更新为：
     `WHERE slug = $1 OR id = (SELECT post_id FROM post_slug_aliases WHERE slug = $1) ORDER BY (slug = $1) DESC LIMIT 1`。
  2. 在 `services/paid-access/internal/httpapi/handler.go:311-319` 的 `createOrder` 错误分支中增加 `case errors.Is(err, domain.ErrInvalidState): writeError(w, http.StatusConflict, "ARTICLE_NOT_PAID", "该文章无需购买")`。

---

### `[x]` BUG-R2-04：商品订单取消时未归还 `products.stock` 导致有限库存被未付款订单永久耗尽
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/repository/postgres.go:1181-1185, 1220-1275`
- **根因分析**：
  - `createAffiliateOrderTx` 在创建 `pending` 订单时立即对有限库存商品执行 `stock = CASE WHEN stock > 0 THEN stock - 1 ELSE stock END`。
  - 但管理员在后台通过 `UpdateAffiliateOrderStatus` 将订单状态改为 `'canceled'` 时，`sold_sync` CTE 仅同步了 `sold_count`，从未将 `stock` 加回 `1`（同样地，若将误取消的 `'canceled'` 订单恢复为 `'pending'`/`'completed'` 也未重新扣减库存）。攻击者只需匿名发起未付款订单即可将有限库存商品耗尽，且管理员取消垃圾订单后库存仍为 `0`。
- **修复方案**：
  - 在 `UpdateAffiliateOrderStatus` (`services/api/internal/repository/postgres.go:1220-1275`) 中：
    1. 当订单从 `'canceled'` 恢复为非 `'canceled'` 且商品为有限库存（`stock >= 0`）时，校验 `stock > 0` 并扣减 `1`（若 `stock == 0` 则拒绝恢复并返回库存不足错误）。
    2. 在 `sold_sync` CTE 中同步更新 `stock`：当从非 `'canceled'` 变为 `'canceled'` 且 `p.stock >= 0` 时 `stock = p.stock + 1`；当从 `'canceled'` 变为非 `'canceled'` 且 `p.stock > 0` 时 `stock = p.stock - 1`。

---

### `[x]` BUG-R2-05：`services/api` 与 `services/paid-access` 的业务限流器 `remoteIP` 仍读取 `X-Forwarded-For` 最左侧伪造 IP
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/httpapi/server.go:230-248`
  - `services/paid-access/internal/httpapi/handler.go:538-549`
- **根因分析**：
  - 第一轮修复（`BUG-SEC-05`）修复了日志中间件 `middleware.go` 中的 `clientIP` 函数（从右向左读取 `X-Forwarded-For`），但 `services/api` 业务处理器（管理员登录、评论提交、订单创建、福利申领）实际调用的是 `s.remoteIP(r)` (`server.go:230`)，而 `services/paid-access`（读者注册、读者登录）实际调用的是 `a.remoteIP(r)` (`handler.go:538`)！
  - 这两个 `remoteIP` 方法仍然执行 `strings.Split(xff, ",")[0]`，导致客户端只需发送伪造的 `X-Forwarded-For: 1.2.3.4` 请求头即可绕过所有基于 IP 的登录防爆破、评论限流与福利申领每日 IP 限额。
- **修复方案**：
  1. 将 `services/api/internal/httpapi/server.go:230` 的 `s.remoteIP(r)` 直接委托给已加固的 `clientIP(r, s.cfg != nil && s.cfg.TrustProxy)`，消除重复实现。
  2. 将 `services/paid-access/internal/httpapi/handler.go:538` 的 `a.remoteIP(r)` 更新为相同的安全逻辑：优先检查 `CF-Connecting-IP` / `X-Real-IP` 单值可信头，解析 `X-Forwarded-For` 时从右向左取最后一个合法 IP，回退时使用 `net.SplitHostPort(r.RemoteAddr)`。

---

### `[x]` BUG-R2-06：客服桥接、福利申领重试、Slug 别名清理与优雅停机边界缺陷
- **严重程度**：Medium-High (P1)
- **位置**：
  - `services/api/internal/channelbridge/bridge.go:28, 188-195, 215-253`
  - `services/api/internal/httpapi/channelbridge.go:162-167`
  - `services/api/internal/httpapi/benefit.go:173-184`
  - `services/api/internal/repository/postgres.go:277, 1524-1537`
  - `services/api/internal/httpapi/admin.go:234, 258`
  - `services/api/cmd/server/main.go:377-383`
- **根因分析**：
  1. **客服桥接 (`channelbridge`)**：
     - `bridge.go:225` 使用 `strings.HasPrefix(strings.ToUpper(content), "FP-CW:")` 判断前缀，但第 28 行正则 `conversationReference` 为区分大小写的 `(?s)^\s*FP-CW:(\d+)\s+(.+?)\s*$`，若客服输入小写 `fp-cw:123 你好`，会报错 `invalid FP-CW reference`。
     - 当启用 `allowUnprefixed` 且客服在尚无活跃会话时发送普通消息，`resolveTargetConversation` 返回 `"wecom operator ... has no active conversation"`，但 `httpapi/channelbridge.go:162` 的 `isIgnoredWeComMessageError` 未包含该错误，导致返回 HTTP 503 触发企微回调重试风暴。
     - 当客服通过显式 `FP-CW:<id>` 回复某个会话时，未调用 `s.mappings.Put` 更新该客服的最近活跃会话映射，导致紧接着发送的图片仍路由到旧会话。
  2. **福利申领重试 (`benefit.go:173-184`)**：
     - 当用户的 `benefit_claims` 记录已创建但首次发放失败（状态为 `failed` 或超时恢复为 `pending`）时，用户点击重试会再次进入 `benefitClaim`。但步骤 7 (`CountBenefitClaimsByNetworkSince`) 在 `if claim == nil` 之前无条件执行，且该用户刚才创建的记录已经计入 `benefit_claims` 表！若 `DailyNetworkClaimLimit == 1`，用户自己的失败记录就会把自己挡在 `429 RATE_LIMITED` 之外，永远无法重试。
  3. **文章与商品 Slug 边界 (`postgres.go:277, 1531` & `admin.go:258`)**：
     - `UpdatePost` (`postgres.go:277`) 执行 `DELETE FROM post_slug_aliases WHERE slug = $1 AND post_id = $2`，若某历史别名曾属于另一篇已归档文章，这里未清理非本文章的同名别名；且 `adminUpdatePost` (`admin.go:258`) 缺少 `len(input.Slug) > 32` 长度上限校验；`uniqueProductSlug` (`postgres.go:1531`) 忽略了 `GetProductBySlug` 的数据库错误。
  4. **优雅停机 (`cmd/server/main.go:377-383`)**：
     - 停机时先执行了 `viewBuf.Flush(shutdownCtx)`，后执行 `httpSrv.Shutdown(shutdownCtx)`，在途请求完成时写入的阅读量计数不会再被 Flush 到数据库。
- **修复方案**：
  1. 将 `conversationReference` 改为 `(?is)^\s*FP-CW:(\d+)\s+(.+?)\s*$`；在 `isIgnoredWeComMessageError` 中加入 `"has no active conversation"`；在显式 `FP-CW:<id>` 解析成功后调用 `s.mappings.Put(ctx, domain.ChannelConversationMap{...})`。
  2. 在 `services/api/internal/httpapi/benefit.go:173-184` 中，仅在 `claim == nil`（需要新建申领记录）时检查 `CountBenefitClaimsByNetworkSince` 每日网络限额。
  3. 将 `postgres.go:277` 改为 `DELETE FROM post_slug_aliases WHERE slug = $1`；在 `admin.go` 的 `isValidSlug` 中统一校验 `len(slug) >= 1 && len(slug) <= 32`；在 `uniqueProductSlug` 中检查并返回 `err`。
  4. 在 `services/api/cmd/server/main.go:377-383` 中先执行 `httpSrv.Shutdown(shutdownCtx)` 等待在途请求排空，再执行 `viewBuf.Flush(shutdownCtx)`。

---

## Round 2 Batch B：前端应用 (`apps/admin` & `apps/public-reader`)

### `[x]` BUG-R2-07：管理后台编辑器序列化丢失行内格式与图片相对路径被转为绝对 URL
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:2497-2508, 2524, 2544-2547, 2612-2633`
- **根因分析**：
  1. 在 `nodeToMarkdown` (`main.tsx:2497-2508`) 中，行内标签守卫 `/^(STRONG|B|EM|I|DEL|S|STRIKE|U|CODE|SPAN|A)$/` 被放在了 `if (node.querySelector("figure.editor-image,figure.editor-youtube,img,.editor-attachment"))` 块内部！导致：
     - 顶层不含图片的 `<strong>`、`<u>`、`<a>` 等节点直接落入第 2524 行 `inlineChildrenToMarkdown(node)`，丢失外层标签；
     - 含行内 `<img>` 与 `<strong>` 的段落 `<p>文字 <strong>加粗</strong> <img ...></p>` 被拆成多个独立段落，且子级 `<strong>` 递归进入 `nodeToMarkdown` 时因不含图片而丢失加粗格式。
  2. 在 `inlineNodeToMarkdown` (`main.tsx:2546`) 中，读取 `node.src` 会返回浏览器解析后的完整绝对地址（如将 `/api/uploads/foo.png` 变成 `http://localhost:5173/api/uploads/foo.png`），保存时被后端 `post_image_policy` 判定为未转存外链图片拦截（而 `nodeToMarkdown` 第 2463 行正确使用了 `getAttribute("src")`）。
  3. 在 `formatInlineMarkdown` (`main.tsx:2622`) 中，段落内的行内图片语法 `![alt](url)` 会被链接正则 `/\[([^\]]+)]\((...)\)/g` 匹配并渲染成 `!<a href="url">alt</a>`。
- **修复方案**：
  1. 在 `nodeToMarkdown` 中，将 `if (/^(STRONG|B|EM|I|DEL|S|STRIKE|U|CODE|SPAN|A)$/.test(node.tagName)) return inlineNodeToMarkdown(node).trim();` 移到 `querySelector` 判断之外；同时将第 2497 行的拆分条件收紧为仅拆分块级媒体容器 `node.querySelector("figure.editor-image,figure.editor-youtube,.editor-attachment")`，使含普通 `<img>` 的段落保持行内序列化。
  2. 将 `inlineNodeToMarkdown` (`main.tsx:2546`) 中的 `node.src` 改为 `(node.getAttribute("src") ?? "").trim()`。
  3. 在 `formatInlineMarkdown` (`main.tsx:2622`) 的链接替换前增加行内图片语法 `/!\[([^\]]*)]\((https?:\/\/[^)\s]+|\/[^)\s]+)\)/g` 的转换。

---

### `[x]` BUG-R2-08：`insertCallout` 在根容器折叠光标下 `endIndex < startIndex` 导致高亮块跳插到文末
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:822-847`
- **根因分析**：
  - 当光标折叠（未选中文本，`range.collapsed === true`）且 `range.startContainer === editor`、`range.startOffset === k (k > 0)` 时：
    - `startAnchor` 取 `editor.childNodes[k]`（即 `startIndex = k`）；
    - `endAnchor` 取 `editor.childNodes[Math.max(0, k - 1)]`（即 `endIndex = k - 1`）。
  - 随后 `children.slice(startIndex, endIndex + 1)` 变成 `children.slice(k, k)`，得到空数组 `selectedNodes = []`，最终走入 `else` 分支 `editor.append(callout, trailingParagraph)`，把高亮块错误插入到整篇文章最底部。
- **修复方案**：
  - 在 `insertCallout` (`apps/admin/src/main.tsx:822-847`) 中：
    - 若 `range.collapsed && range.startContainer === editor`，直接将 `callout` 插入到 `editor.childNodes[range.startOffset] ?? null` 之前；
    - 否则当 `range.collapsed` 为 `true` 时，令 `endAnchor = startAnchor`，确保 `endIndex === startIndex`。

---

### `[x]` BUG-R2-09：管理后台跨文章切换时的陈旧闭包覆盖与删除状态误清空
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:393-532, 580-595, 663-673, 990-993, 1021-1075, 1733-1740, 1871-1899`
- **根因分析**：
  1. `patchActivePost` (`line 513`) 与 `syncEditorMarkdown` (`line 990`) 捕获了当前渲染帧的 `activePost.id`。当图片/附件异步上传完成调用 `replaceMediaPlaceholder` -> `syncEditorMarkdown()`，或 `autoImportEditorImages` 异步完成调用 `patchActivePost` 时，若用户已切换到另一篇文章，会把新文章的编辑器 DOM 序列化写回旧文章状态（或反之）。
  2. `handleAttachmentFiles` (`line 518`) 与 `handleEditorPaste` 的文件粘贴分支 (`line 582`) 未绑定 `targetPostId = activePostIdRef.current`，上传期间切换文章会把附件/图片插入到新文章光标处。
  3. `savePost` (`line 395`) 在 `await pendingMediaRef.current.waitForIdle()` 与 `await autoImportEditorImages(...)` 之前快照了 `const targetPost = activePost`，等待期间用户对标题、可见性或价格的修改会在 `doSave` 发送时被旧快照覆盖。
  4. `deletePost` (`line 508`)、`deleteProduct` (`line 1738`) 与 `deleteTool` (`line 1897`) 在 `DELETE` 请求返回后无条件执行 `setActiveId(null)`，若用户在请求期间切换到了其他条目，会将其错误取消选中。
- **修复方案**：
  1. 新增 `postsRef` 实时同步最新 `posts` 列表；为 `patchActivePost(patch, targetId = activePostIdRef.current)` 与 `syncEditorMarkdown(targetId = activePostIdRef.current)` 增加 `if (!targetId || activePostIdRef.current !== targetId) return;` 守卫。
  2. 在 `insertPendingMediaAtCaret` / `replaceMediaPlaceholder` 中绑定发起时的 `targetPostId = activePostIdRef.current`，若完成时 `activePostIdRef.current !== targetPostId` 则移除占位符并不修改当前文章 DOM。
  3. 在 `savePost` 的 `doSave` 中从 `postsRef.current` 读取 `targetPost.id` 的最新元数据（`title`、`slug`、`excerpt`、`visibility`、`priceCents`、`currency`）。
  4. 在 `deletePost`、`deleteProduct`、`deleteTool` 中改用函数式更新 `setActiveId((current) => (current === targetId ? null : current))`。

---

### `[x]` BUG-R2-10：公共读者端 `benefit.ts`、`portal.ts`、`reader.ts` 异步竞态与异常恢复缺陷
- **严重程度**：Medium (P2)
- **位置**：
  - `apps/public-reader/src/scripts/benefit.ts:91-118, 264-280`
  - `apps/public-reader/src/scripts/portal.ts:291-331, 842-848, 894, 976-983`
  - `apps/public-reader/src/scripts/reader.ts:879-960, 1066-1090, 1128-1139`
- **根因分析**：
  1. `benefit.ts`：`start()` 在 `await this.prepareTurnstile()` 之后才调用 `this.renderState("idle", ...)`，若 Turnstile 同步触发回调，状态会被随后的 `idle` 覆盖；`retry()` 在 `this.campaign` 已加载时未先尝试 `restoreClaim()`，若前一次超时请求其实已在服务端成功发放，点击重试会要求重新过人机验证。
  2. `portal.ts`：`productShareUrl` 直接调用 `new URL(shareUrl, location.origin)` 未捕获非法 URL 异常；`#applyAffiliateSelected` 在未勾选任何商品（`ids.length === 0`）时仍发送请求并提示“已为选中商品应用加价”；`loadRoute` 用布尔锁 `if (routeLoading) return;` 丢弃并发 `popstate` 事件，导致快速后退时地址栏与页面内容不一致。
  3. `reader.ts`：`submitComment` 在 `createRemoteComment` 发送前就通过 `checkCommentRate()` 将时间戳写入 `localStorage`，若网络或后端报错，用户重试满 3 次即被本地锁死；且 `submitComment` 在 `await uploadCommentAttachment` 期间未快照 `activeSlug` 与 `replyParentId`；`checkCommentRate` 与 `getRandomUsername` 的 `JSON.parse` 缺少 `try/catch`。
- **修复方案**：
  1. 在 `benefit.ts` 的 `start()` 中先设置 `idle` 状态再调用 `prepareTurnstile()`；在 `retry()` 中若 `this.campaign` 已存在则先调用 `await this.restoreClaim()`，若未恢复成功再执行 `prepareTurnstile()`。
  2. 在 `portal.ts` 的 `productShareUrl` 中增加 `try/catch` 回退；在 `#applyAffiliateSelected` 中当 `ids.length === 0` 时提示 `"请先勾选要应用加价的商品"` 并返回；将 `loadRoute` 的布尔锁替换为递增序号 `activeRouteSeq` 防止丢弃最新路由。
  3. 在 `reader.ts` 中将 `checkCommentRate` 拆分为只读校验与成功后记录 `recordCommentRate`，在 `submitComment` 入口快照 `targetSlug = activeSlug` 与 `targetReplyParentId = replyParentId` 并传入上传与创建函数，同时为 `localStorage` 的 `JSON.parse` 增加 `try/catch`。

---

## Round 2 Batch C：共享包 (`packages/*`)、部署脚本 (`deploy/*`) 与 CI/CD 工作流

### `[x]` BUG-R2-11：`@freedompost/renderer` 标题纯文本提取与 `@freedompost/security` 类名白名单漂移
- **严重程度**：Medium (P2)
- **位置**：
  - `packages/renderer/src/index.ts:82-92`
  - `packages/security/src/index.ts:69-100`
- **根因分析**：
  1. `packages/renderer/src/index.ts:82-92` 在生成标题 `id` 和 TOC 目录文本时直接使用了 `inline` token 的原始 `next?.content`（例如 `## Hello [World](https://example.com) **bold**` 会把 URL 和星号一起塞进 TOC 标题和 `slugifyHeading`），而 Go 后端 `services/api/internal/renderer/renderer.go:258` 是递归提取纯文本节点，导致 TS 与 Go 渲染出的标题锚点 ID 不一致。
  2. `packages/security/src/index.ts:69-100` 的 `articleAllowedClasses` 未包含 YouTube 嵌入容器使用的 `"youtube-embed"`、`"youtube-player"`、`"youtube-watch-link"`，若通过 `@freedompost/security` 二次清洗含 YouTube 嵌入的 HTML 会丢失样式类。
- **修复方案**：
  1. 在 `packages/renderer/src/index.ts` 中新增 `extractInlineText(inlineToken)`，从 `inlineToken.children` 中拼接 `text` 与 `code_inline` 节点文本（若无 `children` 再回退到 `content`），与 Go 渲染器保持一致。
  2. 在 `packages/security/src/index.ts` 的 `articleAllowedClasses` 中补齐 `"youtube-embed"`、`"youtube-player"`、`"youtube-watch-link"`。

---

### `[x]` BUG-R2-12：`deploy/remote-deploy.sh` 与 GitHub Actions 运维工作流健壮性加固
- **严重程度**：Medium (P2)
- **位置**：
  - `deploy/remote-deploy.sh:22, 189-190, 316-331, 392-399`
  - `.github/workflows/benefit-campaign.yml:38`
  - `.github/workflows/channel-diagnostics.yml:25-40`
- **根因分析**：
  1. `deploy/remote-deploy.sh:22`：`docker compose ... ps -aq "$service"` 在存在历史停止容器时会返回多行 ID，导致 `docker inspect $container` 参数裂变报错。
  2. `deploy/remote-deploy.sh:190`：当环境变量未传入 `REDIS_PASSWORD` 时，第 190 行仍无条件执行 `set_env "REDIS_URL" "redis://:${REDIS_PASSWORD}@redis:6379"`，将 `.env` 中原有的 Redis 密码覆盖为空密码。
  3. `deploy/remote-deploy.sh:316-331`：每次部署无论 `/etc/docker/daemon.json` 是否已配置相同内容，都会无条件重写并执行 `systemctl restart docker`，造成不必要的容器重启抖动。
  4. `deploy/remote-deploy.sh:392-399`：`pg_isready` 循环 30 次全部失败后未报错退出，而是直接继续执行后续迁移。
  5. `.github/workflows/benefit-campaign.yml:38` 缺少 `port: ${{ secrets.DEPLOY_PORT || 22 }}`；`.github/workflows/channel-diagnostics.yml:25-40` 未判断 `sudo` 及 `container_id` 是否为空。
- **修复方案**：
  1. 在 `deploy/remote-deploy.sh:22` 使用 `head -n 1` 取单个容器 ID；将第 190 行 `set_env "REDIS_URL" ...` 移入 `if [ -n "${REDIS_PASSWORD:-}" ]; then` 分支内；在写入 `/etc/docker/daemon.json` 前先比对现有文件内容，仅当不一致时才更新并重启 Docker；在 `pg_isready` 第 30 次仍失败时 `exit 1`。
  2. 在 `.github/workflows/benefit-campaign.yml` 补齐 `port: ${{ secrets.DEPLOY_PORT || 22 }}`，并在 `.github/workflows/channel-diagnostics.yml` 增加 `SUDO` 检测与 `[ -n "$container_id" ]` 判空保护。

---

## 第二轮全量验证与 Code Review 签核报告 (Round 2 Verification & Sign-off)

### 1. 自动化测试与静态检查全量结果

| 检查项 | 执行命令 | 结果 | 说明 |
| :--- | :--- | :--- | :--- |
| **Go API 单元测试 & 静态检查** | `cd services/api && go test ./... && go vet ./...` | **PASS (0 errors)** | 覆盖 `ToolInput` 双格式契约 (`url`/`coverUrl` & `linkUrl`/`iconUrl`)、`realIP` 可信代理解析、企微 `FP-CW:` 大小写与无活跃会话忽略、福利申领重试限额豁免、商品取消订单归还库存 |
| **Go Paid-Access 单元测试 & 静态检查** | `cd services/paid-access && go test ./... && go vet ./...` | **PASS (0 errors)** | 覆盖 `FindArticle` 历史 `post_slug_aliases` 别名解析、`createOrder` 非付费文章 `409 ARTICLE_NOT_PAID` 映射与 `remoteIP` 从右向左解析 `X-Forwarded-For` |
| **TypeScript 全仓类型检查** | `npm run typecheck` | **PASS (0 errors)** | 7 个 `packages/*`、`apps/public-reader` (`astro check` 67 files, 0 errors/warnings/hints) 与 `apps/admin` (`tsc --noEmit`) 全部零错误通过 |
| **Node / 前端工作区单元测试** | `npm test --workspaces --if-present` | **PASS (0 errors)** | `@freedompost/renderer` 含标题纯文本 TOC/锚点提取回归测试；`@freedompost/admin` 16 个测试文件共 78 项用例全部通过（含 `BUG-R2-07` 段落内联图片相对路径与加粗格式保留测试） |
| **全仓生产构建验证** | `npm run build` | **PASS (0 errors)** | 共享包、`apps/public-reader` 静态站点与 `apps/admin` 生产包全部构建成功 |
| **部署治理与预检策略测试** | `node --test deploy/*.test.mjs` | **PASS (31/31 tests)** | 5 个部署与安全策略测试套件共 31 项断言全部通过 |

### 2. 第二轮 Code Review 终审结论
1. **第一轮与第二轮共计 32 项（Round 1: 20 项 + Round 2: 12 项）问题已全部修复（100% `[x]`）**。
2. **零回归保证**：所有修复均经过方案先行评审、最小侵入式修改、新增针对性回归单测及全栈构建验证，未引入任何新编译错误或行为回退。

---

# 第三轮独立审查反馈（6 项发现）根因分析与修复方案

> **审查基线**：针对 `5bb702d..735a81f` 的第三方独立审查反馈，共确认 **6 项问题（2 项 P1、4 项 P2）**。
> **处理原则**：先完整记录每项问题的根因分析、边界复现与无副作用修复方案，再逐项落地修复并将独立复现脚本沉淀为仓库永久回归测试。

## 第三轮修复进度总览 (Round 3 Progress Tracker)

| 编号 | 严重程度 | 模块 / 文件 | 问题描述 | 状态 |
| :--- | :--- | :--- | :--- | :--- |
| `BUG-R3-01` | **High (P1)** | `services/api/internal/httpapi/middleware.go`<br>`services/paid-access/internal/httpapi/handler.go`<br>`deploy/caddy/Caddyfile` | **透传的 IP 头可绕过两服务的限流**：Caddy 仅覆写 `X-Forwarded-For` 未清理 `CF-Connecting-IP`/`X-Real-IP`，而后端优先读取 `CF-Connecting-IP`/`X-Real-IP` 并在 `X-Forwarded-For` 中向左跳过非法项，导致客户端可伪造请求头绕过限流 | `[x]` |
| `BUG-R3-02` | **High (P1)** | `apps/admin/src/main.tsx` | **旧文章保存响应会清空新文章的图片认领信息**：`savePost` 在 `PUT` 响应返回后无条件调用 `importedImageClaimsRef.current.clear()`，若保存期间切换到文章 B 并粘贴外链图，文章 A 的延迟响应会抹掉文章 B 的 `claimToken` | `[x]` |
| `BUG-R3-03` | **Medium (P2)** | `services/api/internal/repository/postgres.go`<br>`services/api/internal/httpapi/admin.go`<br>`deploy/migrations/0015_affiliate_order_stock_reserved.sql` | **零库存仍可恢复取消订单，且再次取消会制造库存**：`UpdateAffiliateOrderStatus` 在 `p.stock = 0` 时使用 `GREATEST(0, p.stock - 1)` 仍允许将 `canceled` 订单恢复为 `pending`/`completed`，随后再次取消会执行 `p.stock + 1` 凭空造出库存 | `[x]` |
| `BUG-R3-04` | **Medium (P2)** | `services/api/internal/repository/postgres.go`<br>`deploy/migrations/0015_affiliate_order_stock_reserved.sql`<br>`packages/db/src/schema.ts` | **取消未扣过库存的订单会凭空补库**：历史遗留订单或商品处于不限库存（`stock = -1`）时创建的订单未预占有限库存，后续商品改为有限库存并取消该订单时，无条件执行 `p.stock + 1` 导致库存虚增 | `[x]` |
| `BUG-R3-05` | **Medium (P2)** | `apps/admin/src/main.tsx` | **图片说明含方括号时，重新打开文章无法显示图片**：`editorImagesMarkdown` 将 `alt` 中的 `[`、`]` 转义为 `\[`、`\]`，但 `markdownToEditorHtml` 的正则 `^!\[([^\]]*)]\(([^)\s]+)\)$` 在遇到首个 `\]` 时截断失败，导致重新打开文章时图片退化为纯文本 | `[x]` |
| `BUG-R3-06` | **Medium (P2)** | `deploy/remote-deploy.sh`<br>`deploy/governance.test.mjs` | **回滚镜像查询失败被吞掉，部署仍会继续**：`remote-deploy.sh` 在 `docker compose ... ps -aq "$service" | head -n 1 || true` 中使用 `|| true` 吞掉了 `docker compose` 执行失败且移除了 `test -n "$container"` 强校验，破坏了生产变更必须具备可用回滚点的门禁约束 | `[x]` |

---

### `[x]` BUG-R3-01：透传的 `CF-Connecting-IP` / `X-Real-IP` 及 `X-Forwarded-For` 解析可绕过两服务限流
- **严重程度**：High (P1)
- **位置**：
  - `services/api/internal/httpapi/middleware.go:98-116`
  - `services/paid-access/internal/httpapi/handler.go:540-566`
  - `deploy/caddy/Caddyfile:38-48, 103-107`
- **根因分析**：
  1. 网关 `deploy/caddy/Caddyfile` 仅通过 `header_up X-Forwarded-For {remote_host}` 强制覆写 `X-Forwarded-For`，并未覆写或剥离客户端请求中的 `CF-Connecting-IP` 与 `X-Real-IP`。
  2. `services/api` 的 `realIP` 与 `services/paid-access` 的 `API.remoteIP` 在 `trustProxy` 启用时优先读取了 `CF-Connecting-IP` 与 `X-Real-IP`，攻击者只需在请求中附带伪造的 `CF-Connecting-IP` 或 `X-Real-IP` 即可完全绕过两服务的 IP 限流。
  3. 此外，在解析 `X-Forwarded-For` 时若从右向左循环跳过非 IP 片段（或仅用 `r.Header.Get` 读取首个同名头），当存在多行 `X-Forwarded-For` 或末尾非 IP 值时可能回退读取到左侧客户端伪造段。
- **修复方案**：
  1. 在 `services/api/internal/httpapi/middleware.go` (`realIP`) 与 `services/paid-access/internal/httpapi/handler.go` (`API.remoteIP`) 中移除对 `CF-Connecting-IP` 和 `X-Real-IP` 的读取；当 `trustProxy` 为 `true` 时，仅取最后一个非空 `X-Forwarded-For` 请求头的**最右侧最后一项**（即紧邻可信反向代理写入的客户端地址），若该项是合法 IP 则返回，否则直接回退至 `r.RemoteAddr`。
  2. 在 `deploy/caddy/Caddyfile` 的反向代理块中显式增加 `header_up -CF-Connecting-IP` 与 `header_up -X-Real-IP` 纵深防御。

---

### `[x]` BUG-R3-02：旧文章保存响应会清空新文章的图片认领信息 (`importedImageClaimsRef`)
- **严重程度**：High (P1)
- **位置**：
  - `apps/admin/src/main.tsx:242, 444-449, 488-493, 515-519, 562, 1068, 1086, 1133`
- **根因分析**：
  - `importedImageClaimsRef` 全局共用一个 `Map`，且 `savePost` 在 `await doSave(markdown)` 返回后无条件执行 `importedImageClaimsRef.current.clear()`。
  - 当管理员点击保存文章 A（`PUT /api/admin/posts/post-a` 仍在途），随后切换到文章 B 并在文章 B 中粘贴外链图片（转存成功后将 `claim-b` 写入 `importedImageClaimsRef`），此时文章 A 的保存请求返回，无条件执行 `.clear()`，导致文章 B 的 `claim-b` 被清空；随后保存文章 B 时 `importedImages` 为空数组 `[]`。
- **修复方案**：
  1. 将 `importedImageClaimsRef` 按文章维度隔离键名（`${postId}:${imageId}`），并在切换文章的 `useEffect([activePost?.id])` 中移除全量 `.clear()`。
  2. 在 `savePost` 的 `doSave` 中记录本次实际随请求发送的 `sentClaimKeys`（仅匹配 `targetPost.id` 下且 URL 存在于当前保存 `markdown` 中的认领项）；当保存成功返回后，仅从 `importedImageClaimsRef.current` 中删除 `sentClaimKeys`，绝不影响其他文章或保存期间新粘贴的图片认领信息。

---

### `[x]` BUG-R3-03 & BUG-R3-04：订单库存预占标记缺失导致零库存恢复订单与取消未预占订单虚增库存
- **严重程度**：Medium (P2)
- **位置**：
  - `services/api/internal/repository/postgres.go:1181-1292`
  - `services/api/internal/httpapi/admin.go:806-815`
  - `deploy/migrations/0015_affiliate_order_stock_reserved.sql`
  - `packages/db/src/schema.ts`
- **根因分析**：
  1. `affiliate_orders` 表未记录订单创建时是否实际扣减并预占了有限库存（`stock_reserved`）。对于历史存量订单（预占功能上线前创建）或商品处于不限库存（`stock = -1`）时创建的订单，其实并未扣减库存；一旦后续将订单取消（或商品改为有限库存后取消订单），`UpdateAffiliateOrderStatus` 无条件执行 `p.stock + 1`，凭空虚增库存。
  2. 当商品已售罄（`p.stock = 0`）时，管理员将已取消（`canceled`）的订单改回 `pending` 或 `completed`，原 SQL 使用 `GREATEST(0, p.stock - 1)`，在未扣减任何库存的情况下放行了状态恢复；若随后再次取消该订单，又会触发 `p.stock + 1` 将库存从 `0` 变成 `1`。
- **修复方案**：
  1. 新增迁移 `deploy/migrations/0015_affiliate_order_stock_reserved.sql`（及 `packages/db/src/schema.ts`），为 `affiliate_orders` 增加 `stock_reserved BOOLEAN NOT NULL DEFAULT FALSE`。
  2. 在 `createAffiliateOrderTx` 中，当 `product.Stock > 0` 时扣减库存并在插入 `affiliate_orders` 时写入 `stock_reserved = TRUE`（当 `product.Stock < 0` 时写入 `FALSE`）。
  3. 在 `UpdateAffiliateOrderStatus` 中：
     - 使用 `order_lock` 与 `product_lock` 锁定订单与关联商品行；
     - 当从 `'canceled'` 恢复为非 `'canceled'` 时，若关联商品为有限库存且当前 `stock = 0`，直接拒绝更新（返回 `domain.ErrInvalidState`，由 `adminUpdateAffiliateOrder` 返回 `409 PRODUCT_OUT_OF_STOCK`）；若 `stock > 0`，则扣减 `p.stock - 1` 并将 `ao.stock_reserved` 设为 `TRUE`（若 `stock < 0`，`ao.stock_reserved` 设为 `FALSE`）；
     - 当从非 `'canceled'` 变更为 `'canceled'` 时，将 `ao.stock_reserved` 设为 `FALSE`，且**仅当原订单 `prev_reserved = TRUE` 且 `p.stock >= 0`** 时才归还 `p.stock + 1`。

---

### `[x]` BUG-R3-05：图片说明（`alt`）含方括号时，重新打开文章无法解析并显示图片
- **严重程度**：Medium (P2)
- **位置**：
  - `apps/admin/src/main.tsx:2397-2412, 2672-2674, 2714-2743`
- **根因分析**：
  - `editorImagesMarkdown` 在序列化图片时调用 `escapeMarkdown(image.alt)` 将 `示例 [一]` 转义为 `![示例 \[一\]](/api/uploads/diagram.png)`。
  - 但 `markdownToEditorHtml`（块级图片 `^!\[([^\]]*)]\(([^)\s]+)\)$`）以及 `formatInlineMarkdown`（行内图片 `/!\[([^\]]*)]\((...)\)/g`）使用 `[^\]]*` 匹配 `alt`，遇到转义的 `\]` 即提前停止匹配并因后续不是 `](` 而匹配失败，导致整行退化为普通段落文本 `<p>![示例 \[一\]](/api/uploads/diagram.png)</p>`，丢失 `<img>` 渲染。
- **修复方案**：
  1. 在 `apps/admin/src/main.tsx` 中新增 `unescapeMarkdown(value: string): string`（还原 `\[`、`\]`、`\(`、`\)`、`\\`）。
  2. 在 `markdownFragmentToEditorHtml`（块级图片与附件）、`formatInlineMarkdown`（行内图片）及 `markdownImagesToPasteHtml` 中，将 `alt`/名称匹配更新为支持转义字符与内嵌方括号的正则，并在生成 HTML 前调用 `unescapeMarkdown` 还原原始说明文本，确保含方括号与圆括号的图片说明可无损往返。

---

### `[x]` BUG-R3-06：`deploy/remote-deploy.sh` 回滚镜像查询失败被 `|| true` 吞掉
- **严重程度**：Medium (P2)
- **位置**：
  - `deploy/remote-deploy.sh:21-29`
  - `deploy/governance.test.mjs`
- **根因分析**：
  - `remote-deploy.sh` 第 22 行 `container=$($SUDO docker compose ... ps -aq "$service" | head -n 1 || true)` 末尾的 `|| true` 吞掉了 `docker compose` 命令本身的执行错误，且用 `if [ -n "$container" ]` 替代了原有的 `test -n "$container"` 强校验，导致当回滚容器查询失败或容器缺失时部署脚本不会中止，违反 `AGENTS.md` “生产变更必须具备可用回滚点”的硬性规定。
- **修复方案**：
  1. 在 `deploy/remote-deploy.sh:21-29` 中移除 `|| true` 与 `if [ -n "$container" ]`：先无管道执行 `containers=$($SUDO docker compose --env-file "$DEPLOY_PATH/.env" -f "$DEPLOY_PATH/deploy/docker-compose.yml" ps -aq "$service")`，再通过参数展开 `container="${containers%%$'\n'*}"` 取首个容器 ID，并执行 `test -n "$container"` 强制断言非空。
  2. 在 `deploy/governance.test.mjs` 中新增断言，确保回滚快照循环包含 `test -n "$container"` 且不包含 `|| true`。

---

## 第三轮独立审查反馈修复验收结果 (Round 3 Verification Summary)

- **Go 后端验证**：
  - `gofmt -w services/api services/paid-access`
  - `cd services/api && go vet ./... && go test ./...` — 全部通过（含新增的 `TestRealIP` 伪造 `CF-Connecting-IP`/`X-Real-IP`/多段 `X-Forwarded-For` 防护单测）
  - `cd services/paid-access && go vet ./... && go test ./...` — 全部通过（含新增的 `TestRemoteIP` 伪造 IP 头防绕过单测）
  - 使用 PGlite 内存 PostgreSQL 验证 `UpdateAffiliateOrderStatus` 真实 SQL：覆盖正常有限库存扣减/退回、历史未预占订单（`stock_reserved = false`）取消不补库、不限库存转有限库存后取消旧单不补库、零库存（`stock = 0`）拒绝恢复已取消订单并防止凭空制造库存，6 项数据库断言全部通过。
- **前端与全工作区验证**：
  - `npm run typecheck` — 全工作区（含 `@freedompost/public-reader` Astro check 与 `@freedompost/admin` TS check）0 error
  - `npm test --workspaces --if-present` — 16 个测试文件共 80 项测试全部通过（含沉淀入 `apps/admin/src/existing-post-images.test.tsx` 的括号图片说明重开测试与跨文章保存竞态测试）
  - 独立复现脚本原位直跑：`npx vitest run --environment happy-dom C:/Temp/freedompost-frontend-independent-review/admin-image-alt.test.tsx C:/Temp/freedompost-frontend-independent-review/admin-save-race.test.tsx` — 2 项独立复现测试全部通过
  - `npm run build` — 全工作区生产构建通过
- **部署与治理脚本验证**：
  - `deploy/*.test.mjs` — 全部通过（含新增的回滚快照 `test -n "$container"` 强校验与禁止 `|| true` 治理测试）

---

# 第四轮独立复审反馈（3 项发现）根因分析与修复方案

> **审查基线**：针对 `735a81f..d1094f4`（[PR #70](https://github.com/Hao-Monster/FreedomPost/pull/70)）的第三方独立复审反馈。第三轮 6 项问题的原始触发场景均已确认关闭，但复审发现 3 项回归问题（**1 项 P1、2 项 P2**）。
> **处理原则**：先完整记录根因与修复方案，再逐项修复并通过独立复审复现脚本与全工作区测试验证。

## 第四轮修复进度总览 (Round 4 Progress Tracker)

| 编号 | 严重程度 | 模块 / 文件 | 问题描述 | 状态 |
| :--- | :--- | :--- | :--- | :--- |
| `BUG-R4-01` | **High (P1)** | `deploy/caddy/Caddyfile`<br>`deploy/production-preflight.test.mjs`<br>`services/api/internal/httpapi/middleware.go`<br>`services/paid-access/internal/httpapi/handler.go` | **Cloudflare 后的不同访客会共用限流额度**：`Caddyfile` 使用 `header_up X-Forwarded-For {remote_host}` 并无条件剥离 `CF-Connecting-IP`，当流量经由 Cloudflare 代理到达源站时，`{remote_host}` 为 Cloudflare 边缘节点 IP，导致同一边缘节点后的不同真实访客共享同一限流配额 | `[x]` |
| `BUG-R4-02` | **Medium (P2)** | `apps/admin/src/main.tsx` | **图片正则的指数回溯可长时间阻塞编辑器**：`markdownFragmentToEditorHtml` 的图片匹配正则 `/^!\[((?:\\.|(?!]\()[^])*)]\((\S+)\)$/` 中，`\\.` 与 `(?!]\()[^]` 均能匹配反斜杠 `\`，在连续反斜杠输入下产生 $O(\phi^n)$ 指数级回溯（ReDoS） | `[x]` |
| `BUG-R4-03` | **Medium (P2)** | `apps/admin/src/main.tsx` | **相邻图片被吞并为一个错误的图片地址**：同一段落内的相邻行内图片（如 `![a](/api/uploads/a.png)![b](/api/uploads/b.png)`）被块级图片正则中的 `(?!]\()[^]*` 与 `(\S+)` 跨越边界吞并成单张错误 URL 的图片 | `[x]` |

---

### `[x]` BUG-R4-01：Cloudflare 后的不同访客会共用限流额度 (`deploy/caddy/Caddyfile:40-42`)
- **严重程度**：High (P1)
- **位置**：
  - `deploy/caddy/Caddyfile:1-60, 114-121`
  - `deploy/production-preflight.test.mjs:209-212`
  - `services/api/internal/httpapi/middleware.go:98-184`
  - `services/paid-access/internal/httpapi/handler.go:542-629`
- **根因分析**：
  - 第三轮为阻断伪造 `CF-Connecting-IP` 绕过限流，在 `Caddyfile` 中保留了 `header_up X-Forwarded-For {remote_host}` 并新增 `header_up -CF-Connecting-IP`。
  - 但在生产环境中，正常访客流量经由 Cloudflare 边缘节点进入 Caddy，此时 `{remote_host}` 是 Cloudflare 边缘节点 IP，而真实访客 IP 位于 Cloudflare 注入的 `CF-Connecting-IP` 头中。直接将 `{remote_host}` 写入 `X-Forwarded-For` 会导致所有经过同一 Cloudflare 节点的不同访客被识别为同一个 IP，共用注册、登录、评论及福利申领限流额度。
- **修复方案**：
  1. 在 `deploy/caddy/Caddyfile` 顶部全局配置块 `{ servers { ... } }` 中配置 `trusted_proxies static <Cloudflare 官方 IPv4/IPv6 CIDR 列表>`、`trusted_proxies_strict` 以及 `client_ip_headers CF-Connecting-IP X-Forwarded-For`：
     - 当连接 Caddy 的直接对端（`{remote_host}`）属于受信任的 Cloudflare 边缘网段时，Caddy 从 `CF-Connecting-IP`（及回退的 `X-Forwarded-For`）提取真实访客 IP 作为 `{client_ip}`；
     - 当对端不属于 Cloudflare 网段（如攻击者直连源站试图伪造 `CF-Connecting-IP` 或 `X-Forwarded-For`）时，Caddy 拒绝信任任何客户端传入的代理头，`{client_ip}` 严格等于直连对端 `{remote_host}`。
  2. 将 `deploy/caddy/Caddyfile` 中所有 `reverse_proxy` 块的 `header_up X-Forwarded-For {remote_host}` 更新为 `header_up X-Forwarded-For {client_ip}`，继续配合 `header_up -CF-Connecting-IP` 与 `header_up -X-Real-IP` 向后端传递唯一经校验的真实访客 IP。
  3. 在 Go 后端 `realIP` (`services/api`) 与 `API.remoteIP` (`services/paid-access`) 中增加纵深防御：当 `X-Forwarded-For` 最右侧可信跳属于 Cloudflare 官方网段且请求携带合法 `CF-Connecting-IP`（或前一跳合法访客 IP）时提取该真实访客 IP，若最右侧跳不属于 Cloudflare 网段则严格忽略 `CF-Connecting-IP`。

---

### `[x]` BUG-R4-02 & BUG-R4-03：图片正则指数回溯（ReDoS）与相邻行内图片被吞并 (`apps/admin/src/main.tsx:2397-2401`)
- **严重程度**：Medium (P2)
- **位置**：
  - `apps/admin/src/main.tsx:2397-2412, 2672-2674, 2714-2734`
- **根因分析**：
  1. **指数回溯（ReDoS）**：第三轮将块级图片正则改为 `/^!\[((?:\\.|(?!]\()[^])*)]\((\S+)\)$/`。在重复分组 `(?:\\.|(?!]\()[^])*` 中，分支 1 `\\.` 与分支 2 `(?!]\()[^]` 的首字符集合存在交集（两者都能匹配反斜杠 `\`）。当输入包含长串连续反斜杠且末尾不匹配 `](`（如 `'![' + '\\\\'.repeat(35) + 'x'`）时，NFA 引擎在将连续反斜杠切分为双字符 `\\.` 或单字符 `[^]` 之间发生 $O(\phi^n)$ 指数级回溯，导致主线程卡死。
  2. **相邻图片吞并**：同一行包含两张相邻图片 `![a](/api/uploads/a.png)![b](/api/uploads/b.png)` 时，由于 `(?!]\()[^]` 允许跨越未转义的 `]`，且 URL 分组被放宽为 `(\S+)`（允许匹配 `)` 与 `![`），导致整行被错误匹配为单个块级图片（要么 `alt` 吞并首张图片 URL，要么 `url` 吞并 `/api/uploads/a.png)![b](/api/uploads/b.png`）。
- **修复方案**：
  1. 将块级图片正则修改为严格线性时间 $O(n)$、首字符完全互斥的写法：
     ```ts
     const image = line.match(/^!\[((?:[^\\\]]|\\.)*)]\(([^)\s]+)\)$/);
     ```
     - 在 `(?:[^\\\]]|\\.)*` 中，分支 1 `[^\\\]]` 明确排除 `\` 与 `]`，分支 2 `\\.` 必须以 `\` 开头，两个分支的首字符集合 **100% 互斥**，在任何输入下均无回溯分支点（严格 $O(n)$ 线性时间）；
     - 同时 `(?:[^\\\]]|\\.)*` 绝不跨越未转义的 `]`，且 URL 部分恢复为 `([^)\s]+)`（绝不跨越 `)` 或空白符）。
  2. 将块级附件正则同步收敛为首字符互斥的线性正则：
     ```ts
     const attachment = line.match(/^\[附件[:：]\s*((?:[^\\\]]|\\.)+?)]\(([^)\s]+)\)$/);
     ```
  3. 将 `formatInlineMarkdown` 与 `markdownImagesToPasteHtml` 中的图片匹配正则统一更新为 `/!\[((?:[^\\\]]|\\.)*)]\((https?:\/\/[^)\s]+|\/[^)\s]+)\)/g`。当一行存在相邻图片 `![a](/api/uploads/a.png)![b](/api/uploads/b.png)` 时，块级图片正则因首张图片的 `)` 不在行尾而立即以 $O(n)$ 失败，进而交由段落行内解析器 `formatInlineMarkdown` 准确渲染为两张独立的 `<img>`。

---

## 第四轮独立复审反馈修复验收结果 (Round 4 Verification Summary)

- **第三方独立复现脚本原位验证**：
  - `node C:/Temp/freedompost-frontend-independent-review/check-regex-redos.mjs`：`n = 20..40` 耗时稳定在 `0.033ms ~ 0.046ms`，严格 $O(n)$ 无回溯。
  - `npx vitest run --root C:/Temp/freedompost-frontend-independent-review --environment happy-dom`：`admin-adjacent-images.test.tsx`、`admin-image-alt.test.tsx`、`admin-save-race.test.tsx` **3/3 全部通过**。
- **Go 后端、前端与部署治理全量回归**：
  - `cd services/api && go vet ./... && go test ./...` — 全部通过（含 Cloudflare 可信网段访客提取与非可信源防伪造测试）。
  - `cd services/paid-access && go vet ./... && go test ./...` — 全部通过（含 `TestCloudflareClientQuotasRemainSeparate` 独立访客配额隔离测试）。
  - `npm run typecheck && npm test --workspaces --if-present && npm run build` — 16 个测试文件共 81 项测试全部通过，生产构建通过。
  - `deploy/*.test.mjs` — 31 项部署与预检测试全部通过（含 Caddy `trusted_proxies static`、`client_ip_headers CF-Connecting-IP X-Forwarded-For` 与 `header_up X-Forwarded-For {client_ip}` 校验）。

