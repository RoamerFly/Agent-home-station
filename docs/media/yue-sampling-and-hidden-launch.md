# YuE采样与Windows后台启动

## 无窗口启动

根目录`启动大肥鱼.vbs`是GUI启动入口，保留BAT作为命令行兼容入口。BAT由cmd解释，不能保证双击时没有短暂窗口。服务从`pythonw → CREATE_NO_WINDOW → PowerShell/Python/WSL`创建，FFmpeg、FFprobe和换声子进程也不分配控制台。仅给控制台程序传`-WindowStyle Hidden`不能避免Windows Terminal接管，不能作为后台启动保证。

`Install-Local-YuE-Service.ps1`注册当前工作区的手动守护任务，没有登录/开机触发器；显式启动音乐服务时才启动守护。不会修改全局Windows Terminal设置。`Stop-Local-YuE.ps1`仍检查任务队列和进程归属后停止。

## Agent可发现的采样接口

`music_sampling_options {}`只读查询本地桥接`GET /parameters`，实时读取ComfyUI的`object_info`。区分原生节点范围、原生默认值与项目工作流默认值。返回的`default`是实际工作流值；`min/max/enum`来自运行中的后端。

`music_local_start`支持以下可选配置；省略字段保留工作流默认值，显式0不被替换，未知/越界参数返回原因，不静默截断：

| 分组 | 参数 | 实际作用 |
|---|---|---|
| sampling.semantic | temperature、top_p、top_k、repetition_penalty | YuE音乐token采样，对应YuE2GenerateMusic |
| sampling.render | steps、cfg、sampler_name、scheduler、denoise | 音频扩散渲染，对应KSampler |
| sampling.score | max_abc_tokens、temperature、top_p、top_k、repetition_penalty、penalty_window | 原创ABC乐谱规划，对应YuE2GenerateABC |
| planning_mode | off、melody、full | 原创规划开关和内容，省略保留工作流默认 |
| text_guidance_scale | 运行接口已有的原生文本CFG | 控制音乐style/lyrics引导，与render.cfg为不同阶段 |

翻唱由SheetSage从参考录音提取乐谱，不运行原创score节点，因此传score/planning_mode会拒绝并说明；使用`reference_mode`选择melody/full。score在原创规划off时也不会被假装应用。

示例（需同时提供实际title/style/lyrics等歌曲参数）：

```json
{
  "planning_mode": "melody",
  "sampling": {
    "semantic": {"temperature": 0.8, "top_p": 0.92, "top_k": 80, "repetition_penalty": 1.15},
    "render": {"steps": 40, "cfg": 1.0, "sampler_name": "dpm_2", "scheduler": "sgm_uniform", "denoise": 1.0},
    "score": {"temperature": 0.6, "max_abc_tokens": 16000}
  }
}
```

桥接v8在入队前按当前后端校验，保存含默认值的有效配方，再向实际工作流节点传参。Agent任务与成品素材同时保留有效配方；操作幂等身份包含参数，重试不会悄悄改变或重复提交同一次生成。旧桥接遇到新参数会要求升级，避免接收后忽略。

当前验证包括合成工作流映射、参数范围/枚举、显式0、幂等、重启后配方保存和Windows无控制台进程检查。查询后端不运行模型；测试并不构成歌曲音质或手机播放验收。
