# 联网插件离线验收

日期：2026-10-31。Windows 本机，Go 1.25.3，项目内 Python；全部资料为合成 HTML/RSS。没有付费模型调用、实时搜索站点验证或微信发送。

## 实现范围

`plugins/web-search` 0.2.0 通过真实 MCP stdio 子进程执行网页解析与搜索相关性判断。网络操作由当前调用的宿主代理完成，子进程不获直接套接字权限。实现 Bing RSS 和 360 结果回退、正文分页、哈希续读校验、明确失败状态。

新增 Agent 发现、调用、原任务读取接口；已安装账号的旧工具名称映射到新插件。安装仍需管理员确认，迁移未对真实账号执行。其余八类插件仍在迁移中。

## 本轮已通过

- 纯 Python 合成回归：Unicode 长正文分页拼接不丢内容，脚本正文排除，哈希变化拒绝；无关搜索结果切换公共索引；禁止 file/FTP/带凭据 URL，读取失败不伪报成功。
- Windows LPAC：真实插件进程初始化、双向管道读取合成网页、搜索结果返回；任务完成落盘，同键恢复不再次请求；读取中撤权拒绝结果。
- 宿主授权：未声明 network、未知代理方法、其他账号均不能到达代理；只提供读取接口，不提供认证头或任意执行命令。
- 管理/Agent：未安装或未启用时不加载工具；安装后工具名称去重、分页字段可见；旧名调用进入插件，停用不回退旧实现。普通成员和已授权主动浏览保持公共读取权限，关闭记忆不阻止检索。
- 结果完整性：不能接受本次代理未读过的来源；不把错误帧当成成功；已核实失败与提交结果未知区分记录。原任务查询、保留工具输出分页均不能绕过来源遗忘、会话/请求者范围或实时撤权。
- 完整 Go 回归、全库 vet、仓库检查、Windows/Linux cmd 构建、原三类模板离线测试通过。后续兼容路由修改另行运行 admin 全包回归；见运行记录，不以先前通过代替新改动验证。

## 未验证及待迁移

- 没有实时访问 Bing/360，不能声称实时服务可用率或真实内容质量通过；测试代理不证明真实公网连通。
- Linux 仅构建通过，尚未运行 bubblewrap 隔离验收。
- 新插件未安装到生产账号，未替换生产 EXE。旧内置代码、分享页特殊读取、媒体搜索和付费服务商兼容能力的最终归属/清理尚未完成。
- 当前业务验证器只接受联网文本结果；媒体、设备、工作区、异步服务查询/取消仍需独立迁移与验收。

主要命令：

```text
python -B plugins/web-search/tests/test_runtime.py
go test ./internal/pluginruntime -run '^TestLive' -count=1 -v -timeout 120s
go test ./internal/admin -run '^TestPublicPlugin|^TestService' -count=1
go test ./... -timeout 300s
go vet ./...
python scripts/check_repository.py
go run ./cmd/wechat-plugin verify plugins/web-search
```

真实 LPAC 用例需显式设置 `WECHAT_PLUGIN_TEST_PYTHON` 指向项目内 Python；默认测试不会自动寻找或下载运行时。包校验成功本身不表示已安装、授权或执行。
