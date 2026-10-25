# YuE2 关联源码与工作流

此目录保存本机YuE整合包的轻量应用源码快照及本项目需要的API工作流，供统一维护、升级和版本审查。`source-manifest.json` 记录原始仓库、固定提交、本机有效源码文件哈希及ComfyUI版本。导入不会修改原目录。

- 应用来源：`git@gitee.com:yuziyue/yue2.git`，本机基线 `63e628e0cf970afe618930932523bb46b3e59992`；现有本地修改随快照保留。
- 当前应用快照未发现独立LICENSE文件。保留上游署名、链接和说明，不据此声称其具备任意再分发授权；公开发布前需核对授权。此处不为上游源码补写本项目许可证。
- ComfyUI使用独立上游仓库及固定版本，按其GPL-3.0许可证管理。本目录不复制其完整源码、Python环境、CUDA运行库、模型、缓存、输入或生成物。
- 桥接服务源码在 `scripts/yue_bridge.py`，通过明确的外部运行时目录连接既有ComfyUI，工作流可直接使用本仓库 `app/workflows_api/`。不再需要为更新工作流覆盖模型与环境。

维护命令（先审查外部项目改动，再运行）：

```powershell
python scripts/sync_related_sources.py --source '<现有YuE目录>/app'
python scripts/check_repository.py
```

工作流和应用源码修改后一起提交主项目；更新上游时保留基线、来源和哈希。主仓库不递归嵌入外部`.git`。
