# 运行时

`main.py` 是 Python 标准库 MCP stdio 入口，`observation.py` 负责视觉请求和结果解析。运行时不读取宿主文件、不直接联网、不接收模型密钥；通过获准调用内的 `wechat/model_request` 使用宿主模型代理。图片引用必须由具体生产策略校验后替换字节；本轮只有合成策略验收。

视频抽帧、ASR 和本地识别运行环境仍未迁入。正式包保持 migration-pending。
