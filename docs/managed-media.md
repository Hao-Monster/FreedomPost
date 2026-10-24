# 受保护的文章附件

文章及评论的新附件存入独立私有存储，通过 `/api/post-media/{postID}/{attachmentID}` 读取。每次 GET、HEAD、Range 请求都会检查附件引用和文章当前权限：公开文章允许匿名读取，私密文章仅管理员可读，付费文章复用阅读器购买校验。购买校验不可用时拒绝读取。返回 `private, no-store`，不生成可绕过文章权限的对象直链。

上传先记录 `uploading` 元数据，再写对象并置为 `ready`。未提交的附件有期限和一次性 claim，只允许绑定上传时的文章；管理员可以预览同文章的未过期附件。正文可复用已提交的正文附件，保存时为目标文章建立独立引用并改写上下文路径。迁移时已经被正文引用的评论附件可以继续在这些文章中保存，不允许通过正文 claim 认领未提交的评论附件或新增其他文章引用。取消一个引用不会取消另一文章的合法公开引用。已经被公开下载的字节无法因另一篇文章转私密而收回。

商品和工具的公开封面仍使用原公开上传接口。文章编辑器的文件、图片、剪贴板与远程图片转存全部使用文章接口。评论上传的额度、claim 和过期清理由同一附件元数据管理。

## 私有存储配置门禁

- Local：`MANAGED_LOCAL_ROOT`；默认是原 `LOCAL_STORAGE_ROOT` 加 `-private`。两个目录解析真实路径后必须互不包含；私有目录须持久化，不能映射到静态服务器的公开根目录。
- R2：`MANAGED_R2_BUCKET`、`MANAGED_R2_PREFIX`（默认 `media`）。私有桶必须与原 `R2_BUCKET` 不同，沿用现有 R2 服务端凭据/endpoint；关闭该桶的 r2.dev、公开域名及任何公开访问策略。
- OSS：`MANAGED_OSS_BUCKET`、`MANAGED_OSS_PREFIX`（默认 `media`）、可选 `MANAGED_OSS_ENDPOINT`。私有桶必须与原桶不同，确认私有 ACL/桶策略；不配置 endpoint 时按私有桶名生成服务端 endpoint，不能沿用旧公开桶 hostname。
- 远端桶必须显式设置 `MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true`。该值是运维确认记录，代码不能代替真实匿名读取验证。缺失私有配置会拒绝启动/上传，不会退回公开存储。
- 保留历史 provider 的私有配置，直到它的附件迁移完成；缺少对应 provider 时媒体读取失败，不回退到历史 `public_url`。

部署前必须以合成附件证明私有对象原始 endpoint / 自定义域名均不能匿名读取，并验证应用的公开、私密、付费、管理员预览、HEAD 与 Range。本文不表示这些外部验证已经完成。

## 历史附件迁移工具

正式发布会将与 `release_sha` 一致的 `/media-migrate` 二进制放入 API 镜像，无需在服务器安装 Go 或另外上传源码。在部署根目录运行以下命令，复用 API 容器的配置、网络和存储挂载。先核对工具版本与 `.release-sha` 一致，再执行只读审计；部署脚本不会自动运行媒体复制、退役或缓存确认。工具不从文章里的任意外部 URL 下载内容，只按数据库中已知的 provider/key 使用存储凭据读取；不枚举桶。默认只查询数据库，输出附件 ID、文章引用、阻断原因及尚待退役/清缓存的计数，不输出正文。

```sh
test "$(docker compose --env-file .env -f deploy/docker-compose.yml exec -T api-go /media-migrate -version)" = "$(cat .release-sha)"
docker compose --env-file .env -f deploy/docker-compose.yml exec -T api-go /media-migrate
```

审计会识别同一对象的 query、fragment、host 大小写、默认端口、路径百分号编码和清理后的路径别名。正文、下载卡片和评论的文章归属都会检查；未登记媒体与商品/工具同时使用的对象需要先人工处理。不得把无 metadata 的旧资源当成已经保护。

变更前备份数据库与原对象，保存配置版本和恢复路径，并暂停文章、评论、商品、工具写入及其他定时写入任务。维护窗口内停止入口网关和 API 服务，等待在途请求结束；通过独立工具容器复用 API 的环境、网络、运行用户和存储卷，避免 API 后台清理任务与迁移并发。停掉 `api-go` 后不能再使用 `exec`，使用下面的 `run --no-deps --entrypoint` 命令，并再次核对工具版本：

```sh
docker compose --env-file .env -f deploy/docker-compose.yml stop nginx api-go
test "$(docker compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /media-migrate api-go -version)" = "$(cat .release-sha)"
docker compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /media-migrate api-go --apply --maintenance-confirmed
```

工具逐个复制到私有存储并核对 SHA-256，再事务更新文章 Markdown/HTML、正文引用和附件 metadata；修改链接目的地址时保留代码示例中的相同文本。每个成功对象在 `managed_media_legacy_sources` 留下旧 provider/key/URL 记录。已复制项目可重复运行，不重复变更。事务失败或提交结果不确定时保留已验证私有副本和公开原件，避免删除数据库可能已经引用的新对象；人工核对后再重试，可能存在待核对的额外私有副本。

**此时历史公开 URL 仍可读取，迁移没有完成。** 文章转为私密/付费会继续要求完成旧源退役与缓存门禁；不能仅凭新 URL 有授权便宣称历史内容已受保护。所有旧链接变体也受保存门禁约束。

在独立批准删除原件、备份可恢复且内容写入仍暂停后，执行明确的退役步骤：

```sh
docker compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /media-migrate api-go --retire-public-originals --maintenance-confirmed
```

该命令只删除已经复制并有迁移记录的原件。删除前重新确认私有副本校验和，拒绝仍被文章、商品、工具或其他旧附件记录引用的对象。失败项目保留记录供重试；部署和 `--apply` 都不会自动执行这一步。

随后清除旧 CDN URL/源站缓存并禁止旧直链继续命中；验证匿名旧 URL 及其等价变体确实不可读。浏览器已经缓存或用户已经下载的公开字节无法远程撤回。完成外部清缓存验收后才记录确认：

```sh
docker compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /media-migrate api-go --confirm-cache-purge
docker compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /media-migrate api-go
```

确认步骤还会用已配置源存储验证旧对象确实不存在；源服务错误不算“不存在”。只有审计无阻断、未登记媒体已处理、待退役/清缓存计数均为零，并完成真实匿名/付费授权验收，才可结束迁移。若需在退役前回滚，恢复备份数据库及对应配置，保留已复制的私有对象；退役后的回滚还需从已验证备份恢复旧对象及缓存策略，不能只回滚代码。

迁移结果核对完成后执行 `docker compose --env-file .env -f deploy/docker-compose.yml start api-go nginx`，再恢复其他已暂停的任务并完成访问验收。迁移失败时保留维护窗口，先核对记录或恢复备份。发布回滚目录 `${DEPLOY_PATH}.rollback` 按发布累积；确认当前版本及可用回退点后人工归档旧目录，不能只按时间自动删除备份。

`0018` 将迁移审计记录与附件的存储生命周期分开。删除文章后，仍被其他文章引用的附件保留；没有剩余引用的附件进入清理宽限期，但旧源退役和缓存确认未完成时不会删除私有副本。完成迁移后可清除附件文件及元数据、释放上传额度，原 UUID 对应的旧 URL 审计记录继续保留，防止旧直链再次写入正文。删除评论所属文章时，其仍被其他正文引用的附件转由存活文章持有。

## 本地验证边界

仓库测试覆盖存储分离、预留文件名、私有流式读取/Range/HEAD、R2/OSS 签名请求与拒绝重定向、文章权限与购买校验故障、管理员 staged 预览、claim 跨文章拒绝、多文章共享及并发保存、旧 URL 变体、迁移保留原件/显式退役/校验失败保护、前端上传 claim 和保存重开。数据库用例需要 `TEST_DATABASE_URL`，每例建立独立 QA schema 并执行原始迁移。

R2/OSS 测试使用本地 HTTP 源模拟，不证明真实桶策略、凭据权限或 CDN 缓存已经正确。管理员 session 测试使用真实 session.Store 连本地 RESP fixture，不证明外部 Redis 的 TTL。此工作未执行生产迁移、删除或部署。
