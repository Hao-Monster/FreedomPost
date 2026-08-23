# FreedomPost Go API

高性能 Go 重构版 API 服务，替换 `apps/api`（TypeScript/Node.js）。  
与现有 PostgreSQL schema、`.env` 配置、`paid-access` 服务完全兼容。

---

## 快速开始（本地 WSL + Docker）

### 前置要求

- WSL2（Ubuntu 22.04+）
- Docker Desktop（已启用 WSL2 integration）
- Go 1.25+（`go version`）
- Node.js 20+（仅集成测试脚本需要）

### 一键全量测试

```bash
# 在 WSL 中执行
cd /mnt/e/CodeWorkstation/freedompost/services/api

# 启动 Postgres + Redis → 编译 → 单元测试 → 集成测试
make test-all
```

### 分步操作

```bash
# 1. 启动测试基础设施
make test-infra-up

# 2. 单元测试（无需 DB/Redis）
make test-unit

# 3. 编译
make build

# 4. 后台启动 API + 运行集成测试
make test-integration

# 5. 清理
make clean
```

---

## 目录结构

```
services/api/
├── cmd/server/main.go          # 入口（-hash-password / -health-check flags）
├── internal/
│   ├── config/                 # .env 读取（兼容现有所有变量）
│   ├── domain/                 # 领域模型 + 接口（零外部依赖）
│   ├── repository/             # PostgreSQL 实现（pgxpool，原生 SQL）
│   ├── security/               # HMAC-SHA256、Token、哈希工具
│   ├── renderer/               # Markdown → HTML（goldmark + bluemonday）
│   ├── ratelimit/              # Redis 固定窗口 + 内存降级
│   ├── session/                # Redis 会话（滑动 TTL）
│   ├── viewcount/              # 异步浏览量缓冲（Redis INCR → DB）
│   ├── searchindex/            # 搜索索引内存缓存
│   ├── paidaccess/             # HMAC 签名内部客户端
│   ├── storage/                # 多存储适配器（local/OSS/R2）
│   └── httpapi/                # HTTP 路由 + 所有 handler
├── test/
│   └── integration.mjs         # 集成测试脚本
├── docker-compose.test.yml     # 本地测试基础设施
├── .env.test                   # 测试环境配置
├── Makefile                    # 开发命令集合
└── Dockerfile                  # 生产镜像（distroless nonroot）
```

---

## 环境变量

Go API 读取与 TypeScript 版本**完全相同**的 `.env` 变量，额外新增：

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `ADMIN_PASSWORD_HASH` | bcrypt hash（cost=12），生产必填 | 空（回退明文） |
| `VIEW_BUFFER_FLUSH_INTERVAL` | 浏览量刷新间隔 | `30s` |
| `GRAY_RELEASE_PERCENT` | 灰度比例 0-100（Caddy 读取） | `0` |
| `R2_ACCOUNT_ID` | Cloudflare R2 账户 ID | — |
| `R2_BUCKET` | R2 bucket 名称 | — |
| `R2_ACCESS_KEY_ID` | R2 访问密钥 | — |
| `R2_SECRET_ACCESS_KEY` | R2 Secret Key | — |

### 生成 Admin 密码 Hash

```bash
./bin/fp-api -hash-password "your-secure-password"
# 输出：$2a$12$...  → 写入 ADMIN_PASSWORD_HASH=
```

---

## API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 存活检查 |
| `/health/ready` | GET | 就绪检查（含 DB/Redis 状态）|
| `/api/posts` | GET | 公开文章列表 |
| `/api/posts/:slug` | GET | 文章详情 |
| `/api/posts/:slug/view` | POST | 记录浏览量（去重）|
| `/api/posts/:slug/comments` | GET/POST | 评论列表/创建 |
| `/api/search-index` | GET | 搜索索引（FlexSearch 格式）|
| `/api/products` | GET | 公开商品列表 |
| `/api/affiliate/access` | POST | 推广员登录/注册 |
| `/api/affiliate/dashboard` | GET | 推广员看板 |
| `/api/affiliate/catalog` | GET | 推广员商品目录（含佣金）|
| `/api/orders` | POST | 创建推广订单 |
| `/api/admin/*` | * | 管理员 API（需 cookie 认证）|

---

## 灰度切流

通过 `.env` 中 `GRAY_RELEASE_PERCENT` 控制（Caddy 读取）：

```
0   → 100% TypeScript API（默认，Go API 运行但不接流量）
10  → 10% 流量切到 Go API（灰度初期）
50  → 50% 分流（稳定性验证）
100 → 全量 Go API
```

---

## 性能对比目标

| 指标 | TypeScript | Go（目标）|
|------|-----------|----------|
| `GET /api/posts` p99 | ~120ms | < 50ms |
| `GET /api/search-index`（缓存）| ~15ms | < 5ms |
| `POST /api/posts/:slug/view` | ~80ms | < 10ms |
| 内存占用（空载）| ~150MB | < 30MB |
| 容器启动时间 | ~3s | < 0.5s |
