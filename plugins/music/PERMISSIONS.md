# 权限

包声明 model.invoke 与 network，各项能力独立授权：

- music.generate 仅使用 model.invoke，经 wechat/model_request 固定 generate 路由调用云端提供商。
- music.bridge_request 仅使用 network，经 wechat/music_bridge 调用管理员配置的固定本地服务。它不是面向 Agent 的通用网络工具。

music_key 与 local_token 都是 x-secret、x-host-only；只能由宿主读取。stdio 不接收令牌或控制 HTTP 头，也不持有原始歌词、参考音频、生成音频或宿主文件路径。小型实际响应 JSON 可由宿主提供作为协议解释证据，不代替私有正文哈希。

不申请 GPU、process.execute、workspace.read/write、任意 socket 或训练权限。请求者、账号、会话、原任务、来源、配置和权限变更由宿主持续核验。插件声明和响应不能扩大授权范围，也不能把其他账号或会话的任务变为当前任务。

`music.service_control` 使用 network 权限进入已绑定的宿主生命周期代理，不授予网络 socket、任意进程启动或终止权限。只允许当前所有者或有效管理员会话控制已登记服务；插件和模型不能选择命令、参数、环境、路径、令牌或 PID。
