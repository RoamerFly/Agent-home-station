# 本地固定歌声

本项目通过外部托管的 [Seed-VC](https://github.com/Plachtaa/seed-vc) 进行零样本歌声转换。固定上游代码提交和模型版本见 `source-manifest.json`。上游为 GPL-3.0；不把第三方源码、模型、Python 环境和用户参考音频打入本仓库。

机器人调用链：YuE 创作/参考翻唱 → Hybrid Demucs 分离人声 → Seed-VC 按角色参考音频转换歌声 → 混回原伴奏 → 暂存和发送。网络下载的原唱文件不经过此转换。转换不调用百炼，也不能直接使用百炼的 TTS 音色 ID；目标是已选 C 版的本地说话音频。

## 配置与安装

在单独的运行根放置固定提交的 `seed-vc/` 源码、`python-libs/` 额外依赖、`hf-cache/hub/` 模型缓存和 `torch-cache/` 分离模型。保留已有 YuE 环境，避免替换其 Torch、Transformers 或其他依赖。额外依赖安装到目标目录，参照 `scripts/singing_voice_extra_requirements.txt` 使用 `pip install --no-deps --target <运行根>/python-libs -r ...`。此清单是已验证音频环境的额外包，并不是全新 Python 环境的完整依赖锁。

当前验证基于 Windows、Python 3.13、Torch/Torchaudio 2.12 CUDA 13 和6 GB RTX 3060 Laptop。Ubuntu 路径和同一脚本可配置，但该环境尚未实测；不能由本机通过推断云端验证完成。

按清单固定版本下载模型：

- `Plachta/Seed-VC`：`DiT_seed_v2_uvit_whisper_base_f0_44k_bigvgan_pruned_ft_ema_v2.pth`、`config_dit_mel_seed_uvit_whisper_base_f0_44k.yml`。
- `funasr/campplus`：`campplus_cn_common.bin`。
- `lj1995/VoiceConversionWebUI`：`rmvpe.pt`。
- `nvidia/bigvgan_v2_44khz_128band_512x`：`config.json`、`bigvgan_generator.pt`。
- `openai/whisper-small`：`config.json`、`model.safetensors`、`preprocessor_config.json`。
- 分离模型放在 `<运行根>/torch-cache/hub/torchaudio/models/hdemucs_high_trained.pt`，来源与 SHA-256 见清单。

大文件按上游 LFS SHA-256 校验，缓存保持 Hugging Face 标准 snapshots/refs 布局。转换时使用固定本地快照，离线加载，不在聊天中临时下载或升级模型。用户参考音频、私有路径和生成歌曲不上传 Git。

私有配置例子：

```json
{
  "enabled": false,
  "profile": "bluewhale-C",
  "python": "/srv/wechat-bot/runtimes/audio/bin/python",
  "runtime": "/srv/wechat-bot/data/singing-voice",
  "reference": "/srv/wechat-bot/data/singing-voice/character-C.wav",
  "reference_sha256": "填入实际参考音频SHA256",
  "source_revision": "取source-manifest.json中的source_revision",
  "model_revisions": {"各模型仓库ID": "取清单中的对应固定提交"}
}
```

先用 `scripts/singing_voice.py --config <配置> --source <歌曲> --output <试听.wav>` 验证。Windows 启动脚本会读取 `secrets/singing-voice/config.json`；其他部署通过桥接参数 `--singing-voice-config` 指定。配置启用后，仅新提交的生成任务自动转换，已有历史音频不改写。重启桥接前必须确认无排队/运行中的任务。

## 一致性与验证边界

普通语音继续由百炼合成：固定已选复刻音色 ID，默认表达指令与单次语气指令组合，保持角色声线。歌声由本地参考音频驱动，不再次复刻百炼音色。

桥接提交时固定配置指纹和角色参考；配置或参考在排队期间改变会报错，不把另一个声音混入该任务。转换失败时保留原始生成音频，任务标记失败，不退回随机歌手作为成功结果交付。活跃模型权重必须完整匹配，只有已知的确定性位置/频率缓存按当前代码重新计算。

分离以短块重叠运行，释放分离模型后再进行换声；使用音高条件保留旋律，不自动移调。混音检查时长漂移、非有限采样与削波。该流程实现参考音色转换，仍可能出现串音、齿音或音色偏差；解码成功和固定参考不等于用户已认可音质或100%音色一致。
