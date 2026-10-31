# 权限

- artifact.read
- artifact.write
- network
- gpu
- model.invoke

当前 `speech.synthesize` 仅使用 `model.invoke`，通过宿主的固定
`wechat/model_request` 路由完成请求。其余声明权限仅为仍处于迁移中的声线和
训练能力预留，不能因为本运行时存在而授予或使用。

仅取得管理员批准的权限交集。请求上下文由宿主绑定；插件参数不能改变账号、接收者或访问范围。
