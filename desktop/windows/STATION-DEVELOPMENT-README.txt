Agent-home-station Windows 开发包

这个目录用于源码更新期间的本机开发验证。直接双击 AgentHomeStation.exe 即可运行，
程序会从自身所在的完整目录自动识别工作区。只有把资料放在自定义目录时，才需要
额外传入 --workspace <项目目录>。

或使用仓库脚本：

  powershell -ExecutionPolicy Bypass -File scripts\Build-Development-Desktop.ps1 -Test

脚本只更新工作站程序、应用适配器和随包运行时，保留本目录的 data、secrets 和
网页登录资料。若需要沿用已有资料，可在首次构建时传入 -DataDirectory <资料目录>；
脚本会把开发包部署配置绑定到该目录，不复制、删除或重置其中的账号和消息。

微信服务启用/关闭在微信「详细配置 → 账号与授权」中完成。媒体理解及模型配置在
工作站公共「项目工具」中完成，应用详情只管理微信或抖音自身功能。

开发包只用于本机验证，不等同于 dist 正式交付包；两者不要同时启动同一资料目录。
