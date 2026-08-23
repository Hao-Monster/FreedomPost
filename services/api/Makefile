# FreedomPost Go API — 本地开发 Makefile
# 运行环境：WSL2 (Ubuntu) + Docker Desktop
# 使用方式：在 WSL 中 cd /mnt/e/CodeWorkstation/freedompost/services/api && make help

.PHONY: help dev test-infra-up test-infra-down test-unit test-integration test-all build fmt vet clean

# ─── 颜色 ─────────────────────────────────────────────────────────────────────
GREEN  := \033[0;32m
YELLOW := \033[1;33m
RED    := \033[0;31m
NC     := \033[0m

help: ## 显示帮助
	@echo ""
	@echo "$(GREEN)FreedomPost Go API 本地开发命令$(NC)"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "} {printf "  $(YELLOW)%-25s$(NC) %s\n", $$1, $$2}'
	@echo ""

# ─── 基础设施 ──────────────────────────────────────────────────────────────────

test-infra-up: ## 启动本地测试 Postgres + Redis
	@echo "$(GREEN)启动测试基础设施...$(NC)"
	docker compose -f docker-compose.test.yml up -d
	@echo "$(YELLOW)等待 Postgres 就绪...$(NC)"
	@until docker exec fp-postgres-test pg_isready -U freedompost -d freedompost_test -q; do sleep 1; done
	@echo "$(GREEN)✅ Postgres 就绪$(NC)"
	@until docker exec fp-redis-test redis-cli ping | grep -q PONG; do sleep 1; done
	@echo "$(GREEN)✅ Redis 就绪$(NC)"

test-infra-down: ## 停止并清理测试基础设施
	docker compose -f docker-compose.test.yml down -v
	@echo "$(GREEN)✅ 测试环境已清理$(NC)"

test-infra-logs: ## 查看测试基础设施日志
	docker compose -f docker-compose.test.yml logs -f

# ─── 编译 & 静态检查 ──────────────────────────────────────────────────────────

build: ## 编译二进制（输出到 bin/fp-api）
	@mkdir -p bin
	CGO_ENABLED=0 go build \
		-ldflags "-s -w -X main.version=local-dev" \
		-o bin/fp-api \
		./cmd/server
	@echo "$(GREEN)✅ 编译完成：bin/fp-api$(NC)"

fmt: ## 格式化 Go 代码
	gofmt -w -l .
	@echo "$(GREEN)✅ 格式化完成$(NC)"

vet: ## 静态分析
	go vet ./...
	@echo "$(GREEN)✅ go vet 通过$(NC)"

# ─── 单元测试 ─────────────────────────────────────────────────────────────────

test-unit: ## 运行单元测试（无需外部依赖）
	@echo "$(GREEN)运行单元测试...$(NC)"
	go test -v -count=1 -timeout=30s \
		./internal/domain/... \
		./internal/ratelimit/... \
		./internal/renderer/... \
		./internal/searchindex/... \
		./internal/security/... \
		./internal/viewcount/...
	@echo "$(GREEN)✅ 单元测试完成$(NC)"

# ─── 集成测试 ─────────────────────────────────────────────────────────────────

dev: build ## 启动 Go API（需先运行 make test-infra-up）
	@echo "$(GREEN)启动 Go API (port 3001)...$(NC)"
	@echo "$(YELLOW)使用配置：.env.test$(NC)"
	./bin/fp-api

dev-run: build ## 后台启动 Go API，立即返回
	@echo "$(GREEN)后台启动 Go API...$(NC)"
	@set -a && . ./.env.test && set +a && ./bin/fp-api &
	@echo "$$!" > /tmp/fp-api.pid
	@echo "$(YELLOW)等待服务就绪...$(NC)"
	@sleep 2
	@until curl -sf http://localhost:3001/health > /dev/null; do sleep 1; echo "等待..."; done
	@echo "$(GREEN)✅ Go API 已启动 (PID: $$(cat /tmp/fp-api.pid))$(NC)"

dev-stop: ## 停止后台 Go API
	@if [ -f /tmp/fp-api.pid ]; then \
		kill $$(cat /tmp/fp-api.pid) 2>/dev/null && echo "$(GREEN)✅ Go API 已停止$(NC)" || echo "$(YELLOW)进程不存在$(NC)"; \
		rm -f /tmp/fp-api.pid; \
	else \
		echo "$(YELLOW)找不到 PID 文件$(NC)"; \
	fi

test-integration: dev-run ## 运行集成测试（自动启动 API）
	@echo "$(GREEN)运行集成测试...$(NC)"
	@GO_API_URL=http://localhost:3001 \
	 ADMIN_USERNAME=$$(grep ADMIN_USERNAME .env.test | cut -d= -f2) \
	 ADMIN_PASSWORD=$$(grep ADMIN_PASSWORD .env.test | cut -d= -f2 | head -1) \
	 node test/integration.mjs
	@$(MAKE) dev-stop

# ─── 全量测试（单元 + 集成）──────────────────────────────────────────────────

test-all: test-infra-up test-unit test-integration ## 全量测试（单元 + 集成）
	@echo ""
	@echo "$(GREEN)══════════════════════════════════════$(NC)"
	@echo "$(GREEN)  🎉 全量测试完成！可进入下一阶段  $(NC)"
	@echo "$(GREEN)══════════════════════════════════════$(NC)"

# ─── 清理 ─────────────────────────────────────────────────────────────────────

clean: dev-stop test-infra-down ## 清理所有（停止服务 + 清理容器）
	rm -rf bin/ /tmp/fp-test-uploads
	@echo "$(GREEN)✅ 清理完成$(NC)"

# ─── 工具命令 ─────────────────────────────────────────────────────────────────

db-shell: ## 连接测试数据库（交互式 psql）
	docker exec -it fp-postgres-test psql -U freedompost -d freedompost_test

redis-shell: ## 连接测试 Redis（交互式）
	docker exec -it fp-redis-test redis-cli

db-reset: ## 重置测试数据库（危险！会清空所有数据）
	@echo "$(RED)警告：将清空 freedompost_test 数据库！$(NC)"
	@read -p "确认请输入 YES: " confirm && [ "$$confirm" = "YES" ] || exit 1
	docker compose -f docker-compose.test.yml down -v
	docker compose -f docker-compose.test.yml up -d
	@until docker exec fp-postgres-test pg_isready -U freedompost -q; do sleep 1; done
	@echo "$(GREEN)✅ 数据库已重置$(NC)"

hash-password: ## 生成 Admin 密码 hash（make hash-password PASS=your-password）
	@[ -n "$(PASS)" ] || { echo "$(RED)使用方式: make hash-password PASS=your-password$(NC)"; exit 1; }
	@go run -ldflags "-X main.hashOnly=true" ./cmd/server -hash-password "$(PASS)"
