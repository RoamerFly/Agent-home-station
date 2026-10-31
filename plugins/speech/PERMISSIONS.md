# 权限

- model.invoke

当前 `speech.synthesize` 仅使用 `model.invoke`，通过宿主的固定
`wechat/model_request` 路由完成请求。仍处于迁移中的声线和训练能力不声明
GPU、网络、素材或工作区权限；它们不可调用，也不会因安装本包而取得或预留权限。

仅取得管理员批准的权限交集。请求上下文由宿主绑定；插件参数不能改变账号、接收者或访问范围。
