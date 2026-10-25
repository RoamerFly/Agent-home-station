# iOS 设备令牌指纹属性的数据流

日期：2026-10-24。依据为 `token-request-only.txt`、`login-ghidra.txt`、
`device-token-go-layouts.json` 与固定 ELF 的只读字节。字符串只在反汇编给出
指针和长度时按该范围读取；未从宽泛 string 候选中猜取。没有执行样本、网络请求或读取 secrets。

## 结构和调用链

`SendIosDeviceTokenRequest` 构造 `mm.TrustData{Tdi: []*mm.TrustDeviceInfo}`；每项是
`Key`（protobuf tag 1）和 `Val`（tag 2）。固定样本分配了 **43** 个
`TrustDeviceInfo`（type `0xeae8e0`），并按下表顺序放入 Tdi。

`comm.LoginData+0x3e8` 已由 ABI 元数据确定为 `*baseinfo.DeviceInfo`，不能再将它
称作未知配置对象。`Login.GenIpadLoginData` 在 `LoginData+0x3e8` 为 nil 时调用
`Login.createDeviceInfo` (`0xb9d700`) 填充它；后者分配 416-byte `DeviceInfo` 并填入
标识、语言/地区、版本、安装/启动时间以及四组文件 `Stat`。令牌请求只读取此已存在的
`DeviceInfo`，没有在请求函数中生成新的 token。

## 43 个属性：精确 key 顺序和 value 来源

`DeviceInfo` 的偏移均相对 `LoginData+0x3e8` 指向的对象。`itoa10` 表示当前样本调用
`func_0x00495ee0(value,10)` 的十进制文本转换；`stat(k)` 表示 `*baseinfo.Stat` 的
对应成员经相同转换。`未恢复 profile literal` 代表实际值从静态 profile/辅助函数提供，
而非可安全替换的空值。

| # | Key | Val 来源 |
|---:|---|---|
| 1 | `deviceid` | `DeviceInfo.DeviceID` `+0x30` |
| 2 | `sdi` | `DeviceInfo.Sdi` `+0x148` |
| 3 | `idfv` | `baseutils.IOSUuid` (`0x7c0a00`) 的第一个返回 string；原始输入由调用参数提供 |
| 4 | `idfa` | `IOSUuid` 的第二个返回 string；原始输入同上 |
| 5 | `device_model` | `DeviceInfo.IphoneVer` `+0xa0` |
| 6 | `os_version` | `DeviceInfo.OsTypeNumber` `+0xe0` |
| 7 | `core_count` | `itoa10(DeviceInfo.CoreCount +0xf0)` |
| 8 | `carrier_name` | `DeviceInfo.CarrierName` `+0xf8` |
| 9 | `is_jailbreak` | ELF `0x013aae60` 的长度 1 literal：`"0"` |
| 10 | `device_name` | `DeviceInfo.DeviceName` `+0x40` |
| 11 | `client_version` | `LoginData.ClientVersion` `+0xa0` 经 `runtime.convT32` 和格式化 helper 生成十进制文本；不同于 `DeviceInfo` |
| 12 | `plist_version` | 同一 `LoginData.ClientVersion` 经独立的 `runtime.convT32` / 格式化路径生成十进制文本 |
| 13 | `language` | ELF `0xf6040a` 的长度 2 literal：`"zh"` |
| 14 | `locale_country` | ELF `0xf6040c` 的长度 2 literal：`"CN"` |
| 15 | `screen_width` | 由 `LoginData.DeviceType` 分支选择：`"440"`（ELF `0xf608db`，长度 3）或 `"810"`（`0xf608e1`，长度 3） |
| 16 | `screen_height` | 同一分支选择：`"956"`（`0xf608de`，长度 3）或 `"1080"`（`0xf6142a`，长度 4） |
| 17 | `install_time` | `itoa10(DeviceInfo.InstallTime +0x158)` |
| 18 | `kern_boottime` | `itoa10(DeviceInfo.KernBootTime +0x160)` |
| 19 | `ft_sysverplist_inode` | `itoa10(DeviceInfo.Sysverplist +0x168 -> inode)` |
| 20 | `ft_sysverplist_at` | `itoa10(Sysverplist -> at)` |
| 21 | `ft_sysverplist_mt` | `itoa10(Sysverplist -> mt)` |
| 22 | `ft_sysverplist_ct` | `itoa10(Sysverplist -> ct)` |
| 23 | `ft_sysverplist_bt` | `itoa10(Sysverplist -> bt)` |
| 24 | `ft_var_inode` | `itoa10(DeviceInfo.Var +0x178 -> inode)` |
| 25 | `ft_var_at` | `itoa10(Var -> at)` |
| 26 | `ft_var_mt` | `itoa10(Var -> mt)` |
| 27 | `ft_var_ct` | `itoa10(Var -> ct)` |
| 28 | `ft_var_bt` | `itoa10(Var -> bt)` |
| 29 | `ft_etcgroup_inode` | `itoa10(DeviceInfo.Etcgroup +0x180 -> inode)` |
| 30 | `ft_etcgroup_at` | `itoa10(Etcgroup -> at)` |
| 31 | `ft_etcgroup_mt` | `itoa10(Etcgroup -> mt)` |
| 32 | `ft_etcgroup_ct` | `itoa10(Etcgroup -> ct)` |
| 33 | `ft_etcgroup_bt` | `itoa10(Etcgroup -> bt)` |
| 34 | `ft_etchosts_inode` | `itoa10(DeviceInfo.Etchosts +0x188 -> inode)` |
| 35 | `ft_etchosts_at` | `itoa10(Etchosts -> at)` |
| 36 | `ft_etchosts_mt` | `itoa10(Etchosts -> mt)` |
| 37 | `ft_etchosts_ct` | `itoa10(Etchosts -> ct)` |
| 38 | `ft_etchosts_bt` | `itoa10(Etchosts -> bt)` |
| 39 | `ft_dyldcache_inode` | `itoa10(DeviceInfo.Dyldcache +0x170 -> inode)` |
| 40 | `ft_dyldcache_at` | `itoa10(Dyldcache -> at)` |
| 41 | `ft_dyldcache_mt` | `itoa10(Dyldcache -> mt)` |
| 42 | `ft_dyldcache_ct` | `itoa10(Dyldcache -> ct)` |
| 43 | `ft_dyldcache_bt` | `itoa10(Dyldcache -> bt)` |

键名的静态地址示例：`deviceid` `0xf662a6` 长度 8、`sdi` `0xf608e4` 长度 3、
`ft_sysverplist_inode` `0xf74165` 长度 20；四组 `Stat` 键的指针/长度都在
`0xb8a3..0xb8b9` 指令序列中明确写入。完整 key 顺序由这些 `TrustDeviceInfo` 分配
写入 slice 的索引确定。

## `createDeviceInfo` 的供给边界

`createDeviceInfo` 的可证实行为如下：

- 分配 `baseinfo.DeviceInfo`，调用 `0x7c1a40`、`0x7bf540`、随机/标识辅助函数填入
  UUID/设备字符串；这些 helper 的内部生成规则尚未恢复。
- 从 `LoginData` 复制既有设备描述字段，包括调用点可见的 `+0xa8/+0xb0`、
  `+0x2a8/+0x2b0`、`+0x2d8/+0x2e0` 等；随后写入 profile 固定的语言/地区、bundle/OS
  字符串。这是对象创建阶段，令牌请求阶段不会重新随机化它们。
- 针对 iPad 路径选择一组时间采集 helpers：`0x7c2760`、`0x7c26a0`、`0x7c2220`；
  将结果规范为 Unix 秒样式并传给 `0xb9df80`、`0xb9e060`、`0xb9e140`、`0xb9e260`、
  `0xb9e340`、`0xb9e420`，以生成四个 `Stat` 及相关数据。
- `GenIpadLoginData` 仅当 `LoginData+0x3e8` 为空时调用 `createDeviceInfo`，因此一次
  已保存 profile 的后续令牌请求应复用同一对象。

## 尚不能实现的部分

本证据证明了 attribute schema 和多数真实输入来源，但尚不构成可提交的指纹生成器：

1. `IOSUuid`、DeviceInfo 标识生成和各文件时间 helper 的算法/持久化规则未恢复；
2. token 服务返回的 `mm.TrustResponse` 的合法保存和刷新链仍需单独核对。

不得把这些值填成空串、随机值或本机 Windows 属性后提交请求。该表只为离线恢复和一致性
校验提供字段级依据。
