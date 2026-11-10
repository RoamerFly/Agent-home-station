Agent-home-station Windows 便携包

适用 Windows x64。请将整个压缩包解压到当前用户可写的本地目录，双击 AgentHomeStation.exe。
系统需有 Microsoft Edge WebView2 Runtime。微软官方下载 https://developer.microsoft.com/microsoft-edge/webview2/consumer/
首次使用时准备大肥鱼的家，配置自己的模型服务后即可聊天。
含微信运行时的便携包会启动本机微信服务，可从「应用 → 微信」登录自己的账号。
基础包提供本地工作台。微信需要随包运行时，抖音、OBS 和项目工具各按所用能力准备。

默认本机资料在 data\station，家目录在 data\station\managed-workspaces。
更新前先安全停止工作站，备份 data 和 secrets，并保留已登记的模型、素材和应用资源。
新装使用干净的发行包。原版微信管理端的数据请通过工作站的接续流程核对。
把整套旧资料搬到新目录或新 Windows 用户时，还需核对 NTFS 所有者、微信资料库和密钥绑定路径。
这些检查通过后，再确认原账号、原会话及回执能在目标工作站读取。

最小化保留任务栏按钮；关闭时可收起到托盘或退出并停止服务。
默认工作站地址为 127.0.0.1:8082，含微信运行时的便携包另用 127.0.0.1:6174。
出现端口占用时可退出占用端口的旧站，或在本机部署文件中选择两个不同的空闲回环端口。

项目 https://github.com/RoamerFly/Agent-home-station
