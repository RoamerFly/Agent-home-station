# WSL 管理数据迁入 Windows

这是同一个微信登录在两个部署之间的管理数据迁移，不是导入官方 Windows 微信聊天。先验证 Windows 登录、授权收发和本机执行能力，再切换。音乐、CUDA、Linux 虚拟环境及符号链接需要单独适配，复制文件不代表已可执行。

## 备份和准备

1. 排空两端的消息、维护和任务工作，停止管理服务。确认 Windows 和原部署的账号身份绑定均存在且相同，不根据昵称判断。保留新 Windows 原生登录。
2. 复制已停止的旧管理目录和 bot-state，逐文件校验 SHA-256，检查 SQLite 完整性。先把 UNC/WSL 上的关闭数据库复制到本地；不要在跨文件系统共享上打开运行中的 SQLite 写事务。
3. 保存旧配置、服务单元和桌面入口到单独的私有留存目录，保留原始数据。Windows 切换前另取一致备份，包含本轮新资料、回执和游标。

离线迁移器只写入一个全新 staging 目录，默认只预演。以下均为合成路径和账号示例：

```powershell
python -X utf8 scripts/migrate_windows_account.py --source-admin E:/private-backup/admin --target-admin E:/private-stage/admin --from-account old-instance --to-account desktop --path-map E:/private-backup/path-map.json
# 检查预演后，对同一个尚不存在的 staging 目标执行：
python -X utf8 scripts/migrate_windows_account.py --source-admin E:/private-backup/admin --target-admin E:/private-stage/admin --from-account old-instance --to-account desktop --path-map E:/private-backup/path-map.json --apply
go run ./cmd/wechat-memory --dir E:/private-stage/admin/companion --action verify
```

私有 path-map 为旧绝对路径到新绝对路径的 JSON 对象，应分别列出管理目录、受管私人/公共工作区和资源目录。优先匹配更长的前缀。迁移器只重映射结构化身份与路径，原文、摘要、提示词、歌词和笔记不做字符串替换。原始事件编号、正文哈希、来源和修订关系保持。数据库有非空 WAL/回滚日志时拒绝迁移，先停写并取一致备份。失败的 staging 留存供检查；原始数据不改写，不在原目标上盲目重试。

## 切换与验收

- 按 migration report 中的 scope_mapping 迁移私人会话目录，校验每个普通文件；保留账号目录名与 workspace-directory 标记。Linux 链接和临时 scratch 应列入单独报告，不能跟随链接扩大读取范围。
- 校对人格、模型密钥、群规则、可信用户、记忆设置、声线绑定及素材路径。保留两端任务状态，不把摘要当完成证据。新 Windows 管理目录已有写入时必须合并或显式保留，不能覆盖；同名幂等编号冲突时停止切换。
- 保留 Windows 的 native accounts、registry、storage-key、cursor-key 和 bot-state。旧原生任务 ID、上传 ID、游标不能作为新后台的可执行状态。旧未完媒体恢复种子留存待复核，禁止自动重发。历史回执留存为历史证据。
- Windows Agent 的 full 命令模式使用 WindowsConfig（pythonw、helper、spool）；新安装由桌面默认提供包内 Python CLI 和 `runtimes/windows-agent-host`，不会启用权限或覆盖已有显式配置。旧配置迁移时校对这三条路径，spool 在 Agent 可写工作区之外。沿用 LPAC 隔离，不回退到无限制宿主 shell。Linux 公共模型/项目可留存，但需要重新注册兼容的 Windows 运行环境。
- 在同一磁盘移动已校验的 staging，以目录重命名切换。暂停标记应写到**管理目录** `admin/memory-update-hold`（多账号布局使用其公共管理根），不要误写到 native 数据根。启动后使用原有管理鉴权读取每个记忆范围、来源、任务、声线及配置，最后调用 `POST /api/runtime/memory-activate`，让程序验证后解除标记。
- 使用 Windows 原生桌面入口重新启动。旧 WSL 项目服务取消自启动并保持停止，保存服务单元，不关闭共享 WSL。配置可用 `runtime_directory` 指定独立的 `wx.exe`/`wechat-admin.exe` 运行目录，仍校验同目录清单和哈希。

## 恢复

开始接收 Windows 新写入前，校验失败可停下本实例，恢复原程序和原管理目录，再检查健康。原 WSL 数据保留，不自动启用旧服务。开始接收新写入后，暂停新处理并保留当前 SQLite、回执与游标，使用兼容版本修复或显式合并；不能把旧 JSON/旧数据库直接覆盖回去，否则会丢失新消息并可能重放任务。

本机私有迁移报告记录备份位置、文件校验、范围映射、排除项和激活检查。仓库与 PR 不存放真实聊天、密钥、私有声线原件或下载模型。
