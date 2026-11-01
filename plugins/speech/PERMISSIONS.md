# 权限

包声明 `artifact.read`、`model.invoke`。默认云端能力仅使用 `model.invoke`；命名能力同时使用两项权限读取宿主绑定的声线资源并请求受限本地推理。

- `wechat/model_request`：仅默认云端能力可调用固定 `synthesize` 路由；密钥保留在宿主。
- `wechat/local_speech`：仅命名能力可提交绑定的声线 ID、资源摘要、正文与推理参数。宿主解析文件路径、核验注册资源并调用已有授权的隔离模型运行器；不接受任意程序、argv 或输出目录。

stdio 执行器不授予 `gpu`、`workspace.read`、`workspace.write`、`process.execute` 或直接 `network` 权限。Windows 本地 GPU 工作者继续使用宿主批准的运行策略；该工作者不是 stdio 权限提升的通用入口。

宿主绑定账号、会话、请求者、来源和任务。插件参数不能扩大范围，不能读取其他声线或改变正文、参考录音、参考文本及模型权重。私有声线仍服从宿主原有所有者和跨会话权限要求。
