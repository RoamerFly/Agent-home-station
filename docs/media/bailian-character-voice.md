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

百炼声音设计与复刻 ID 绑定 TTS 模型，不能直接传给 YuE。说话复用百炼 ID；固定歌声通过独立的本地参考音频转换模块实现，旋律参考本身不等于固定歌声音色。TTS 试听、歌声转换执行、微信接受回执和用户认可音质需要分别验证。

官方参考：[声音设计 API](https://help.aliyun.com/zh/model-studio/voice-design-api-references)、[声音设计说明](https://help.aliyun.com/zh/model-studio/voice-design-user-guide)、[语音合成](https://help.aliyun.com/zh/model-studio/non-realtime-tts-user-guide)。

## 参考音频复刻

`Client.CloneVoice` 使用同一百炼服务的 `create_voice`，通过 Base64 Data URI 上传本机参考语音，不需要公开音频 URL。调用前把用户选择的样本转换为标准16位 PCM WAV；客户端检查容器长度、声道、采样率和5～60秒时长。参考原文件不修改。预处理与音量归一化可以分别选择，目标模型仍绑定 `qwen-audio-3.1-tts-plus`。

`TestLiveBailianReferenceVoiceVariants` 是另一个默认跳过的本机试听辅助程序。显式设置 `WECHAT_VOICE_CLONE_LIVE=1`、`WECHAT_VOICE_CLONE_REFERENCE`（准备好的 WAV 绝对路径），以及前述运行根、项目根变量后，只执行该测试。它按 `design/voice/bluewhale-reference-variants.json` 创建两个复刻配置：保留原声与开启预处理/音量归一化；原声复刻再通过不同合成指令提供俏皮、温柔等表达候选。四条试听不等于四个独立音色，也不是后期变调。

私有登记文件为 `secrets/bluewhale-voice-clone.json`，保存参考音频 SHA-256、模型、预处理参数和音色 ID。创建前记录状态，创建后先落盘 ID，再查询就绪状态；登记内容与当前样本不符时停止，不覆盖原音色。无 ID 的未决创建需先人工查证，不会重复注册。样本、转写与试听全部进入忽略目录 `research/evidence/voice-clone-bailian/`；参考识别文字不作为客观音色相似度评分。

此辅助程序不修改当前机器人音色、不向微信发送、不更新 YuE 演唱配置。用户选定前只交付试听。复刻质量与原样本、模型表现和表达指令有关，不能保证与参考完全相同。接口规范见[百炼声音复刻 API](https://help.aliyun.com/zh/model-studio/voice-clone-design-http-api)。

用户已选定参考音频复刻方案 **C「俏皮元气」**，记录在 `selected_variant`。它使用 `raw` 复刻音色与 C 的表达指令。实际服务端音色 ID 只从本机私有登记文件解析，并通过管理端语音配置接口保存，不写入仓库。C 的试听音频同时作为本地歌声转换参考；YuE 生成器本身不接收该 TTS ID。

后续已接入本地歌声转换模块，见 `components/seed-vc/README.md`。普通说话仍复用百炼音色 ID；临时表达指令叠加在角色默认设定之后，不替换默认设定。唱歌使用保存的 C 音频作为本地参考，不将 TTS ID 当作 YuE 歌手参数。
