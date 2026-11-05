蓝色大肥鱼 Windows 原生便携版

双击 WechatAgent.exe：自动启动 Windows 原生微信后台，直接显示管理页。
默认最大化打开；托盘恢复时保留上次窗口状态。
首次使用在管理页扫码登录微信，并配置模型和回复范围。
普通最小化：保留任务栏按钮，服务继续运行。
仅明确选择托盘时隐藏窗口，不弹出托盘提醒。再次打开 EXE 恢复原窗口。
关闭窗口：默认弹窗选择取消、最小化到托盘运行、退出程序。
可勾选“记住此选项”；托盘菜单“关闭窗口时”可恢复每次询问。
选择退出或托盘菜单退出：等待在途任务后停止本实例服务并退出。
同一 Windows 会话只运行一个程序；重复双击或启动其他副本只唤起已有窗口。

本目录可放在可写的磁盘位置。请整体保留 EXE、runtimes 和许可证。
用户资料在 data；页面会话在 secrets；升级时保留这两个目录。
微信后台与管理页无需 WSL、Docker、系统 Python、Rust 或 Node。系统需有 WebView2 Runtime。
便携包自带 runtimes 下的 Python CLI、FFmpeg/FFprobe 和可用的 MSVC 可再发行 DLL。
LPAC 代理脚本在 runtimes\windows-agent-host；首次安装自动登记包内执行器，仍遵循 Agent 权限设置。
ASR 模型、SDK、音乐模型及 CUDA 是项目内独立托管资源，升级程序时保留。
Python CLI 是为隔离命令构建的无 GUI 派生环境，不是通用桌面 Python/Tk 环境。

这个版本使用独立资料，不会自动导入官方 Windows 微信的旧聊天记录，
底层仍使用 Linux 微信协议/设备身份，因此手机登录确认显示 Linux 电脑；
程序实际在 Windows 执行，不通过 WSL，也不冒充 Windows 客户端身份。
不会自动迁移现有 WSL 账号、模型和任务，也不会删除原资料。
微信原生登录与全部媒体能力仍需在目标机器完成实际验收。
日志在 data\logs。端口冲突时拒绝接管其他服务。

如需自定义目录/端口，在 secrets\local-deployment.json 配置：
{"backend":"windows-native","data_root":"data","account":"desktop","admin_url":"http://127.0.0.1:8081","native_url":"http://127.0.0.1:6174"}

开发者：RoamerFly
项目：https://github.com/RoamerFly/Agent-home-station
反馈：https://github.com/RoamerFly/Agent-home-station/issues
