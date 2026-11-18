Agent-home-station Windows 便携包

适用 Windows x64。请将整个压缩包解压到当前用户可写的本地目录，双击 AgentHomeStation.exe。
系统需有 Microsoft Edge WebView2 Runtime。提示缺少桌面运行组件时，点「获取 WebView2」，安装后返回工作站点「重试」。
微软官方下载 https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download
离线电脑可提前准备微软的 Evergreen Standalone Installer x64。
首次使用时准备大肥鱼的家，配置自己的模型服务后即可聊天。
基础包提供本地工作台和公共工具运行所用的 Python、FFmpeg 与隔离宿主。
退出工作站后，把同版本微信应用包放入 applications\wechat，把公共工具放入 publictools，再双击启动。
微信应用包须包含 runtime\windows-x64 的原生服务、管理控制台、运行库和校验清单，启动后可在「应用 → 微信」扫码。
工具包内保留程序和模型等公共资源。需要独立后端的工具在新机核对登记，网络服务按需配置自己的凭据。

默认本机资料在 data，工作区在 data\workspace。
迁移历史会话、任务、授权和个人设置时，先安全停止旧工作站，再通过可信介质单独搬运 data。
新装使用干净的发行包。原版微信管理端的数据请通过工作站的接续流程核对。
搬家后核对指向原电脑的外部工具路径，以及原账号、会话和回执。微信与第三方站点可能要求重新登录。

最小化保留任务栏按钮；关闭时可收起到托盘或退出并停止服务。
默认工作站地址为 127.0.0.1:8082，含微信运行时的便携包另用 127.0.0.1:6174。
出现端口占用时可退出占用端口的旧站，或在本机部署文件中选择两个不同的空闲回环端口。

项目 https://github.com/RoamerFly/Agent-home-station
