蓝色大肥鱼 Windows 原生便携版

双击 WechatAgent.exe：自动启动 Windows 原生微信后台，直接显示管理页。
首次使用在管理页扫码登录微信，并配置模型和回复范围。
最小化：收起到托盘，服务继续运行。再次打开 EXE 恢复原窗口。
关闭窗口或托盘退出：等待在途任务后停止本实例服务并退出。

本目录可放在可写的磁盘位置。请整体保留 EXE、runtimes 和许可证。
用户资料在 data；页面会话在 secrets；升级时保留这两个目录。
无需 WSL、Docker、Python、Rust 或 Node。WebView2 使用系统已安装的运行时。
语音/视频处理另需 FFmpeg 与 FFprobe。音乐模型及 CUDA 是可选独立资源。

这个版本使用独立资料，不会自动导入官方 Windows 微信的旧聊天记录，
不会自动迁移现有 WSL 账号、模型和任务，也不会删除原资料。
微信原生登录与全部媒体能力仍需在目标机器完成实际验收。
日志在 data\logs。端口冲突时拒绝接管其他服务。

如需自定义目录/端口，在 secrets\local-deployment.json 配置：
{"backend":"windows-native","data_root":"data","account":"desktop","admin_url":"http://127.0.0.1:8081","native_url":"http://127.0.0.1:6174"}

开发者：RoamerFly
项目：https://github.com/RoamerFly/wechat-agent
反馈：https://github.com/RoamerFly/wechat-agent/issues
