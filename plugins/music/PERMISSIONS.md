# 权限

- model.invoke

`music.generate` 仅使用 `model.invoke`，通过宿主固定的 `wechat/model_request` 路由请求。
`music_endpoint` 与私密 `music_key` 只由宿主读取；运行时、日志和工具参数均不接收密钥。

RVC、训练和 GPU 占位能力不声明 GPU、网络、素材、进程或工作区权限，也不可调用。
仅取得管理员批准的权限交集。请求上下文由宿主绑定；插件参数不能改变账号、接收者或访问范围。
