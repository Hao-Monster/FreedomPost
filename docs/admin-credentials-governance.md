# 管理员凭据治理

## 唯一事实来源

- 生产环境只保留 GitHub Actions Secret `ADMIN_PASSWORD_HASH`。
- GitHub Secret 是期望配置，但更新 Secret 不等于授权普通代码发布更换密码。普通发布必须与现网凭据一致；有差异就停止。
- 该值必须是未加引号、未做 Compose 转义的 60 字符 `$2a$` 或 `$2b$` bcrypt hash。
- 服务器 `.env` 只允许存在 `ADMIN_PASSWORD_HASH`；`ADMIN_PASSWORD` 和 `HASH_PASSWORD` 是历史别名，必须为空或不存在。
- 数据库、文章内容、对象存储及其他应用密钥不属于本治理范围。

## 发布门禁

1. `production-preflight.mjs` 检查 hash 格式，并拒绝 `ADMIN_PASSWORD` / `HASH_PASSWORD`。
2. 普通发布在修改配置前，比较 GitHub 输入、现有 `.env` 与 API 容器的管理员凭据；不一致则拒绝发布，保留运行服务。首次安装也需要明确选择轮换模式。
3. `remote-deploy.sh` 保留现网用户名，删除明文别名，以同目录临时文件和原子替换更新 `.env`，保留 Compose 的美元符号转义和 `0600` 权限。
4. 服务替换前先核对候选容器经 Compose 解析后的凭据；替换后再次核对实际 API 容器。值不一致时不得写发布成功记录。记录只包含发布 SHA、轮换模式和回滚路径，不包含 hash。
5. Go API 启动时仍校验 bcrypt 格式；失败则拒绝启动，不回退到明文。
6. 健康检查及未登录返回 401 都不能证明密码可用。轮换后必须在 `admin-freedompost.thinderbox.uk` 完成 Cloudflare Access 和实际管理员登录。

## 轮换流程

1. 先获得凭据变更授权，在受控环境隐藏输入新密码两次，生成 bcrypt hash 并用同一输入本地验证；不在命令行参数、日志、Issue、提交或聊天中记录明文密码。
2. 只更新 `ADMIN_PASSWORD_HASH` Secret，确认工作流没有引用旧别名。
3. 从受保护 `main` 手动部署经过审查的完整 SHA，并明确选择 `rotate_admin_password=true`；普通发布保持默认 `false`。密码和 hash 不作为 workflow 输入。
4. 等待实际部署完成，再执行上述配置及登录验收；仅提交 workflow 不代表密码已在线上生效。

## 失败与回滚

- Secret 更新或部署请求超时，先核对实际状态，不盲目重复轮换或部署。部署失败可能发生在服务替换之后，不能假定线上仍是旧配置。
- 决定恢复旧凭据时，从部署前的受限权限备份中取得未加引号、未做 Compose 转义的合法 bcrypt hash，恢复到 GitHub Secret。若运行容器已使用新凭据，须经正式部署流程选择 `rotate_admin_password=true` 恢复旧凭据；若尚未替换服务，仅恢复 Secret 并核对一致性即可。若失败已造成 `.env` 与容器不同，先把 `.env` 的管理员凭据恢复为当前容器使用的值，再重试正式部署。只恢复服务器 `.env` 会留下两端漂移；不得恢复明文变量。
- 若受保护 `main` 已变化，先审查新的部署目标；不要绕过完整 SHA 门禁。普通发布会拦截遗留漂移，不会把新的 Secret 再次悄悄覆盖到已恢复的服务。

## 清理与审计

- 清理前保存权限为 `0700/0600` 的时间戳备份，仅用于回滚。
- 清理后审计变量名、hash 长度/前缀、文件权限、容器环境和登录结果；审计输出不得包含凭据值。
- 删除 GitHub 上的 `ADMIN_PASSWORD`、`HASH_PASSWORD` Secret 后，不得重新创建同名 Secret。
