# 蓝色大肥鱼：百炼专用音色候选

音色方向在 `design/voice/bluewhale-candidates.json`：清透元气、软糯俏皮、海盐温柔、灵动小傲娇。四个方向使用同一段试听文字，便于人工比较；均为原创成年女性角色音色，不模仿真人。

客户端 `internal/bailian/voice_design.go` 调用百炼 `voice-enrollment` 的 `create_voice` 和 `query_voice`，目标模型为 `qwen-audio-3.1-tts-plus`。音色创建属于服务端写操作，不自动重试；即使后续解析失败也保留已取得的音色 ID，通过查询恢复。服务端可能省略创建响应中的绑定模型，或返回不同的预览格式；必须查询确认绑定，不能跨模型直接套用 ID。

## 本机试听

`TestLiveBailianWhaleVoiceCandidates` 为显式启用的人工试听辅助程序，默认测试跳过。它读取私有运行目录中的百炼配置，不改变当前音色，也不向微信发送消息。

运行前设置 `WECHAT_VOICE_DESIGN_LIVE=1`、`WECHAT_LIVE_MEDIA_ROOT`（运行数据根）和 `WECHAT_LIVE_WORKSPACE`（项目根），并仅执行该测试。此操作会创建百炼音色并调用计费语音合成。普通 CI 不配置这些变量。

- 创建状态先写入忽略目录 `secrets/bluewhale-voice-design.json`，保存 ID 后才继续处理音频。存在状态但没有 ID 时停止，人工检查服务端记录，防止重复创建。
- 已存在的 ID 通过查询恢复，试听使用普通 TTS 接口合成统一台词为 WAV，另合成一段不同文字确认音色可以复用。设计接口的预览可能没有可靠容器信息，不能仅改扩展名当作 MP3。
- 音频和含服务端 ID 的目录报告保存在忽略目录 `research/evidence/voice-design-bailian/`，不进入 Git。下载遵循已有公共媒体校验，不把鉴权头传递给资源地址。
- 普通 TTS 下载的 WAV 也可能保留流式长度占位值；辅助程序仅对已识别的 PCM 布局和已知占位值，在 HTTP 完成后按实际字节数补齐容器长度，不修改音频采样。
- 试听文件交付前需要 FFprobe 检查格式、时长，并用 FFmpeg 完整解码。解码成功与用户对声音风格的满意度是不同结论。

## 能力边界

本次能力是**说话音色设计与复用**。百炼声音设计 ID 绑定 TTS 模型；当前 YuE 参考翻唱链路没有接入该 TTS 音色 ID，旋律参考也不等于固定歌声音色。用户选中候选后，才能设置对应的说话声音；若要用同一角色声线演唱，还需验证并接入支持目标声线的歌声转换或定制歌声服务。不能把 TTS 试听当作已经完成固定音色演唱。

官方参考：[声音设计 API](https://help.aliyun.com/zh/model-studio/voice-design-api-references)、[声音设计说明](https://help.aliyun.com/zh/model-studio/voice-design-user-guide)、[语音合成](https://help.aliyun.com/zh/model-studio/non-realtime-tts-user-guide)。
