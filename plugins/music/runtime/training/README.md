# 显式 RVC 训练 CLI

`rvc_train_trial.py` 是实际的离线 prepare / train / index 实现，已从旧 scripts 目录迁入此安装包；`requirements.txt` 是其训练专用依赖。它是已登记项目的显式开发 CLI，**不是** `music.train` 产品 API，也不会被 MCP main.py 自动导入或执行。安装本包不授予 GPU、项目访问或新增声线权限。

训练继续使用调用者明确指定的注册解释器、原 `--project` / `--dataset` / `--experiment` / `--resume-from`、checkpoint 和所选声线来源。所有原参数与退出行为保持；没有新的默认声线选择、任务搬迁或自动重启。显式 `--device cuda:0` 不可用时拒绝，不回退 CPU。项目执行仍须保留原有 actor、profile、来源和 `gpu=true` 授权；运行本文件不能扩大这些权限。

prepare 保留官方/legacy 上游布局检测、私有 shadow、静音切分尾段修补和完整 F0/HuBERT 特征清单；train 保留依赖预检、阶段状态、最终导出 epoch/version/f0 校验、生成器和判别器实际 optimizer steps 大于零的检查与模型 SHA-256；index 保留确定性的 L2 索引和实际向量数。源模型、训练样本、优化器 checkpoint 和历史任务仍留在原位置，不纳入包索引。迁移本身不运行训练或复制任何真实权重；将来显式调用 prepare 的 shadow 行为沿用原实现。

额外依赖安装到独立目录，保留注册 CUDA torch 与推理环境。Windows 嵌入式 Python 的子进程继续通过原 `--extra-python-path` bootstrap 保留项目、依赖路径和参数；Matplotlib 3.9.4 继续支持已固定上游的 RGB 画布 API。

Windows 注册工具声明以下字段：`training_entry`、`training_requirements`、`training_package_directory`、`training_package_sha256`、`training_package_version`、`training_owner=plugin:music`，并明确 `training_product_api=pending`。入口指向已部署包资源；实际执行前必须让原项目隔离策略只读访问该已校验包。元数据声明和普通 CLI 测试不等于已经具备该只读挂载或真实 GPU 验收。

旧 `scripts/rvc_train_trial.py` 仅保留转发：优先使用显式 `WECHAT_MUSIC_PACKAGE_DIR` **和** `WECHAT_MUSIC_PACKAGE_SHA256` 成对覆盖；无覆盖时读取与旧入口相邻的 `rvc-training-plugin.json`：

```json
{"version":1,"package_directory":"<installed-or-deployed-music-package>","manifest_sha256":"<exact-plugin.json-sha256>"}
```

不完整覆盖、缺绑定、manifest SHA 不符、任一索引文件变化、链接和未索引文件均拒绝，绝不搜索 repository/plugins 作为源码回退。注册器仅将仍被引用的旧项目 CLI 更新为转发并记录此指针，不改原模型或历史任务。依赖应读取项目声明的 `training_requirements`，旧 scripts requirements 已移除。

包内合成测试覆盖 prepare/train/index 状态、特征完整性、导出与 optimizer 证据、私有副本和断点复制；兼容脚本测试覆盖独立 CLI help、原参数/退出码、明确优先级、篡改与无绑定拒绝及注册声明。测试不启动真实 GPU、训练环境、设备、网络或微信；模型质量、实际 CUDA 训练与听感仍须单独验收。
