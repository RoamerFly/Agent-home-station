# 换声服务诊断与候选方案

核查日期：2026-10-29。目标是保留原歌的歌词、旋律和节奏，改变人声音色；与YuE参考式再创作分开。

## 本次故障

实际本机音乐单元`wechat-agent-music.service`处于正常停止状态，配置仍指向127.0.0.1:6188，端口没有监听。两次`music_rvc_start`在健康检查阶段失败，没有创建换声任务。模型文件存在并不能代替服务启动。

同时发现原调用只使用默认角色配置，缺少按任务选择曼波的参数。新增注册音色查询、任务级`voice_profile_id`和受管服务启动工具；声线进入配方、幂等身份及恢复请求。默认C配置保持不变。实际音色相似度仍需要转换后试听。

## 候选比较

| 项目 | 适用方式 | 核实情况与建议 |
| --- | --- | --- |
| [RVC](https://github.com/RVC-Project/Retrieval-based-Voice-Conversion-WebUI) | 使用已训练的目标角色模型，将原录音分离人声后转换、再混回原伴奏 | 已有曼波权重和索引正属于这一体系，先修复现有链路。接口或WebUI更换本身不会让同一模型的质量必然变好。 |
| [YingMusic-SVC](https://github.com/GiantAILab/YingMusic-SVC) | 面向真实歌曲的零样本歌声转换；针对伴奏/和声干扰与F0错误设计 | 官方实现有命令行、Gradio和源人声/目标音色/伴奏入口。GitHub预训练表链接缺失，但已另外找到[官方权重库](https://huggingface.co/GiantAILab/YingMusic-SVC/tree/main)：732MB转换模型与1.3GB分离模型；权重页标注CC-BY-NC-4.0，与代码MIT分开。值得优先做短样对比，尚未在本机下载部署或证明听感优于现有模型。 |
| [Applio](https://github.com/IAHispano/Applio) | RVC工程化方案、训练和文件批处理 | [官方CLI](https://docs.applio.org/reference/cli/)可明确选模型、索引、音高预测、切块与清理参数，适合现有角色模型和自动化。仍属RVC体系，需检查权重架构及embedder匹配；换客户端不必然改善同一模型的相似度。 |
| [Seed-VC](https://github.com/Plachtaa/seed-vc) | 短参考音频零样本转换，支持继续微调 | 歌曲应比较V1的44.1kHz、F0条件歌声模型；V2的语音音色/口音转换能力不等于歌声更好。本项目已有相关实现和历史候选，之前听感未达到用户要求。上游已归档，继续使用需固定依赖。 |
| [w-okada/voice-changer](https://github.com/w-okada/voice-changer) | 实时麦克风/远端服务、客户端管理 | 更适合未来实时手机Agent语音；它是多引擎实时服务，不是单独的新歌声算法。版本与发行包支持的模型不同，部署前需核对RVC支持，不能承诺所有平台发行包均支持RVC。 |
| [so-vits-svc](https://github.com/svc-develop-team/so-vits-svc) | 训练专用目标歌声再转换 | 官方仓库已归档；现有RVC、GPT-SoVITS权重不能直接互换。没有充分收益依据时不作为新增主链路。 |

YingMusic-SVC的作者报告对真实歌曲转换有提升；这是上游结果，不是本机同源试听结论。[官方说明](https://github.com/GiantAILab/YingMusic-SVC)、[实际命令行参数](https://github.com/GiantAILab/YingMusic-SVC/blob/main/my_infer.sh)。Seed-VC参数及模型类型以[官方模型表](https://github.com/Plachtaa/seed-vc#model-checkpoints)为准。

## 推荐验证顺序

先恢复现有RVC服务，验证指定曼波配置实际进入任务和产物。随后固定同一段15–30秒、同一分离人声、同一伴奏与音量，对比RVC和可取得完整权重的候选算法；分别检查字音、音色、节奏、鼻音、电音和杂声。目标只有说话参考时，歌唱音域覆盖不足仍可能影响结果，不能用更多训练轮次替代试听。

所有候选保留独立配置和版本；通过短样后再决定是否接入公共工具。研究阶段不替换默认引擎、不重复生成整曲、不自动向微信发送对比样本。本机显存和耗时以实测记录，未跑通的项目不填估计数字当作结论。
