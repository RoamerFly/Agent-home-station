# RVC 训练证据与指定声线

## 设备与解释器

宿主 GPU、系统隔离挂载和 Python 的 CUDA 构建分别核对。`server_status` 返回设备与挂载状态，Agent 执行 CUDA 命令需 `gpu=true`，并使用项目清单的受管解释器。未挂载设备或 CPU-only PyTorch 中的 `cuda=False` 不能推导宿主没有 GPU。

`rvc_train_trial.py` 与 `rvc_voice.py` 支持 `--device auto|cpu|cuda:0`；显式 CUDA 不可用时拒绝，不改用 CPU。唱歌配置未指定 `rvc_device` 时使用 auto，侧车记录实际分离和换声设备。虚拟环境的 Python 调用路径保留符号链接，解析成基础解释器会丢失 venv 包环境。

## 私有训练副本

训练辅助脚本检测 legacy 与当前官方布局。官方入口通过 `python -m train.…` 执行，避免把同目录 `train.py` 误当成 train 包。`--source-project` 只创建新副本，源码、日志、优化器、导出权重独立；预训练、HuBERT 和 RMVPE 资源只读复用，不覆盖角色模型。

固定上游的静音切分中，每个短句的尾段写入曾放在外循环外，导致只保留最后一个短句。辅助脚本仅在有来源清单的私有副本中修正这个已知位置，记录 compatibility_patches；不修改公共上游源文件。合成测试验证多个短句都保留，全部波形必须拥有匹配的 F0 与 HuBERT 特征才生成训练清单。

训练额外依赖见 `scripts/rvc_training_requirements.txt`，安装进独立目录，保留 CUDA torch 和推理环境。固定 Matplotlib 3.9.4 兼容上游 RGB 画布接口。训练前验证 TensorBoard 和画布依赖，避免已加载大模型后才因日志依赖缺失退出。

```text
<CUDA-Python> -m pip install --no-deps --target <private-training-libs> -r scripts/rvc_training_requirements.txt
<CUDA-Python> scripts/rvc_train_trial.py --python <CUDA-Python> --source-project <registered-rvc> --project <new-private-shadow> --dataset <authorized-samples> --experiment <new-name> --stage prepare --device cuda:0 --extra-python-path <private-training-libs>
<CUDA-Python> scripts/rvc_train_trial.py --python <CUDA-Python> --project <private-shadow> --dataset <authorized-samples> --experiment <name> --stage train --device cuda:0 --batch-size 2 --epochs 60 --extra-python-path <private-training-libs>
```

消息准备、特征完成、训练进程、优化步骤和模型导出是不同状态。每阶段写 training-state.json，失败明确标记 failed；子进程返回 0 后仍检查最终权重存在、epoch、版本和音高配置，再记录权重哈希。调用者还应核对进程及优化日志，不能将外层 shell 的 0 退出码当成内部失败程序成功的证据。epochs、batch、保存间隔和进程数可配置，默认值不是能力上限。

## 账号声线与任务绑定

GPT-SoVITS 说话权重不是 RVC 歌声权重。新增歌声权重作为独立变体登记，保留说话变体及其文件。请求或歌曲标题点名已登记声线时，`music_rvc_start` 要求匹配的 voice_id/profile，遗漏和错选会报错，不执行默认声线。

v13 桥接支持账号私有 profile：ID 使用 `private-` 前缀，清单位于该账号 voice-library/manifests，配置必须留在同一 voice-library。普通成员不可见私有目录；撤权后不能继续选用。桥接只接收受控 profile ID，不从请求接受任意配置路径。配置、模型与索引哈希固定在任务中；恢复继续核对原任务。

长期记忆有助于重新找到指定声线和未完事项；实际模型、计算设备及投递状态仍由任务配方、产物和微信回执证明。召回正确不代表执行已自动选择正确引擎。

## 验证范围

合成回归覆盖命名声线遗漏/错选、私有 profile 隔离与管理员撤权、旧桥接拒绝、零退出码但没有最终权重、切分尾段保留、参数与设备校验。真实模型、样本、训练日志和音频保存在私有工作区；不提交到源码仓库。训练成功与完整解码是技术结果，不能当作人工听感或手机播放验收。
