# 音乐与换声

范围：受控云端歌曲生成；翻唱、RVC、训练与 GPU 服务仍未迁移。

当前状态：`music.generate` 是唯一已安装、可发现的能力。它只能通过宿主绑定的
`wechat/model_request` 路由把提示词、可选歌词和超时提交给管理员配置的云端接口。
插件没有网络、密钥、文件、GPU 或微信发送能力。返回的是提供商返回的外部音频 URL，
不表示已经下载、试听、登记素材或发给任何人。

请求固定使用 `gender=female` 与 `format=mp3`。不能传入 `voice_id`，也不能把默认女声
描述成命名角色声线。提供商失败不会自动重试或切换后端。

`music.cover` 与 `music.train` 保持 migration-pending、无权限且不可发现；本包 ready
不表示 RVC、唱歌训练、素材或 GPU 能力已迁移。

宿主协议见 ../../docs/plugins/PROTOCOL.md。不得绕过宿主直接发送微信；生成、下载、素材登记与投递回执分别记录。
