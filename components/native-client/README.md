# 微信原生接入依赖

原生客户端来源、固定提交与本地累计补丁见 `source-manifest.json`。源码由原仓库获取并保留其许可证；本项目不包含微信账号、会话、原始协议样本或原生构建环境。

`research/patches/native-client-local-integration.patch` 是当前本机原生客户端相对于固定上游的完整累计补丁，涵盖语音播放/分段恢复、分享卡片元数据与引用处理。它替代早期单独的 `native-voice-playback-fix.patch`，两者不要重复应用。

只读采集已审查的源码差异：

```powershell
python scripts/sync_native_patch.py --source '<agent-wechat 源码目录>'
```

新机器在固定上游提交应用累计补丁，再构建Rust客户端。当前已经运行的账号数据不属于此步骤，不要覆盖或重建。
