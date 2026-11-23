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
