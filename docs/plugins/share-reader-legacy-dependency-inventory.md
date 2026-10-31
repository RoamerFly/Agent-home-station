# Share Reader：旧路径依赖盘点

日期：2026-11-01。此表用于轻主体迁移时判断哪些代码不能仅因存在 Python 插件
实现而删除。结论来自仓内静态引用，不代表真实凭据、网络或微信端已验证。

## 已删除的无调用代码

`internal/channels/isaac.go` 中的 `DecryptData` 没有任何仓内调用。现行媒体读取只使用
`DecodePrefix`，它固定解码前 128 KiB，并由 `internal/admin/channels_understanding.go` 与
`internal/admin/live_channels_test.go` 使用。已删除前者，保留后者和 ISAAC 流实现。

## 必须保留到迁移完成的旧路径

| 旧代码 / 数据 | 当前调用者 | 为什么不能删除 | 迁移后才能删除的条件 |
| --- | --- | --- | --- |
| `channels-config.json` 与 `channels-bindings.json` | `internal/admin/channels_settings.go`、管理端设置/绑定接口、预演器 | 未安装 share-reader 的账号仍使用 Cookie、人工绑定和桥接地址；配置还承担恢复与迁移来源 | 每个存量账号完成并核验迁移，保留的绑定数据已有插件等价的来源绑定与恢复路径 |
| `channels.NewResolver`、`Resolver.Resolve`、`ObjectShareURL`、`ValidCookie` | `Manager.resolveReceivedChannels` 的“未安装插件”分支 | 旧账号需保持元宝和视频号助手 Cookie 分域、短链和解析错误语义 | 已迁移账号覆盖全部，且插件宿主固定路由/凭据隔离、恢复与失败语义完成验收 |
| `channels.ResolveObject`、`ObjectBridgeURL` | 同一旧分支、设置保存校验 | 本地环回桥接是管理员受控的旧对象来源；不能改成插件直连或静默 Cookie 回退 | 插件宿主有等价的受控桥接声明、迁移了 `object_bridge_url`，并完成来源绑定验收 |
| `channels.ParseObjectBridgeResponse`、`ShareURL` | `share_bridge_plugin.go`、`share_plugin_adapter.go`、旧绑定校验 | 即使已安装插件，宿主仍在插件边界验证来源对象、短链和桥接响应，避免插件结果替换到别的分享 | 宿主将同等验证抽为稳定的插件兼容契约并有独立测试；不能仅把解析复制到 Python 后删除 |
| `channels.DecodePrefix` 与 ISAAC 流 | `channels_understanding.go`、live 合成测试 | 下载/解码仍属于核心素材交接，share-reader 只返回受控媒体位置和解码键，不处理媒体字节 | 核心素材解码被明确迁为另一个受控能力并保持同一来源/素材约束 |

## 看似重复但不可直接合并的解析

`plugins/share-reader/runtime/channels.py` 的 `share_url`、`resolve` 与 `parse_bridge_feed`
和 Go 中部分校验名称相似，但运行位置和输入授权不同：Python 接收宿主已绑定的
`wechat/http_route` 或 `wechat/channels_bridge` 响应；Go 侧负责旧账号的实际受控
传输、管理端保存校验及宿主边界复核。把 Go 函数移动到插件会使未迁移账号失去能力，
或让插件取得本不应拥有的 Cookie/桥接地址，因此本轮不做伪“去重”。

## 配置迁移边界

当前 `share-reader` 迁移只处理 `yuanbao_cookie` 和 `assistant_cookie` 的宿主私密映射。
`object_bridge_url` 被预演标为 `unmapped_legacy_fields`：它只提示“保留原处”，不等于已
迁入插件。此字段尚未有等价插件配置和受控迁移方案，因此不能因默认云端语音或公开分享
能力已迁移而删除旧设置适配器。
