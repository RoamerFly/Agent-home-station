# RVC 换声工具

`rvc.convert` 是 Windows 本地站点的异步换声能力。它只接受宿主已经为当前任务登记的音频 `artifact_id` 和精确的 `voice_profile_id`，由站点宿主连接受管的 RVC broker，产出经宿主核验的音频素材。

## 依赖与安装

仓库中的插件定义不提交 Python、模型或下载文件。安装后的 Windows 工具包将 RVC 项目放在 `project/rvc-upstream`，将其专用 Python 放在 `runtime/windows-music-python`；私有声线仍放在 `release/data/voices`。独立命令行只使用包内 Python，不依赖 `release/env`。GPU 驱动仍由机器提供。

在工具包目录运行 `./run-rvc.ps1 --model "<声线.pth>" --index "<声线.index>" --input "<输入.wav>" --output "<输出.wav>"`，或者运行 `./run-rvc.ps1 --help`。没有索引时可显式设置 `--index-rate 0`。入口以 `python -m infer.cli` 从项目根目录启动；直接执行 `infer/cli.py` 会因 Python 模块搜索路径缺少项目根目录而报 `No module named 'infer'`。

独立命令行接受明确的本机文件路径，生成结果只写到指定输出文件。`rvc.convert` 是另一条由工作站宿主管理的任务接口：管理员安装并授权 GPU 后，宿主才可启动受管服务并映射已登记的声线版本。未安装、声线不存在、GPU 不可用或服务不健康时，宿主应返回实际阶段的可恢复状态；不得改用默认声线。

对尚未分离的歌曲，运行 `./run-separate.ps1 -InputFile "<歌曲.wav>" -OutputDirectory "<输出目录>"`，默认使用包内 `models/separation` 的 `MGM_MAIN_v4.pth`，输出人声与伴奏。可用 `-Model` 选另一个已下载且在 `pymss` 清单内的模型，`-Device cpu` 可避免占用显卡。此入口为 `pymss` 加载包内 `tools` 模块和 FFmpeg，不依赖 `release/env`。

`assets/uvr5_weights/HP2_all_vocals.pth` 是旧 UVR5 格式，当前 `pymss` 模型清单和 VR 元数据均不接受它；报 `Unknown pymss model` 不是缺少 Python。独立分离改用上面的兼容模型；`run-rvc.ps1` 对分离后的人声换声。

`runtime/managed-service.json` 只是固定的宿主 broker 协议说明：没有 URL、命令、配置项或凭据，也不能由通用 MCP 执行器启动。循环回环地址、进程、请求幂等键、取消和结果核验均由 Go 宿主负责。

## 使用边界

1. `artifact_id` 只代表当前账号、会话、请求者和任务已授权的输入素材。
2. `voice_profile_id` 必须精确匹配已登记声线和任务约束；指定声线失败时不替换为其他声线。
3. 成功只表示换声成品已登记，不表示微信已发送；投递及回执属于主体能力。
4. 任务可查询、取消和恢复。取消后的迟到成品可以保留为素材，但不能为原任务重新投递。

## 验证边界

包契约测试验证声明、哈希、权限和受控目录；站点 broker 的启动、GPU 推理、真实声线质量、微信服务器接受和手机播放分别由宿主集成测试或人工验收记录。本包不会访问网络、微信、凭据、其他账号数据或任意工作区。
