# 公共 RVC 角色声线

公共库按角色存放在 `public_workspace/models/voice-library/<角色>/weights`，私人训练库保持账号隔离。模型和 index 不入 Git；安装脚本保存固定来源、大小与 SHA-256。

| 角色 | 调用 profile | 来源 |
| --- | --- | --- |
| 曼波（卡通-曼波） | `voice-manbo` | chaye741/RVC-Voice-Models |
| smn-曼波（保留的待兼诗歌剧） | `voice-smn-manbo` | 原 TLME/RVC-Umamusume |
| 丁真 | `voice-dingzhen` | chaye741/RVC-Voice-Models |
| 卢本伟 | `voice-lubenwei` | chaye741/RVC-Voice-Models |

新版来源：[模型](https://huggingface.co/chaye741/RVC-Voice-Models/tree/main/weights)、[配套 index](https://huggingface.co/chaye741/RVC-Voice-Models/tree/main/indices)，固定修订 `acfd9f836ca1fc862a594bc303cfccee8d565e94`。

该修订中的丁真模型为256维RVC v1，同名index却为768维；卢本伟模型为768维，同名index却为256维。原文件均保留并校验，但这两套的默认 `index_rate=0`，不将不匹配索引用于推理。曼波使用其配对索引；默认检索率不改变输入歌曲和模型身份。

在 Linux/WSL 的实际运行路径执行：

```sh
python3 scripts/install_public_rvc_voices.py --workspace /path/to/wechat-bot
```

Windows 可以先使用 `--download-only` 下载；随后在 WSL 对应路径用 `--install-only` 登记 Linux 路径。下载失败不会替换当前库，所有配套文件验证完成后才安装。原曼波角色目录移至 `smn-曼波`，保持模型字节、素材文件 ID 和旧配置内容；配置的历史权重路径通过已校验的同目录重定位加载。反复安装不会重复增加角色。

Agent 使用 `voice_library_list` / `voice_library_read` 发现角色，通过 `project_read(id=rvc)` 获取参数与用法，再将 `voice_profile_id` 交给 RVC 任务或自行运行项目。RVC 负责已有**人声音频**换声；这些权重不能直接作为 GPT-SoVITS 或百炼文本转语音使用。不同角色登记互不改变机器人默认声线。

下载校验、模型加载、真实推理、试听和微信投递是独立验收步骤；不要把下载完成当作听感验收或发送成功。
