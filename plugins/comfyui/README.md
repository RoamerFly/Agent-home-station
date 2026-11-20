# ComfyUI 图像与视频生成工具

`comfyui.workflows` 列出当前身份已登记的工作流及其成品类型，`comfyui.run` 使用其中一个工作流异步生成 PNG 或 MP4，`comfyui.job` 查询或取消本任务的作业。工具接受受批准的 `workflow_id` 和类型化标量输入。

## 依赖与安装

此包不包含 ComfyUI、模型、节点、Python、GPU 驱动或服务地址。Windows 站点管理员可将已经审核的本机 ComfyUI 便携安装登记给宿主；宿主自行启动、停止并只连接本机 loopback broker。当前验证的外部安装可为 Windows x64 ComfyUI 0.37.2，但该软件和模型绝不随本工具包分发。

`runtime/managed-service.json` 是固定的宿主协议声明，不是可直接执行的启动文件。循环回环地址、工作流登记、输入字段校验、幂等键、取消、输出文件核验和素材登记均由站点宿主负责。

### 登记 Wan 2.1 文生视频

本包包含 `examples/wan21-text-video.json` 工作流和 `examples/wan21-text-video.registration.json` 登记记录。将工作流放到本机已登记的工作流目录后，把登记记录加入其中的 `registry.json` 的 `workflows` 列表。保持原有图像工作流条目。

| 模型目录 | 文件 |
| --- | --- |
| `diffusion_models` | `wan2.1_t2v_1.3B_fp16.safetensors` |
| `text_encoders` | `umt5_xxl_fp8_e4m3fn_scaled.safetensors` |
| `vae` | `wan_2.1_vae.safetensors` |

从 [ComfyUI 的 Wan 2.1 示例](https://comfyanonymous.github.io/ComfyUI_examples/wan/) 获取模型。工作流支持独立调整宽、高、帧数、采样步数、种子和帧率。宽、高采用 16 的倍数，帧数采用 4n+1。实际资源消耗随尺寸、帧数和步数变化。

## 使用边界

1. `workflow_id` 必须是宿主为当前身份和任务登记的工作流；未登记的工作流不会被执行。
2. `inputs` 只能包含宿主为该工作流声明的标量字段。无输入素材读取权限；未来需要素材输入的工作流须以新增权限单独登记。
3. 成功只表示成品已作为当前任务素材登记，不表示已发送到微信或其他应用。
4. `comfyui.job` 只能读取或取消当前授权范围内的作业；查询会按顺序返回最多 16 项已核验输出，取消后的迟到成品不能为原任务重新投递。

## 验证边界

包契约测试验证声明、哈希、权限和受控参数。站点 broker 启动、GPU 实际推理、模型质量、应用服务器接受和终端展示分别由宿主集成测试或人工验收记录。本包不访问网络、微信、凭据、其他账号资料或任意工作区。
