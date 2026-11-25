# ComfyUI 图像与视频生成工具

`comfyui.workflows` 列出当前身份已登记的工作流及其成品类型，`comfyui.run` 使用其中一个工作流异步生成 PNG 或 MP4，`comfyui.job` 查询或取消本任务的作业。工具接受受批准的 `workflow_id` 和类型化标量输入。

## 依赖与安装

源码目录不提交第三方运行时或模型。本机安装包的 `runtime/engine` 包含独立的 ComfyUI 核心和 Python，模型放在本包 `models`；GPU 驱动仍由系统提供。可在本包目录运行 `./run-service.ps1 -Help` 检查入口，或运行 `./run-service.ps1` 在独立端口启动服务。工作站通过私有登记文件使用同一包内运行时，并将生成素材放入 `release/data`。

`runtime/managed-service.json` 是固定的宿主协议声明，不是可直接执行的启动文件。循环回环地址、工作流登记、输入字段校验、幂等键、取消、输出文件核验和素材登记均由站点宿主负责。

## 工作流与数据位置

工具包保存程序、依赖和使用说明，不保存正在使用的工作流。工作站的共享工作流图和 `registry.json` 位于 `release/data/resources/comfyui/workflows`，自定义节点位于 `release/data/resources/comfyui/custom_nodes`；大肥鱼自行创建的工作流位于 `release/data/workspaces/home/workflows/comfyui`。在工作站登记新工作流后，使用 `comfyui.workflows` 核对可调用的编号和参数。

Wan 2.1 文生视频等现有流程从上述数据目录读取。更新工具包时保留 `release/data`，不要用工具包文件覆盖工作流登记或生成结果。

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
