# 商城订单自动客服交付 v2：测试与验收记录

日期：2026-10-12。范围：已确认的生成单号后自动打开同访客客服、发送原订单、持久幂等和失败恢复。本轮本地开发、目标验证和独立复审完成；当前记录的是本地候选版本，不代表生产已经上线。

## 环境与隔离

- FreedomPost：`codex/development`，基线 `6d2f4c215c273a1a9762bdb32d6b70dfba1550aa`；调查时远端 main 为 `66169e8e796212c252ddc40dac532108a5f21425`。
- 自有 Chatwoot：固定上游 4.17.1 / `b354a9550e1fb59fa537a9c384232cb076213e72`，官方 runtime digest `sha256:0dcaaacc41ba5219b48af80b236f7707dbd5d58228320950af71a4309c349a7a`；仅加载经过文件哈希核验的 9 个后端增量及候选前端资产。
- Windows + WSL Debian，Go 1.26.9，PostgreSQL 17.11、隔离 Redis、官方 Ruby 3.4.4 与锁定 Rails/gems。没有启动 Docker、安装系统 CA、修改系统代理或关闭 TLS 校验。
- 数据库仅绑定 loopback，使用 `fp_order_*qa`、`fp_paid_handoff_qa`、`fp_chatwoot_order_*qa`；浏览器商品、推荐人、账号均为 QA 数据。
- 本地 Rails 捕获异步任务，邮件、Webhook、机器人和外部集成不执行。仅受控执行真实 `conversation.created` 自动化监听器及指向本机 Redis/浏览器的 `ActionCableBroadcastJob`，以检查原生二维码附件欢迎与实时显示；这不等于验证生产队列调度。
- API TLS 使用本任务证书和进程级信任；公开网站、管理服务和生产数据库未改动。浏览器只操作独立的本地 QA 标签页。

## 验收与风险矩阵

| 验收标准或风险 | 测试入口 | 层级 | 结果与边界 |
|---|---|---|---|
| 正常路径不自动复制、不在发送后切换身份 | `market-order-regression.test.ts`、`order-handoff-portal.test.ts` | 源码与真实模板 DOM | PASS；浏览器证据另列 |
| owner 建立、CSRF、异主访问、过期凭据、输入冲突 | `httpapi/order_handoff*_test.go` | HTTP handler + 真实 PG | PASS |
| 相同请求并发、订单响应丢失、未知订单 ID 恢复 | `repository/order_handoff_integration_test.go` | 真实 PG 唯一约束/事务 | PASS |
| CAS 访客绑定、lease 过期、旧尝试不能覆盖新回执 | 同上 | 真实 PG 并发 | PASS |
| 客服超时、错误回执、重定向和身份冲突 | `chatwoot/handoff_test.go` | 真实 TLS HTTP 边界替身 | PASS；不是 Rails 端到端 |
| owner 丢失不重建、重试原单、sent 只读恢复 | `order-handoff.test.ts` | 状态机单元 | PASS |
| 用户关闭期间取消 GET/POST/退避，迟到响应不重开 | `order-handoff.test.ts`、`order-handoff-portal.test.ts` | 事件/模板/状态机 | PASS，独立复跑 28/28 |
| SDK 来源、请求 ID、generation 与绑定校验 | 固定上游 SDK/App 测试 | iframe/SDK DOM 与 store | PASS，最终 185/185 |
| 接收端并发、响应丢失、跨访客/Inbox 隔离 | `deploy/order-handoff/integration_test.rb` | 真实 Rails 路由/模型/PG | PASS |
| 历史订单回执读取不泄露 private/deleted/unmapped 消息 | 同上 + App 测试 | 真实 Rails + UI 单元 | PASS；分页独立审查另列 |
| 事务失败不产生幽灵消息、迁移重复执行 | 同上 | 真实 Rails/PG | PASS |
| 首条订单触发真实二维码附件、幂等重放不再发欢迎 | 同上 | 原生自动化监听器 + ActiveStorage | PASS；不替代生产 worker 验证 |
| 新网站不得先于支持协议的客服发布 | `chatwoot-handoff-preflight.test.mjs` | 发布顺序门禁 | PASS；发送时仍需认证 capability |
| 新访客点击、消息可见、二维码、刷新、草稿、390px | 本地真实浏览器 | 跨系统端到端 | 已执行，具体场景与工具限制见下表 |

## Red → Green

1. 商城原实现回归：修复前 2/2 FAIL（679 ms），命中自动剪贴板与状态位于关闭弹窗内；修复后相关前端套件通过。
2. 订单所有者接口：修复前 2 个目标用例 FAIL；实现 owner 先确认、CSRF、持久请求映射后通过。
3. 发布 capability 门禁：修复前 0 PASS / 4 FAIL（346 ms）；修复后 11/11 PASS（355 ms）。
4. 真实 PG 单连接池复现：事务内再次借连接死锁；改为复用当前事务后同一场景通过。没有通过增加池大小隐藏问题。
5. 主动关闭客服：新增测试修复前 24 PASS / 4 FAIL（2.68 s）；修复后 28/28 PASS（2.17 s）。独立审查再次执行 28/28 PASS（1.98 s）。
6. 历史订单回执：原只查询 latest conversation 的实现 24 PASS / 1 FAIL；认证读取原映射回执后 25/25 PASS。
7. 独立审查另发现旧回执注入主 store 会污染历史分页游标：跨会话和同会话尚未加载的较旧消息均受影响。Red 为 25 PASS / 2 FAIL；修复采用独立只读回执展示，不改变主时间线游标，Green 27/27。独立复跑 27/27 PASS（12.75 s），验证旧回执 73 展示后仍从 `before=181` 实际加载 170；不跳过未加载历史。

## 已执行的命令与结果

以下计数按各测试工具实际口径；Go 顶层用例与包含子用例计数不重复相加。

| 命令/位置 | 结果 | exit | 通过 / 失败 / 跳过 | 耗时与说明 |
|---|---|---:|---|---|
| `npm exec -w @freedompost/public-reader -- vitest run src/lib/order-handoff.test.ts src/lib/order-handoff-portal.test.ts` | PASS | 0 | 28 / 0 / 0 | 独立复跑 1.98 s |
| public-reader 全部 `vitest run src` | PASS | 0 | 97 / 0 / 0，20 文件 | 关闭问题修复后 3.99 s |
| public-reader `astro check` | PASS | 0 | 67 文件，0 error/warning/hint | 关闭问题修复后 |
| public-reader `astro build` | PASS | 0 | 14 页面 | 2.70 s |
| 根目录 `npm run check` 最终全量 | PASS | 0 | 203 JS 业务测试 + 31 发布测试，0 FAIL/SKIP | 所有 workspace 类型检查、公开站 14 页、admin 构建通过；公开站最终测试 4.56 s、构建 2.28 s |
| API `go test -race -coverprofile=C:/Temp/fp-order-api-coverage.out -covermode=atomic -json ./...` | PASS | 0 | 73 顶层 / 191 含子例，0 FAIL/SKIP | 155.59 s；真实 PG |
| API `go vet ./...`、`go build ./...` | PASS | 0 | 不适用 | Go 1.26.9 |
| API `govulncheck` v1.7.0 | PASS | 0 | 0 可达、0 已导入包漏洞 | 未调用模块仍提示 `GO-2026-5932`，不能称所有依赖无漏洞 |
| paid-access 升级前真实数据库测试 | PASS | 0 | 23 / 0 / 0 | 23.26 s |
| paid-access 升级前安全扫描 | FAIL | 非零 | 10 可达、2 已导入包漏洞 | 触发最小工具链/依赖安全升级 |
| paid-access 升级后真实数据库测试 | PASS | 0 | 23 / 0 / 0 | 70.93 s |
| paid-access `go vet`、Windows/Linux 构建 | PASS | 0 | 不适用 | 不修改支付业务逻辑 |
| paid-access 升级后 `govulncheck` | PASS | 0 | 0 可达、0 已导入包漏洞 | 仍有 21 个未调用模块提示 |
| 真实 PG 网站迁移及再次执行迁移 | PASS | 0 | 0014 应用、14 个 already-applied | 不删除旧订单或旧数据 |
| `node --test deploy/chatwoot-handoff-preflight.test.mjs` | PASS | 0 | 11 / 0 / 0 | 355 ms |
| Chatwoot 发布模拟（WSL Debian） | PASS | 0 | 41 / 0 / 0 | 35.01 s；新增发布门禁后 |
| Chatwoot backend patch/allowlist 测试 | PASS | 0 | 9 / 0 / 0 | 固定上游哈希与严格文件列表 |
| 原 direct-conversation patch 测试 | PASS | 0 | 5 / 0 / 0 | 保留直接会话行为 |
| Chatwoot SDK/widget/store 最终回归 | PASS | 0 | 185 / 0 / 0，18 文件 | 20.23 s；Node 24、pnpm 10.2.0、TZ=UTC |
| Chatwoot 最终 App 显示文案回归 | PASS | 0 | 27 / 0 / 0 | 10.99 s；独立复跑 12.75 s |
| Chatwoot 修改的 11 个 JS/Vue 文件 ESLint | PASS | 0 | 0 error/warning | `--max-warnings 0` |
| Chatwoot Vite、SDK 生产资产构建 | PASS | 0 | 5,075 modules；231 Vite entries、SDK plain/gzip/Brotli 核验 | 1m32s / 6.16s；本地资产，不是 Docker release build |
| 原始 archive 两阶段 patch check/apply + 哈希复算 | PASS | 0 | 21 + 23 源文件；9 runtime 文件；0 mismatch | 独立提取树与实际受测源码一致 |
| 真实固定 Rails/PG 订单集成 | PASS | 0 | 16 场景、99 断言、0 FAIL/SKIP | 2.57 s；包含二维码与历史授权 |
| `node --test deploy/qr-welcome/ssh-fingerprint.test.mjs` | PASS | 0 | 5 / 0 / 0 | 独立复跑 477 ms |
| 本轮网站暂存 diff 的 Gitleaks stdin | REVIEWED | 1 | 5 个检测项，0 真实凭据 | 独立复核全部为测试固定 UUID requestKey / Idempotency-Key；保留原扫描，不改规则 |
| 本轮客服暂存 diff 的 Gitleaks stdin | PASS | 0 | 0 检测项 | 脱敏输出，773 ms |
| 网站 `git diff --cached --check` | PASS | 0 | 0 whitespace 问题 | 仅任务文件暂存 |
| 客服暂存 whitespace 检查 | REVIEWED | 2 | 16 个格式提示 | 全部为 unified diff 必需的单空格上下文；除此补丁文件外检查 exit 0，精确 applier 及源码哈希另行通过 |

完整网站 `npm run check` 和最终客服资产构建已通过。真实浏览器证据见下表；未运行的项不能从表中其他 PASS 推定通过。

## 覆盖率

- Go API statement coverage：全包 33.3%；本次新增 HTTP 78.3%、repository 78.3%、Chatwoot client 85.5%。这是 statement coverage，不是分支覆盖率。
- paid-access statement coverage：51.5%。
- 客服两个 handoff 模块：行覆盖率 91.41%，分支覆盖率 81.93%（上游 Vitest v8）；不是整个 widget 的覆盖率。未覆盖部分包括防御性 malformed/unavailable/cancel 分支；没有把未运行的浏览器故障路径计入覆盖率。
- 没有降低既有门禁、添加 skip/only 或弱化断言来换取通过。

## 失败、修复和范围说明

- Windows 上 Chatwoot 发布模拟曾因 Linux 路径语义和固定子进程时限失败；用同一断言在 WSL Debian 执行通过，没有删除断言。
- 本地官方 runtime 使用 proot，`bundle exec` 可执行查找存在环境兼容问题；改用 `ruby bin/rails`，Rails boot 仍执行 `bundler/setup`，并实际核对锁定依赖。未将该环境问题解释成产品通过。
- 为通过项目共享安全门禁，仅升级 API 和 paid-access 的 Go 工具链及被报告的依赖闭包；没有改动支付、价格、付款或密码散列业务。
- 单元/HTTP mock 只覆盖外部边界；事务、约束、路由、消息持久化与附件均另有真实数据库/框架证据。
- 独立审查未发现剩余 Critical/High 阻断项。已显示的跨历史会话回执不保证实时接收删除事件；刷新或再次认证读取会拒绝已删除/私有消息，不能宣传为跨历史会话实时撤回保证。

最终本地客服资产 SHA256：SDK `4caef9736aaeafd868693b9b05de626c77c24f42f67c916f0f0ce33f988c248`；Vite manifest `48a051a4b80b0c782bb3ffad1ce9b5deaec21c4346a8a61100bb5a9344831ab3`；handoff patch `c0145e007e826f0ac1e429a61bcadbe8da702f0a45be8d3f0c2cc64d5ffa18cb`。

## 真实本地浏览器记录

浏览器实际访问 `127.0.0.1` QA 商城，生成真实隔离订单，通过 Go → TLS → 固定 Rails → PostgreSQL 保存，再读取真实消息。没有 mock 订单或消息回执。QA HTML 设置 `Permissions-Policy: clipboard-write=()`；成功链路发生在该限制下。未安装到剪贴板调用计数器，因此“没有自动调用”仍由源码和前端回归断言证明，不能伪称浏览器计数为零。

| 场景 | 实际可观察证据 | 结果 |
|---|---|---|
| 首笔新访客订单 | 点击生成单号后自动展开客服；`FP55E3F084` 原文可见；数据库一个订单、message 1 / conversation 1，attempts 1 | PASS |
| 首单二维码、刷新再查看 | message 2 为唯一真实附件欢迎；刷新/打开客服后订单与图片同时可见；图片 complete=true、1126×1627；没有新增消息 | PASS；初次 QA 适配器捕获了广播，补启本机广播后继续验收 |
| 第二笔独立订单与草稿 | `FP437DFE10` → message 3 / 同 conversation 1；输入中的 `QA-保留草稿-20261012` 未丢失；原历史存在，二维码仍只有一条 | PASS |
| 客服已保存但响应丢失 | 代理在真实 receiver HTTP 200 后丢弃回包；`FP307A537C` attempts 2，最终只对应 message 4，界面确认原文可见 | PASS |
| 延迟响应的常规交付 | `FP29986006` → message 5；真实等待响应后成功，未复制或重建 | PASS |
| 发送中主动关闭、迟到响应 | `FP43035885` 从“正在发送”立即转“客服交接已暂停，原单保留”；10 秒延迟过去后仍关闭、仍暂停；没有强制重开 | PASS |
| 关闭后恢复原单 | 点击“重试发送此订单”，仅 GET session、POST recover、GET support-delivery；无 POST orders / POST support-delivery；原 message 6 可见，attempts 仍为 1 | PASS |
| 已落库但未知订单号时刷新 | 代理记录真实订单 POST 201 后延迟回包；刷新前界面仅有“待核对的下单请求”，无订单 ID；刷新自动恢复 `FP0B75E829` → message 7，未新建额外订单 | PASS |
| 390×844 视口 | 原订单消息、客服标题和输入框实际可见，输入框矩形在视口内；截图已保存，验收后恢复原视口 | PASS，非物理设备验收 |
| 最终数据对账 | 6 次明确购买动作 → 6 张订单、6 条交付映射、6 条 incoming 订单消息、1 条二维码附件；均在会话 1 | PASS |
| 原定价、推荐与付款状态 | 6 单均 1234 分、同 QA 推荐人；均 pending / 未完成；商品 stock 1000、sold_count 0 | PASS，仅本次未付款路径 |
| 最终控制台 | QA 静态资源修正及刷新后，记录范围内无新的 warn/error；注入故障在代理侧有真实状态记录 | PASS，不隐藏初始环境错误 |

本地运行器曾对 SDK、音频与机器人头像返回静态 404；QA 代理现只针对固定目录/精确请求路径返回真实 runtime 文件，并核对哈希。此兼容处理不修改生产源码，不将 HTML 错误页冒充资源。浏览器 iframe 内按钮定位曾遇工具焦点/坐标错误，改用父页面真实客服开关完成关闭；不据此声称 iframe 内键盘完整验收。

截图：`C:/Temp/fp-order-browser-first-order.jpg`（原订单与真实二维码同屏，刷新重开后）；`C:/Temp/fp-order-browser-390px.jpg`（390px 实际消息与输入框）。这些是本地 QA，不是生产截图。

## 验收后清理

- 本地 QA 标签页已关闭，视口已恢复，用户原有生产标签页未操作。
- 代理、Go API、Rails、Redis 与 PostgreSQL 均经 PID、命令和目录核验后精确停止；Windows/WSL 两侧确认本任务的 7 个端口均不再监听，没有停止整个 WSL 或无关服务。
- 未删除测试记录、源码、附件或 runtime。PostgreSQL 数据目录保留；Redis 以 `SHUTDOWN SAVE` 保存，快照校验通过，仅含 QA DB14/15 共 22 个 key。
- 保留位置及停止证据记录在本机 `C:/Temp/fp-order-qa-stack.md`；任务证书私钥和 QA 配置不进入提交或发布产物。

## 发布状态和未验证边界

- 未推送、未创建本轮 PR、未合并、未部署。生产发布和生产真实订单/消息验收需另有明确授权。
- 本地已实际验证订单原文在 widget 可见，没有把 API 201、消息回执、构建或单元测试直接称为用户可见成功。
- 浏览器中 SDK 延迟超过 5 秒、完全断网、owner Cookie 手动清除、跨会话实时删除、完整键盘/iframe 内关闭按钮及人工消息发送：NOT RUN。相关身份、延迟、取消、历史权限、分页和普通消息兼容另有目标单元/框架集成证据；浏览器工具的 iframe 控件定位限制不能假称通过。
- 生产真实 worker/重启、生产数据库迁移和回滚执行：NOT RUN。发布流程必须先客服、后网站；回滚保留幂等映射与历史数据。
- 真人微信设备扫码、所有设备/浏览器、长时间负载、页面关闭后的后台交付：NOT RUN／不在本次已确认范围。

发布仍需远端 CI 完整容器构建、受保护 main 的完整 SHA、迁移前核验及生产受控 QA；本地通过不跳过这些门禁。目标在生产发布授权与验收完成前保持未完成状态。
