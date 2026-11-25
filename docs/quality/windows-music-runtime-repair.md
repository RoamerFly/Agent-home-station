# Windows 旧音乐服务运行资源修复

2026-11-21 的正式包检查发现，兼容微信管理端已启用的 `music.service_control` 仍使用旧登记。其 Python 解释器和 ComfyUI 登记文件均已从原位置迁走，原入口返回 `windows_music_python_unavailable`，不能将独立 YuE 工具的生成成功视作此入口通过。

本次代码允许该管理端使用同一正式发行包 `env/windows-python-cli/python.exe`。接纳前要求管理端处于正式 `data/applications/wechat/legacy-admin` 位置，发行清单与 Windows 平台、数据/运行目录相符，Python 文件是无重解析点的常规文件，且 SHA-256 与 `env/windows-python-cli/runtime-manifest.json` 登记一致。原先包内 Python 的规则保持兼容。当前私有配置不会被源码迁移自动改写。

验收顺序是：在隔离配置副本中仅替换失效的 Python 和 ComfyUI 登记路径；验证启动资源；运行相关 Go 测试、完整回归、vet 和仓库检查；提交并构建正式 Windows 包；保留配置原件及摘要后，测试旧入口的启动、鉴权健康和停止。若真实启动失败，恢复原配置并保留失败状态。期间不生成歌曲或发送微信消息。

本轮原始实测和配置副本仅在本机忽略目录 `temp/all-tool-acceptance-win`，不入库。全项工具与消息验收在本机交接进度文件持续记录。
