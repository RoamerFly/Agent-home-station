# GPT-SoVITS 本地角色语音

`project_list` / `project_read {"id":"gpt-sovits"}` 返回实际项目、解释器、基础模型和入口路径。
公共源码位于 `public_workspace/projects/gpt-sovits`；公共基础模型位于
`public_workspace/models/gpt-sovits`。运行时位于 `public_workspace/runtimes/gpt-sovits-linux`。
Python 环境复用同一公共工作区的 Linux music CUDA 包，独立安装 GPT-SoVITS 推理依赖；不重复复制 CUDA/Torch。

## Agent 调用

1. 用 `voice_library_list` 找到指定名称，再用 `voice_library_read` 核对 `engine`、推荐 GPT/SoVITS 配对和参考。
2. `voice_library_prepare` 校验选定的权重、参考音频和转录文本；已迁移的私人声线直接返回账号 voice-library 中的文件，避免重复复制。旧控制目录归档仍会准备到账号 global。工具返回每个文件的真实路径、原文件名及类型，不能根据哈希文件名猜模型类型。
3. 私人声线使用账号 global 目录。真实管理员在私聊或群里均可通过 `server_exec` / `server_process` 的 `global=true` 挂载；CUDA 使用 `gpu=true`。
4. 阅读 CLI `--help` 或直接读取上游 `api_v2.py`、`GPT_SoVITS/TTS_infer_pack/TTS.py`。可编写自己的脚本，不必局限于此适配入口。
5. 输出 WAV 放在本账号当前会话 output 或 global 私有 output；用素材导入和微信发送工具投递。合成成功不等于已发送。

命令示意，所有变量取自上述工具真实结果：

```sh
"$PYTHON" "$PROJECT/gpt_sovits_tts.py" \
  --project "$PROJECT" --models "$BASE_MODELS" \
  --gpt "$GPT_WEIGHT" --sovits "$SOVITS_WEIGHT" --version v2 \
  --reference "$REFERENCE_WAV" --prompt-text-file "$REFERENCE_TXT" \
  --text '你今天刷牙了吗？' --device cuda \
  --output "$PRIVATE_OUTPUT/角色试听.wav"
```

参考音频的转录文字必须与**参考音频**对应，不能写成要合成的新句子。
可用 `--parameters JSON文件` 调整并透传原生 `TTS.run` 参数，例如 top_k、top_p、temperature、seed、speed_factor、batch_size、sample_steps、cfg_rate、text_split_method 等。明确传入的值不被适配器静默缩小。
CLI 输出完整 WAV；流式消费用上游 API 或自写代码。缺少指定权重或 CUDA 不可用会明确报错，不换成默认声线或静默回退 CPU。显式 `--device cpu --no-half` 可使用 CPU。

普通好友/群友只能使用公共条目；个人声线归档仍由账号管理员访问。获得读取权限不代表任意引擎兼容：GPT-SoVITS 的文本合成不能直接作为 RVC checkpoint 使用。

## 新机器安装

准备官方 GPT-SoVITS checkout、对应版本通用基础模型及可运行的公共 Linux CUDA Python 环境。
从主项目运行 `scripts/register_gpt_sovits.py --source CHECKOUT --public-root PUBLIC --python PUBLIC/runtimes/gpt-sovits-linux/bin/python`。
该步骤导入已提交源码和明确列出的通用资源，不导入训练记录、个人权重、源环境或原 API 配置；不会覆盖不同版本的受管资源。

在公共 runtimes 创建独立 venv；将同一公共工作区的 Linux music 环境 site-packages 路径登记为该 venv 的 `.pth`，复用既有 Torch/CUDA。然后用新解释器安装 `gpt_sovits_inference_requirements.txt`。资源版本应按登记清单校验；迁移时这两个运行时需要一起迁移。也可以建立完全独立的 CUDA 环境并更新 manifest 的 python 字段。

官方说明：[GPT-SoVITS](https://github.com/RVC-Boss/GPT-SoVITS)。源码与基础模型安装、导入检查、真实推理、听感确认及微信播放验收是不同状态，分别记录。
