# 权限

- artifact.read
- artifact.write
- model.invoke

仅取得管理员批准的权限交集。请求上下文由宿主绑定；工具参数不能改变账号、接收者或访问范围。

0.2.0 的图片/贴纸观察、入站语音转写和音频内容观察使用 `artifact.read` 与 `model.invoke`。`model.invoke` 只允许当前调用通过宿主请求声明的模型路由，不授予直接联网、任意 HTTP、文件系统或微信发送权限。视觉、远程 ASR 与音频内容观察各有独立的管理员地址与宿主持有密钥；工具参数不能更换地址或认证头。本地 ASR 不增加 `process.execute` 权限：它仅在公开配置 `local_asr_profile_id` 与宿主签发的 profile/资源版本匹配时调用受限 `wechat/local_transcription`，由宿主验证来源、素材、运行环境与资源挂载，并在隔离环境中启动本包固定的 `local_asr.py`。`media.listen` 不下载或读取音频字节，只通过来源绑定 WAV 引用调用受控 `audio-review` 路由。素材引用必须由宿主绑定到当前来源及修订，不能把任意路径或 URL 当图片或音频输入。

`artifact.write` 为尚未迁入的媒体处理契约保留，不代表当前观察实现会生成素材。撤销权限、停用、配置变更或来源失效时，宿主必须阻止后续调用及结果接受；未完成这条生产适配前保持包不可安装。
