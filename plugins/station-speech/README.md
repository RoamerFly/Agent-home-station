# Windows 本机语音工具

`station-speech.voices` 读取当前主人已登记的说话声线，`station-speech.synthesize` 用明确指定的声线合成文本。合成会在当前聊天回合等待结果，但宿主仍以原请求和取消状态核对结果；工具只返回经宿主核验和登记的成品，微信或其他应用的发送仍由主体另行处理。

`station-speech.job` 用于后续聊天回合查询或取消已有任务。它不会重新提交合成：本机一轮式运行器停止或桌面重启后，未完成任务只能显示为待处理或被取消。查询完成任务时，宿主会再次核对原请求来源；来源已遗忘或撤权时，不返回成品、文本摘要或核验回执。取消是窄控制操作，允许在来源被遗忘后停止残留任务，但同样不泄露成品。

Windows 安装包将 GPT-SoVITS 项目放在 `project`、基座模型放在 `models`、专用 Python 与依赖放在 `runtime/engine`、运行器放在 `runtime/control`、FFmpeg 放在 `runtime/media`。私人声线权重和样本继续放在 `release/data/voices`，不会并入公共包或 Git。安装后仍需由管理员登记声线。

可在包目录运行 `./run-speech.ps1 -VoiceId <已登记声线ID> -Text "要说的话" -OutputFile <输出.wav>` 独立合成；`-Device cpu` 可避免占用显卡。入口只使用本包运行时，读取 `release/data/publictools/registrations/station-speech.json` 的声线清单，也可用 `-Registration` 指定清单。生成文件不会自行发送。

`speed` 是可选参数，必须使用所选声线公开的可用范围；较小数值表示更慢的语速，结果会记录实际使用的速度。`text_language` 仅接受后端支持的语言枚举。文本最大长度由宿主按 UTF-8 12000 字节核验。指定声线不匹配或后端没有该声线时，任务会明确失败，不会改用默认声线。
