# RVC 专用声线试训

上游：[RVC WebUI](https://github.com/RVC-Project/Retrieval-based-Voice-Conversion-WebUI)，MIT。当前本机包为 `RVC1006Nvidia`，关键源码指纹见 `source-manifest.json`；不是根据文件夹名称推断最新版本。

RVC 需要已训练的目标说话人模型，不能将百炼音色 ID 或10秒参考音频直接传入作为新歌手。已有派蒙等模型只能验证运行环境或其自身音色，不能标成角色 C。用户已授权使用百炼 C 的复刻 ID 合成不同文本，作为实验训练集。文本见 `design/voice/rvc-trial-corpus.json`，音频、模型、检索索引和训练日志全部留在忽略目录。

## 隔离原则

原包的 Python 标准库缺失。验证时在独立私有目录使用官方 Python 3.9.13 embeddable 的可执行文件和标准库，复用原包的 site-packages，并通过 `site.addsitedir` 执行其中的路径配置。没有往原包内覆盖 Python、升级依赖或删除任何模型。

在私有 shadow 项目中复制 `infer/`、`configs/`、`i18n/`，建立独立的 `logs/` 和 `assets/weights/`。只读复用原包的 HuBERT、RMVPE 和 pretrained_v2。不要把整个 `assets/` 链接回原包：训练的 `savee` 会写入 `assets/weights/`。模型、环境和本机数据不进入 Git。

## 运行入口

`scripts/rvc_train_trial.py` 是显式离线试训工具，三个阶段分别执行：

```text
<音频Python> scripts/rvc_train_trial.py --python <RVC-Python> --project <shadow项目> --dataset <语料目录> --stage prepare
<音频Python> scripts/rvc_train_trial.py --python <RVC-Python> --project <shadow项目> --dataset <语料目录> --stage train --epochs 30
<RVC-Python> scripts/rvc_train_trial.py --python <RVC-Python> --project <shadow项目> --dataset <语料目录> --stage index
```

默认实验名 `bluewhale-C-trial`，32 kHz、v2、RMVPE、batch 4、固定语料排列；从本机 f0G32k/f0D32k 预训练模型开始。每10轮保存可试听权重。训练时只使用同时具备音频、特征和两类音高文件的条目，保留静音样本。小型试训使用精确 L2 检索索引，不把尚未训练/未添加向量的索引当作可用。

`scripts/rvc_voice.py --project <shadow项目> --model <明确的pth文件> --index <明确的index文件> --source <已分离人声.wav> --output <转换人声.wav>` 负责推理，支持显式半音调整和检索比例。输出仅为人声，需与同一原文件中分离出的人声做差，再混回转换结果。不得把完整伴奏直接当作人声换声，也不能重复叠加原唱。工具不改机器人默认引擎，不向微信发送。

## 质量边界

合成语音可用于可行性试训，但不等于具备真实演唱的音域、力度和高音覆盖。重复一段短录音、给它变速或单纯增加训练轮数，不能宣称获得了更多真实训练数据。训练完成、音频能解码和用户认可歌声质量是不同验收项。

## 已选 A 与自动生成后的换声

用户已选 A：已训练的 C 模型、检索比例0.35、移调0。模型和索引在后续推理时反复加载，不为每首歌重新训练；只有更换目标声线或改进模型时才需要试训/微调。说话仍使用百炼 C 音色，唱歌使用 RVC 权重。

`scripts/singing_voice.py` 的私有配置增加 `engine: "rvc"`、`rvc_python`、`rvc_project`、`rvc_model`、`rvc_index`、`rvc_model_sha256`、`rvc_index_sha256`、`index_rate: 0.35`、`semitones: 0`。`python` 仍为提供人声分离的音频 Python，`runtime` 提供已有分离模型缓存。原 Seed-VC 配置可备份保留，但 RVC 推理不加载 Seed-VC 的模型。

桥接在任务提交时固定配置指纹；RVC 模型或索引哈希改变则拒绝执行。每首歌按顺序加载分离模型、释放它、再启动独立 RVC 进程，避免两个大模型同时占用显存。生成阶段结束后，桥接先让 ComfyUI 释放模型。任意长度的末尾分离窗口会补齐到固定窗口，截回真实长度再混音，避免 HDemucs 的 STFT 尺寸错误；不截断歌曲。

本机一次完整182.016秒录音转换验证：RTX 3060 Laptop 6 GB，单任务、模型已下载，进程墙钟约36.9秒；分离约8.2秒，RVC加载及转换约23.2秒，整卡显存采样峰值2059 MiB。包括读取、分离、换声和混音，不包括 YuE 作曲、网络下载和微信上传，也不包括训练。该结果是一次本机实测，不是三分钟原创任务的延迟承诺。文件在本机，原曲与生成音频未入 Git。

## 歌声相似度改进实验（2026-10-25）

管理员确认20条百炼合成训练语音仍符合已选C声线，问题主要发生在歌声转换。使用现有语料和优化器检查点，在独立 `bluewhale-C-v2-120` 实验目录继续训练，已在第60轮保存后停止并释放显存，尚未完成计划的120轮。30轮模型、原训练集、原优化器和当前选中A配置均保留，未自动切换声线或发送微信消息。

`--resume-from <旧实验名> --experiment <新实验名> --stage train` 仅允许新目录，复制配置、文件表和G/D优化器检查点并校验哈希。可变检查点不能硬链接到旧实验，否则后续覆盖会破坏原模型；原音频/特征仍通过文件表只读引用。大文件摘要采用分块读取，避免一次加载整个优化器。复制不完整时保留 `.preparing` 目录供检查，不自动覆盖或盲目恢复。

当前候选均用同一30秒原录音及已分离的人声：B为60轮原调、index=0.35/protect=0.33；C为60轮高八度，index=0.65/protect=0.5；D为60轮人声和伴奏同时升7半音，index=0.65/protect=0.5。试听MP3使用相同响度目标，均通过完整解码，质量尚待管理员试听确认。CAMPPlus双参考余弦诊断中旧A约0.654、B约0.664、C约0.584、D约0.647；这只是跨说话/歌唱域的诊断指标，不是“相似百分比”、用户满意度或音质通过证明。单纯增加轮数和升调尚未证明解决相似度问题。

RVC推理增加显式 `--protect` 与 `--device`。CPU采用较短切片减少单次分配，但本机训练占用约5.2GB显存时，两次CPU并行转换仍内存不足，因此实际对比采用保存检查点后串行GPU推理，不承诺低内存CPU可并行训练和推理。

歌声配置可选 `accompaniment_semitones`、`protect`，默认保持旧行为。非八度的演唱升降调必须与伴奏同步，否则拒绝配置，防止人声和伴奏不在同一调性；八度变化可保留伴奏。伴奏使用FFmpeg rubberband改变音调并保留时长，失败会报错，不静默退成跑调混音。当前A仍为人声0/伴奏0，没有改成候选声线。合成单元测试覆盖调性匹配、混音不叠原唱、时长漂移拒绝和独立复制；本机三版实际混音/转码通过，不代表已完成手机播放或主观音质验收。
