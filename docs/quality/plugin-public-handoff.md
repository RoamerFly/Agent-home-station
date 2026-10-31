# 公开分享来源与媒体交接验证

2026-10-31，Windows 本机。只使用合成消息、网页、协议响应及临时插件安装目录，没有付费模型调用、真实站点访问或微信测试发送。

## 实际完成

公开分享入口在检测到已安装插件后进入独立解析流程。未安装时暂保留原兼容路径；停用或损坏的已安装插件不会回退。`share-reader` 升至 0.1.4，但仍标记 migration-pending；所有可运行测试仅临时提升测试副本状态，不改变正式安装限制。

解析绑定原消息身份、来源版本、当前会话和授权。宿主记录实际读取过的页面及其字面媒体地址，验证插件结果的来源、候选、分页和未观察状态。平台作品身份由已核验包中的解析器检查，宿主的地址证据检查是额外约束，不能将任意网页上出现的链接都称为已核实的同一作品。

完成记录将正文和播放地址存入私有交接表；公开结果只保留来源、覆盖及未下载/未观察状态。重启后读取原私有结果，不再次运行解析。明确解析失败保存 failed 和实际错误码；未知结果仍需核对，不能直接重放。

下载重新检查来源、遗忘、启用状态、包/配置版本及任务修订。直链视频或图片先取得响应类型和地址，关闭探测响应，不将媒体正文经 MCP 传给解析器。实际下载仍走现有 DNS/IP 固定、私网拒绝、TLS、大小及内容类型检查。Referer 只包含来源站点；媒体跨域跳转拒绝。

## 验证范围

- Python 解析回归共 21 项：原视频号、快手和 B 站场景，以及 HTML video/source、音频来源排除和脚本内容排除。均为离线资料。
- Go 策略测试覆盖未读取来源、伪造地址、替换来源、重复 JSON 字段、未知字段、虚假已观察标记、分页矛盾、显式分页参数、禁用后禁止旧路径、读取中撤权、下载中撤权、跨会话和遗忘。
- HTTP 层使用本地合成服务器和测试传输器验证源站 Referer、不携带 Cookie/认证头、同源跳转、阻止跨域跳转、直链大文件类型探测，以及普通页面仍受字节限制。此注入只用于离线测试，不代表真实域名或 TLS 的线上可用性。
- 真实 Windows LPAC 执行项目内 Python：网页正文、直接媒体、广告页失败、失败不重复提交、媒体地址私有交接、记忆开启/关闭、关闭控制器后重启恢复，以及下载期间停用、修改配置、重新启用后的旧结果拒绝。
- 模拟媒体字节只验证传递和类型契约，未据此声称可解码、已观看、模型理解正确或手机可播放。

## 检查及复现

本轮完整 `go test ./... -count=1 -timeout 300s` 通过。随后增加的下载期间配置/任务版本复查通过相关 admin 回归与 LPAC 测试。完整 vet、Windows/Linux amd64 管理程序构建和包的 15 个文件摘要检查通过。无前端代码改动。

提交前执行 `python scripts/check_repository.py --private-root dist/windows-native/data`，核对 9 个本机私有值，检查结果为 0 项发现；差异格式检查通过。合成拒绝凭据 URL 的测试使用 URL 结构构造虚构值，仓库不保存实际凭据。

```powershell
python -B -m unittest discover -s plugins/share-reader/tests
go test ./internal/admin -run 'TestPublicShare|TestReceivedShare' -count=1 -timeout 90s
go test ./internal/mcptools -run TestPublicPlugin -count=1 -timeout 30s
$env:WECHAT_PLUGIN_TEST_PYTHON=(Resolve-Path dist/windows-native/runtimes/windows-python-cli/python.exe).Path
go test ./internal/admin -run 'TestLivePublicShare|TestLiveReceivedShare' -v -count=1 -timeout 90s
go run ./cmd/wechat-plugin verify plugins/share-reader
```

临时验证输出位于 `temp/plugin-public-handoff-validation`，不提交二进制、运行库或真实资料。仓库只保留合成夹具、源码和结论。

## 尚未完成

媒体理解仍为原主体适配，不是独立插件间调用；最终素材登记契约、分页续办与自动候选耗尽后的协作调度、配置迁移和旧平台代码删除仍需继续实施。该增量不代表正式分享插件或整个轻主体已完成。本轮没有安装到真实账号、更新生产程序或改变微信登录状态。
