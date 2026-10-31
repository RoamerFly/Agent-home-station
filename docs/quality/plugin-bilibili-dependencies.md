# B 站解析迁出与依赖启动验证

2026-10-31。仅源码、合成资料与隔离进程验证，没有真实网站、付费模型或微信消息测试。

## 分享插件

`share-reader` 0.1.3 包含独立 B 站公开解析。请求和最终地址中的 BV/分 P 必须一致，短链只能解析到受支持 BV 页面；播放请求使用页面中同一分 P 的 CID。正文/候选继续分页并校验内容哈希，跟踪参数不进入展示来源，分 P 保留。

8 组新增 Python 回归覆盖精确分 P、短链、跨作品跳转、无效/重复分 P、超过 100 的实际有效分 P、缺失分 P、元数据重复字段和错误类型、试看、非渐进格式、危险媒体地址、播放接口跳转、响应超限、内容修订及完整 stdio 双请求。连同已有快手/视频号/公开页测试共 20 项通过。

真实 Windows LPAC 执行插件 Python 代码，网络由内存合成响应提供。验证分 P 2 的 CID、同键恢复不重复读取、第一次读取后撤权不再请求播放接口，以及原快手/视频号回归。未访问真实 B 站，未验证服务端当前可用性，不宣称已实际观看视频。

正式包保持 migration-pending，只有临时测试副本可启用；15 个包文件摘要校验通过。公开分享生产适配、受控下载/素材交接、跨插件媒体理解与旧实现删除仍未完成。

## 通用依赖宿主

离线验证包含菱形共享依赖拓扑启动一次、重复启动不重启进程、不执行业务工具、缺失/停用/循环依赖在进程启动前失败、根插件配置缺失不启动子依赖、配置完成后可启动、部分失败保留实际启动结果、重叠启动与停止竞态，以及传递依赖崩溃后禁止调用。

启动失败的节点保留异常状态，已成功启动的依赖保持运行；用户可依当前状态停止或修复，宿主不自动回滚正在被其他插件使用的服务。管理 API 返回具体依赖标识与稳定错误码，不返回进程异常正文或配置密钥。

依赖只按声明的插件 ID 解析，版本约束、跨插件业务调用及 GPU/设备等新增权限策略仍需后续实施。Linux 的构建通过也不能代表实际隔离执行已验证。

## 复现命令

```powershell
python -B -m unittest discover -s plugins/share-reader/tests
go test ./internal/pluginhost ./internal/pluginruntime -count=1 -timeout 120s
$env:WECHAT_PLUGIN_TEST_PYTHON=(Resolve-Path dist/windows-native/runtimes/windows-python-cli/python.exe).Path
go test ./internal/pluginruntime -run TestLiveShareParserIsolatedExactWorkAndRevocation -v -count=1 -timeout 90s
go run ./cmd/wechat-plugin verify plugins/share-reader
```

本轮收尾：完整 `go test ./... -count=1 -timeout 300s`、`go vet ./...`、Windows/Linux amd64 管理程序构建及仓库私有凭据检查通过。真实 Windows LPAC 的已安装配置/重启恢复和分享解析回归均通过。没有替换生产 EXE 或迁移真实账号配置。
