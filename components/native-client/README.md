# 微信原生接入依赖

原生客户端来源、固定提交与本地累计补丁见 `source-manifest.json`。源码由原仓库获取并保留其许可证；本项目不包含微信账号、会话、原始协议样本或原生构建环境。

固定的旧源码提交早于上游根许可证的加入。本目录 `LICENSE.upstream` 保留上游提交 `7527cfc455be74b258cba34f75f38523b59ba1c7` 明确授予的 MIT 许可证；Windows 构建按固定哈希校验并附带该文件，第三方 SILK、协议与媒体声明分别保留。许可证来源独立记录，不把后来的实现混入固定源码构建。

`research/patches/native-client-local-integration.patch` 是当前本机原生客户端相对于固定上游的完整累计补丁，涵盖语音播放/分段恢复、分享卡片元数据与引用处理。它替代早期单独的 `native-voice-playback-fix.patch`，两者不要重复应用。

只读采集已审查的源码差异：

```powershell
python scripts/sync_native_patch.py --source '<agent-wechat 源码目录>'
```

新机器在固定上游提交应用累计补丁，再构建Rust客户端。当前已经运行的账号数据不属于此步骤，不要覆盖或重建。
