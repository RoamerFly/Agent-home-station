# Windows 独立聊天与家目录 · 5.B.4

独立入口可以在没有微信账号、原生端、ADB 或模型运行环境的安装中启动。主人完成本机身份准备、保存模型连接后，即可在软件页面与同一条大肥鱼聊天。它复用现有聊天引擎和 SQLite 记忆内核，不把本机主人伪装成微信账号。

这是开发预览，尚未替换正在使用的正式微信程序。独立入口可在 Windows 桌面窗口中运行，也可单独启动本地 HTTP 服务。Agent 已能在自己的家目录读写文件、保存工具；配置受管 Windows 运行器后可隔离执行程序。应用接入仍在后续阶段。

## Windows 桌面预览

在源码目录构建到全新的隔离目录：

```powershell
./scripts/Build-Desktop.ps1 -LocalStation -OutputDirectory temp/local-station-preview
```

双击输出目录里的 `AgentHomeStation.exe`，窗口显示本机聊天与设置页面。此构建不要求微信原生程序、ADB、Python、GPU 环境或任何应用账号，也不拷贝原来的私有配置。Windows 仍需 WebView2 Runtime；构建需要源码开发环境。

验收使用另一个独立输出目录，加上 `-Test` 会生成合成测试资料和测试程序，不应将这份测试资料当作自己的日常安装。该模式包含两轮真实服务启停，以及私有桌面的实际 WebView 加载与退出，不向用户任务栏弹出测试窗口。

预览沿用现有最大化、普通最小化、关闭选择、托盘及单例逻辑。其单例与控制通道同旧 `WechatAgent.exe` 分开，便于迁移期间并存；预览停止操作不会停止旧微信应用。正常退出通过继承的私有管道通知子服务，先停止接收新聊天、取消当前生成，再关闭自有进程和存储。没有公开的 HTTP 停止接口。

默认私有资料在包目录 `data/station`，家目录根在其下的 `managed-workspaces`，与直接启动独立服务的默认值一致。桌面通过现有管理登录取得本机会话，并核对服务 PID 和私有目录匹配自己启动的子进程；端口被占用时拒绝接管。

需要自定义目录时，可在这个预览包自己的 `secrets/local-deployment.json` 配置：

```json
{
  "backend": "local-station",
  "data_root": "data/station",
  "workspace_root": "data/station/managed-workspaces",
  "admin_url": "http://127.0.0.1:8082"
}
```

路径相对预览包目录解析。不要将该配置写进原微信安装，也不要指向真实资料进行试验；生产迁移仍需后续应用接入步骤。未知运行结果不会在重启时自动重放；模型可按原任务编号查询，尚不自动唤醒续办。

## 启动与配置

```powershell
go build -o temp/local-home/agent-home-station.exe ./cmd/agent-home-station
& ./temp/local-home/agent-home-station.exe
```

打开 `http://127.0.0.1:8082`。空的测试安装默认管理密码为 `admin`；也可在启动前设置 `AHS_ADMIN_PASSWORD`。已有 `admin-password` 优先于环境变量，日志不输出口令。

设置目录默认在可执行文件旁的 `data/station`，工作空间默认是其下的 `managed-workspaces`，均不受启动时当前目录影响。需要自定路径时使用绝对路径：

```powershell
& ./temp/local-home/agent-home-station.exe `
  -data 'E:\AgentHomePreview\host' `
  -workspace 'E:\AgentHomePreview\workspace' `
  -listen '127.0.0.1:8082'
```

该示例建立的是隔离测试安装，不表示原来的大肥鱼已经迁移。真实安装的身份、配置和消息尚未自动搬动。

进入后先准备家目录，再在“设置”保存 API 服务商、HTTPS 地址、模型名称和 Key。默认使用 `deepseek-flash`、开启思考、强度 `low`。设置保存本身不测试连接、不调用模型，只有你发送消息后才请求服务。OpenAI 兼容接口是否支持思考以提供商协议为准；当前适配器只向 DeepSeek 发送这套思考参数。

密钥只保存在宿主的 `local-model.json`，页面只显示是否已配置，留空保留原值，显式勾选才清除。该文件独立于旧微信模型设置，既不复制旧 Key，也不修改旧账号。保存使用版本条件；发生冲突时保留编辑草稿，先核对最新配置再保存。

## 聊天、原文与失败恢复

- 本机消息的来源为 `local`，归属于稳定鱼身份及本机主人。历史按实际发生顺序展示，先显示最近一页，再按游标读取更早记录。
- 输入和实际保存的回复使用原有 SQLite 事件表。近期上下文是选取窗口，较早原文可通过 `memory_search`、`memory_read` 按需取回；工具不能选择其他微信账号或会话。
- 每次发送有稳定请求编号。相同编号、相同原话在回复已保存后再次提交，直接返回原回复，不再次调用模型。编号相同而正文变化会被拒绝。
- 回复气泡整组保存，不把只生成、尚未保存的文字显示成已完成。这里的完成表示已写入本地聊天记录，不代表微信发送、手机播放或主人已经阅读。
- 模型失败保留用户原文，页面提供同编号的手动重试。重启不自动重放未完成请求；服务端有原记录时可以继续核对。浏览器网络不确定时先读真实历史，已有回复即清除失败状态。
- 文件写入、修改和 Windows 进程启动会先登记到原请求。重试会提供最近八项操作和总数，进程结果从原作业登记查询。已有操作或缺少新版检查点的旧请求可以续查、取消原进程。需要新增写入或启动时，请发一条新请求。
- 忘记或更正来源后，旧请求和迟到结果不能绕过原来的记忆屏障。宿主关闭会取消正在生成的本地回合。
- 模型请求用量写入独立的 `local-billing`，沿用提供商报告/未知的区别。新建本地聊天目前不自动启动额外摘要、维护或复核调用，不把估算当作账单。

原始事件会留在 SQLite 中，近期窗口之外不等于删除。此步接通的是对话记录和按需读取；自动长期条目整理、主动行为与生活视图尚未接入这个独立入口。

## 家目录与旧服务

点击准备后，宿主的 `fish-identity.json` 保存鱼身份、本机主人与授权版本。家目录为：

```text
<workspace>/fish_home/<完整 fish_id>/home/
  diary/
  projects/
  works/
  scratch/
  Project Tools/
  Connected Apps/
```

聊天与家目录执行授权是独立的。暂停工作空间不会禁止主人继续聊天；此步的授权仍未挂到旧微信 Agent 的文件执行路径上。`Connected Apps` 目前是空目录，不代表微信已迁入。

### 文件与自编工具

模型可按需发现 `server_list/read/search/write/edit`，在上述家目录内读取、搜索、新建和修改文件。相对路径以家目录为根，绝对路径也必须在家目录内。新建不会覆盖已有文件；修改需要先读取，再提交内容哈希和要替换的唯一片段。文件与 `Project Tools` 下的自编工具跨重启保留。

`server_windows_process` 提供启动、列表、状态、输出和取消。启动使用绝对可执行文件路径、参数数组、家目录内工作目录及稳定的 `request_key`；同编号不会重复启动。进程完成与聊天回复是两种状态，目前要由模型在本轮查询，或由后续请求按原编号续查，尚未接入完成后主动唤醒模型的调度器。重启时未确认完成的进程标为中断，不盲目重放。

暂停工作空间会拒绝新的文件操作和程序启动，并取消仍在运行的程序；已有进程记录仍可查询和取消。写文件与暂停共用身份锁，已开始的一次原子文件操作可以先完成。运行程序的撤权检查约每秒一次，不表示暂停能撤回它此前已经写好的文件。

### Windows 运行器

可执行文件旁存在下列受管运行时布局时自动识别；没有安装运行器也能聊天及操作文件，启动程序会返回 `windows_runtime_not_configured`，不会退回不受限制的宿主命令行。

```text
runtimes/windows-python-cli/pythonw.exe
runtimes/windows-python-cli/python.exe
runtimes/windows-agent-host/windows_agent_host.pyw
```

开发预览可用 `-runtime-config` 指向绝对路径 JSON：

```json
{
  "pythonw": "E:\\AgentHomePreview\\runtimes\\windows-python-cli\\pythonw.exe",
  "helper": "E:\\AgentHomePreview\\runtimes\\windows-agent-host\\windows_agent_host.pyw"
}
```

解释器和隔离代理必须在 Agent 可修改的工作空间之外。执行使用现有 Windows LPAC 代理，仅挂载鱼家目录为可写目录；受管运行时目录为只读。配置、密钥、进程控制和暂存文件位于宿主私有目录，不作为 Agent 的可读写目录。运行器无效或隔离失败时明确失败，不自动改成宿主直接执行。

旧管理端的 `/local-home` 使用原身份注册表和管理登录，只提供身份与家目录准备，并明确引导进入独立服务聊天。不要同时运行旧版和新版程序写同一份真实身份文件。

新代码读取 v1/v2 身份文件时不自动初始化；写操作升级为 v3并保留原绑定。新版进程在 Windows 通过文件锁串行更新，锁文件不跟随最终重解析点，持锁期间不可替换。旧程序不理解 v3，不能在新记录写入后直接切回旧版继续写入；应停下相关服务，由兼容版本修复。

## 验证与下一步

本轮以合成模型替换外部传输，走真实聊天引擎、HTTP 和 SQLite，核对历史、重试、重启、遗忘和取消。页面使用真实本机服务检查准备、设置、暂停和恢复；合成气泡渲染场景单独标注。没有新增付费模型调用、真实微信消息或生产数据迁移。

```powershell
python -B scripts/test_local_home_ui.py `
  --binary temp/local-home/agent-home-station.exe `
  --output temp/local-home/browser
```

回放需要 Python Playwright 和 Edge，测试目录及截图不进入 Git。5.B.3 的真实 Windows LPAC 验证使用隔离合成文件：创建并执行 Python 工具、生成作品、拒绝读取宿主私有测试文件、相同编号不重复启动、暂停后取消进程，重启仍可查原记录。没有调用付费模型或微信。自动长期记忆整理、进程完成续办及正式桌面集成尚未交付，后续继续分步连接，再进入应用注册与微信迁移。
