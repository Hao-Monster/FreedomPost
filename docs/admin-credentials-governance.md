# 管理员凭据治理

## 唯一事实来源

- 生产环境只保留 GitHub Actions Secret `ADMIN_PASSWORD_HASH`。
- 该值必须是未加引号、未做 Compose 转义的 60 字符 `$2a$` 或 `$2b$` bcrypt hash。
- 服务器 `.env` 只允许存在 `ADMIN_PASSWORD_HASH`；`ADMIN_PASSWORD` 和 `HASH_PASSWORD` 是历史别名，必须为空或不存在。
- 数据库、文章内容、对象存储及其他应用密钥不属于本治理范围。

## 发布门禁

1. `production-preflight.mjs` 检查 hash 格式，并拒绝 `ADMIN_PASSWORD` / `HASH_PASSWORD`。
2. Go API 在生产启动时再次校验 bcrypt hash；失败则拒绝启动，不回退到明文。
3. `remote-deploy.sh` 写入 hash 前删除服务器上的两个历史别名，写入后检查它们确实不存在。
4. 部署后必须检查 API healthz、`/api/admin/session` 未登录返回 401，以及使用管理员凭据完成一次真实登录。

## 轮换流程

1. 在受控环境生成新的 bcrypt hash，不在日志、Issue、提交或聊天中记录明文密码。
2. 只更新 `ADMIN_PASSWORD_HASH` Secret，确认工作流没有引用旧别名。
3. 运行生产 preflight，部署并执行上述 smoke test。
4. 失败时使用部署前保存的服务器 `.env` 备份回滚；不得恢复明文变量。

## 清理与审计

- 清理前保存权限为 `0700/0600` 的时间戳备份，仅用于回滚。
- 清理后审计变量名、hash 长度/前缀、文件权限、容器环境和登录结果；审计输出不得包含凭据值。
- 删除 GitHub 上的 `ADMIN_PASSWORD`、`HASH_PASSWORD` Secret 后，不得重新创建同名 Secret。
