# Windows release 目录精简计划

当前本机发布目录：`E:\Agent\agent-home-workstation\dist\release`。此文只规定迁移顺序；运行中的微信登录、任务、模型和媒体不参与自动清理。

## 目标结构

| 目录 | 保留内容 |
| --- | --- |
| `applications/` | 微信、抖音、哔哩哔哩等应用本体及入口 |
| `publictools/<工具>/` | 各公共工具的程序、模型依赖和独立 `runtime/`；每个工具能从自己的入口启动 |
| `data/applications/` | 登录、消息、任务、应用状态 |
| `data/voices/` | 声线登记、训练素材、模型和索引；工具通过登记引用，不复制模型到程序目录 |
| `data/publictools/` | 工具登记和用户配置 |
| `data/artifacts/` | 生成媒体及可恢复的中间结果 |
| `AgentHomeStation.exe`、`manifest.json`、许可证 | 发布入口与必要元数据 |

## 现状与处理顺序

1. **固定引用清单。** 扫描登记文件、服务启动参数和正在运行的进程，记录对 `release/env`、`publictools/*/models`、`data/voices` 的实际引用。任何引用未迁移前不移动其目标。
2. **工具环境归位。** 将 `release/env` 中仍由公共工具使用的环境分别归入该工具的 `runtime/`，修改登记和独立入口；逐个做一次启动与代表性任务检查。`windows-agent-host` 等工作站自身环境另行归属，不套用公共工具规则。
3. **模型只登记一份。** RVC 与 GPT-SoVITS 声线以 `data/voices` 为唯一有效目录。更新登记后，确认新权重与索引可执行，再把旧版本移动到 `E:\待删除` 的带日期目录。训练项目和原始样本继续留在 `data/voices`，直到确认复现需求和模型来源记录完整。
4. **清理发布根目录。** 对 `station-acceptance.test.exe` 这类构建或验收遗留文件，在确认没有进程、脚本或任务引用后，从下次本机更新包中排除；许可证仍随应用保留。根目录最终只放入口、清单和许可证。
5. **原地更新。** 用候选包替换程序文件并重启工作站；`data/` 独立保留，更新不覆盖用户状态。先核对服务健康及实际微信消息接收，再处理旧目录。

## 完成标准

- 每个公共工具从包内入口启动，不依赖 `release/env` 的共享 Python。
- 声线登记只指向 `data/voices` 中当前版本，真实语音合成与 RVC 换声各完成一次本地验证。
- 工作站更新后仍能读取原登录、消息、任务和媒体，服务恢复在线。
- 待删除清单注明原路径、目标路径、引用检查结果和迁移日期；不在本轮直接永久删除。

## 2026-11-23 首轮执行记录

- 发布根目录的 `station-acceptance.test.exe` 已确认不是正在运行的程序，且源码构建脚本不引用它；按原 SHA-256 `9eef9a7522e481fef70180fd0ef996dab901e5a7c78bfcd8898b57b6b828a275` 移至 `E:\待删除\agent-home-workstation-release-cleanup-20261123`，保留恢复能力。程序与数据没有重命名。
- 已将 ComfyUI 原有四个工作流登记文件逐文件校验复制到 `data/resources/comfyui/workflows`，创建 `data/resources/comfyui/custom_nodes` 作为自建节点目录。旧文件暂留原位，直到新登记与实际服务读取成功。
- 当前 `env/windows-python-cli` 和 `env/windows-agent-host` 被工作站 Agent 配置直接引用；`windows-agent-host` 还被运行中的公共工具桥接进程引用。`env/windows-station-speech`、`document-workshop`、`windows-media` 仍有站内或旧配置引用。首轮不移动这些运行环境，也不把目录名当成重复证据。
- 后续逐项把真正的公共工具运行时引用改为各自 `publictools/<工具>/runtime`，核验后再移动无引用副本。`env` 中属于工作站自己的运行器需要先确定新归属并完成配置切换。当前清理仅完成根目录遗留程序与 ComfyUI 工作流数据迁移，尚未达到整目录完成标准。

## 2026-11-25 工作区收拢与目录审查

- Astra 审查正式目录：`data/workspace` 是 7 个空目录、0 个文件；实际 `data/workspaces` 有 1,311 个文件，共 4,239,124,002 字节。正式配置为 `workspace_root=data\\workspaces`。独立服务默认值、工具文件索引和媒体删除扫描中的旧路径已在源码中改为使用当前工作区根，同时保留历史目录读取。
- 空的 `data/workspace`、`data/applications/wechat/legacy-admin/agent-workspace` 和 `managed-workspaces` 已移到 `E:\待删除\agent-home-workstation-release-cleanup-20261125-ad989707`。无登记引用的 `data/voices/rvc/retired-public-library-20261122` 也完整移入同一保留区，原文件共 108 个、5,635,028,126 字节；其中仍有约 523 MB 与当前私有声线库不同或缺失，因此不做按文件去重。
- `data/music-experiments` 的 8 个文件、173,730,062 字节已原盘移动到 `data/workspaces/home/works/music-experiments`，文件总字节数保持一致。未移动微信登录、会话、任务或当前声线。
- 工作站内部暂存路径已在结构化布局中归入 `data/main_setting/tmp`；原 `data/project-tool-tasks` 与 `data/comfyui-output-stage` 均无文件，空骨架移入同一待删除批次。本地视觉用量 `usage.json` 移入 `data/main_setting/local-vision-usage`，迁移前后 SHA-256 均为 `DB5AC6411300904A696701D617FFC981AB04319ED79C711AAB66754B691A06C7`。旧布局在其他安装中仍可读取已有用量账本。
- 正式 `data` 根目录现为 `applications`、`artifacts`、`main_setting`、`publictools`、`resources`、`tools`、`voices`、`workspaces` 八个目录。
- 全发布目录中，至少 10 MB 的同体积候选做哈希比较后发现约 23.65 GiB 的逻辑重复内容，主要是各工具独立运行环境的 CUDA/Python 文件；它们按各自路径运行，不作为可清理副本。`data/publictools/obs-preview/config` 是到 `data/tools/obs-preview/config` 的目录联接，没有第二份配置。
- 更新脚本报告 `updated/restarted/healthy/native_login=true`；清理后本机 `:8080/healthz` 和 `:8082/healthz` 都返回 200。本次没有发送微信消息或对真实媒体执行新的生成任务。
