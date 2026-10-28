# 账号 Agent、受管工作区与跨会话附件

版本：0.35.0，2026-10-28。管理服务保存凭证与执行控制状态，Agent 只取得服务端依据当前微信消息派生的工作区范围。旧版 `host.roots=["/"]` 不再赋予 Shell 宿主访问权限。

## 目录与权限

通过 `WECHAT_WORKSPACE_ROOT` 指定稳定存储根；未指定时使用管理服务数据目录下的 `managed-workspaces`。多账号管理器继承同一根，但账号数据使用独立哈希目录。

```text
<root>/
  public_workspace/
    projects/                 公共项目代码
    models/                   公共模型
    runtimes/                 受管运行环境
    manifests/*.json          项目入口与使用说明
  pri_workspace/<account-hash>/
    guides/                   本账号管理的指南，不向其他账号共享
    global/memory/            管理员全局笔记
    conversations/<chat-hash>/
      memory/                 Markdown 记忆
      inbox/                  收到的文件副本
      output/ tasks/ scratch/
    assets/objects/<sha256>   原件及转换素材
```

账号、来源会话、真实发送者、任务编号由 `ExecutionContext` 传递；昵称、引用内容、文件正文不能提升权限。

| 请求 | 可写目录 | 可读公共资源 |
| --- | --- | --- |
| 普通成员 | 本会话目录 | projects、models、runtimes、manifests |
| 管理员普通请求 | 本会话、public_workspace | 同上 |
| 管理员显式 `global:true` 调用 | 当前账号私有目录、public_workspace | 同上 |

`global:true` 是单次调用的范围，不会改变下一位群友的身份。进程按账号、会话、请求者及权限类型分开登记，运行期间检查授权是否仍有效。普通成员的提醒与本会话文件处理可以落盘，但不能使用别人的素材编号访问其他会话。

管理密码、微信登录态、API Key、发送控制队列和进程控制文件仍保存在管理服务数据目录，不挂载给 Agent。

## 系统隔离

- Linux 使用 Bubblewrap，创建文件、进程和网络命名空间，清除环境变量，只绑定获准目录及必要的只读系统运行库。没有宿主 `/home`、盘符挂载、WSL 互操作或管理服务 Socket；子进程继承隔离。
- Windows 隐藏传输调用 LPAC AppContainer，以目录范围派生 SID 并授予该范围 ACL；没有网络能力，子进程加入关闭即结束的 Job。默认解释器为 **CMD**。旧的宿主 PowerShell 执行入口被拒绝。
- Linux `server_process(gpu:true)` 可显式开放 GPU 设备；这只表示设备挂载，不表示某一 Python/CUDA/模型组合已经通过兼容性验收。
- 隔离缺失或启动失败会返回错误，不回退为宿主执行。文件工具使用 `os.Root`，创建与编辑保留版本冲突检查；路径、符号链接、Junction 和子进程均需验收。

系统依赖：Go 1.25；Linux 需要可用的 Bubblewrap 和非特权命名空间。Windows 需要支持 LPAC 的系统及隐藏 Python 控制桥。管理服务自身属于可信控制面，其服务账号权限仍需由部署者管理。

**当前本机音乐环境边界：**Windows CMD 隔离测试通过，但已迁移的 Python/CUDA 环境在 LPAC 中启动失败，当前继续适配本机 WSL/Linux GPU 受管运行环境，暂停镜像构建。复制并校验项目不等于项目已在沙箱运行；本机受管服务现已通过隔离 CUDA 张量计算，Comfy 私有 Socket、YuE 节点发现和桥接健康检查；RVC 已在沙箱中用合成短音频完成真实模型转换（未发送微信消息）；整首歌曲与音质仍单独验收。不要为恢复歌曲功能重新启用不受限宿主 Shell。

## 项目发现与迁移

Agent 先调用 `project_list`、`project_read` 读取 `manifests/*.json` 的入口、README、解释器和使用说明，再按需读代码。项目发现不需要扫描 C、D、E 盘。

`server_search` 改为可续接的只读分批搜索，默认每次 100 条匹配、1000 个文件或约 5 秒返回一次；它们是起始值，不是整个任务上限。`max_files`、`timeout_seconds` 可显式调整或设为 0 取消相应分批边界。`has_more=true` 时原样传回 `next_cursor`，省略 `offset`，即使本页没有匹配也要继续；游标按真实文件字节与行号推进，不反复读取之前的文件。游标绑定路径、关键词、目录过滤与当前文件版本，变化时要求重新检索；整个目录是实时视图，不宣称为快照。

默认跳过 `.git`、`__pycache__`、`.venv`、`venv`、`node_modules`、`runtimes`；直接指定某目录作为搜索起点不会因它自身同名被跳过，或用 `exclude_dirs:[]` 检索全部目录。二进制前缀只做小块探测，不再为了检测模型文件读入数 MiB。结果分别给出实际分批参数、停止原因、累计跳过数和覆盖状态；跳过二进制、超长行、不可读文件与主动排除目录不等于证明目标不存在。旧 `offset` 保留兼容但会重扫，大目录应使用新游标。授权边界、`os.Root` 和取消机制保持有效。

`scripts/migrate_managed_workspaces.py` 接收明确的复制清单：

```json
{
  "root": "/srv/wechat-agent",
  "copies": [
    {"source": "/srv/legacy/yue", "target": "public_workspace/projects/yue"}
  ]
}
```

`exclude_paths` 按源根目录相对路径排除资源（如 `models`），保留 `comfy/ldm/models`、`comfy_api/input` 等源码包；旧 `exclude` 按递归目录名匹配，需谨慎。先不带 `--apply` 检查范围，再执行 `--plan <private-plan.json> --apply`。逐文件验证 SHA-256；重复执行复用相同文件，冲突时拒绝覆盖；拒绝链接和递归复制。迁移不删除原目录。模型、运行时、工作区、真实清单和迁移证据均不进入 Git。复制后仍需修复绝对路径、核对模型和运行时，并实际启动验证后再切换服务。

## 当前账号全局记忆

管理员可使用 `agent_global_search` 按会话、关键字和分页读取摘要、原消息与素材。结果携带来源会话名称、原消息时间、消息或素材编号。它不会把全部群聊全文塞进每一轮提示词，也不改变原群共享上下文。普通成员没有此检索入口。

管理端「角色 → 任务与文档」增加受管项目、工作区状态和账号全局检索；控制台登录身份可以检查这些资料，不会伪造一条微信消息并自动发送。

管理员在私聊中检查群媒体时，`video_inspect(message_id=...)` 会根据原生消息核对同账号真实来源；用缓存 `asset_id` 时可以显式指定 `source_thread_id`。普通成员仍只能读取当前会话，管理员也不能跨账号或读取未授权来源。读取结束时再次核对来源权限，不改变发起会话。

`agent_diagnostics` 向真实管理员提供按会话、消息筛选的运行时间、事件、阶段、状态和提供商代码，支持分页，不返回原始错误正文、Key、模型提示词或后台文件。检查转写不需要把含认证信息的服务目录挂载给 Shell。

音轨存在但 ASR 返回 `ASR_RESPONSE_HAVE_NO_WORDS` 时，记录为“未识别出可转写的词”，保留实际已读时间范围。它可能对应音乐、环境声或不清晰人声，不是音轨提取/鉴权失败，也不能据此声称绝无人声。其他识别错误保留阶段与 HTTP/提供商代码，视觉成功结果继续保留。

## 文件接收与跨群发送

原生客户端在 `49/6` 正文为空时投影结构化附件名、大小和消息身份；上层先识别文件，不再作为普通分享卡片。未知子类型（如尚未确认的 `49/74`）不直接视作文件。内部 CDN 凭证留在原生服务，不进入模型上下文。

接收阶段依次记录 identified、downloading、available，失败包含阶段原因。原件流式取得后计算哈希，存入账号素材库及会话 inbox；相同字节多次接收保留各次消息身份。声明的原生文件上限目前为 512 MiB；声明上限与实际已测试大小需要区分。

管理员调用：

```json
{
  "source_thread_id": "source@chatroom",
  "asset_id": "asset-from-real-message",
  "target_thread_id": "destination@chatroom",
  "kind": "voice",
  "request_key": "one-logical-request"
}
```

工具为 `wechat_send_media`；`asset_id` 和真实文件 `message_id` 二选一，kind 支持 file、voice、image、video。兼容入口 `wechat_forward_file` 走同一链路。准备完成只表示排队，用 `wechat_operation_status` 检查实际目标回执；不要再把附件放进发起私聊的最终回复。

已有音频直接复用原件，默认转换为每段最多 55 秒的完整语音队列，不重新生歌，也不固定截成三段。来源、发起和目标会话分别保存。上一段取得可验证接受回执后，按动态随机间隔推进下一段；未知回执核对目标会话记录，进入原队列退避恢复，不重发已接受段。授权撤销或原任务取消时停止。

批次日志、父任务关联、发送队列和每段回执分别持久保存。启动时修复批次保存与队列交接之间的中断，沿用原操作和媒体身份。任务在目标投递完成前不能按“已准备好”提前清理。

## 验证证据

| 项目 | 已验证范围 |
| --- | --- |
| Linux | Bubblewrap 真实执行、外部目录/链接/宿主控制目录不可见、环境清理、进程取消 |
| Windows | LPAC 真实写入本会话、拒绝外部读写及子进程越界、隐藏窗口、取消进程树、拒绝旧请求 |
| 文件 | 原生投影测试；本机已取得约 14.5 MB 真实 FLAC 原件并保存私有素材 |
| 跨会话媒体 | 合成消息与假发送器：目标绑定、多于三段、幂等、重启恢复、已接受段不重发、父任务等待、授权撤销 |
| 管理端 | 真实浏览器登录，项目与全局检索弹窗可用 |

跨群真实微信接受回执、手机端文件和语音播放、多账号实机并发、音乐 GPU 项目的沙箱运行仍须单独验收。测试模拟通过不表示真实消息已送达。跨平台拆分及后续 Docker 规划见 [镜像构建方案](../../镜像构建方案.md)。镜像启动和隔离检查不等于跨群实机投递或音乐音质验收。
