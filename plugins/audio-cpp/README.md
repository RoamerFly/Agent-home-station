# audio.cpp 语音引擎

这是由工作站宿主接管的 Windows 本机 audio.cpp 适配器，提供登记模型的离线语音生成和语音识别。模型、运行目录和服务地址由主人登记，聊天工具只接收宿主核验后的结果。

Windows 安装包将可执行文件和 DLL 放在 `runtime/engine`，发音器放在 `runtime/phonemizer`，模型放在 `models`。可在包目录运行 `./run-audio-cpp.ps1 --Mode cli --help`，或 `./run-audio-cpp.ps1 --Mode server` 启动本包引擎；不依赖 `release/env`。独立启动的服务仍需本机模型配置，输出不会自行发送到聊天。
