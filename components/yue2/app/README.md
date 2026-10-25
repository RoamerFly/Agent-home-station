# 云端使用步骤
## 1. 虚拟环境
```bash
conda create --name yzyapp python=3.12
conda activate yzyapp
pip install -r requirements.txt

# 然后修改 start.sh 的python解释器为绝对路径
```

## 2. 启动脚本参考
```bash
#!/bin/bash

source /usr/local/miniconda3/etc/profile.d/conda.sh
conda activate py312

cd /root/ComfyUI
nohup python main.py --listen 0.0.0.0 --port 8188 --enable-manager --output-directory /root/yzyapp/outputs >/start.d/log/comfyui.log 2>&1 &
sleep 60

/root/yzyapp/start.sh > /start.d/log/start.log

```

# 其他事项
```bash
# workflow_api 目录下的工作流，需要在使用时重新导出 api json 文件。

```

