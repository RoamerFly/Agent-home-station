# MMTLS 短连接记录层候选实现

`internal/mmtls` 是一个离线、无网络的候选记录层实现。它只接受已经建立会话后注入的 32 字节 PSK 和早期数据 ticket，构造短连接 HTTP 请求记录，并校验合成响应记录；它不实现登录、微信业务封包、RQT 或实际 HTTP 发送。

当前候选布局来自固定样本 `research/samples/main.elf.bin` 的有限静态反编译，重点证据是 `evidence/send-crypto-ghidra.txt` 中 `Mmtls.MmtlsEncryptData`（`0x8f3e60`）和 `Mmtls.MmtlsDecryptData2`（`0x8f3580`）。历史公开代码仅用于交叉理解，路径为 `C:/Temp/wechat-bot-research-20261023/wechatdll-public/Mmtls/`，仓库没有许可证；本实现没有复制其源码，也不宣称 clean-room 或线上兼容。

已实现的候选约定：

- 记录头为 `type || f1 03 || uint16be(length)`；请求依次是明文 `0x19` client hello、加密 `0x19` noop、加密 `0x17` business、加密 `0x15` close。
- client hello 及其后 noop 明文构成客户端 transcript；SHA-256 后以标签 `early data key expansion` 做 HMAC-SHA256 HKDF-expand，取得 16 字节 AES-128-GCM key 和 12 字节 IV。
- 响应第一条要求是明文 `0x16` server hello；客户端 transcript 加它形成 SHA-256，以 `handshake key expansion` 得到响应 key/IV。之后的每条允许记录均执行 AEAD 校验，不能跳过 noop 或 close 的失败。
- AAD 为 `uint64be(seq) || 5-byte record header`，nonce 为 base IV 的最后 4 字节与 `uint32(seq)` 异或；每个 `Exchange` 单独持有状态。

响应类型目前仅接受首条明文 `0x16`，以及之后加密的 `0x19`、`0x17`、`0x15`。未加入“首条 `0x16` 后再有加密 `0x16`”的猜测性支持：历史 `MmtlsDecryptData2.go` 的分包循环明确在索引 0 处理 `title == "16"`，之后只在 `title == "17"` 时解密；它是 `MMtlsPost.go` 实际调用的简化解码器。另一份未被该调用使用的 `MmtlsDecryptData.go` 虽会在 server hello 后解密下一条记录并称它为 `serverfinishdata`，但没有检查其 record type。因此静态材料不能证明加密 `0x16`，当前实现保持拒绝未知类型。受控会话包可推翻或细化这项范围。

以上的 record type、ticket 格式与 handshake transcript 都是候选。测试只使用本地独立合成 peer 验证记录编解码、损坏 tag、截断、未知类型、重复或缺失 business、输入边界和 nonce 差异；它们不是微信服务器的互通证明。下一步需要在用户授权的测试号和受控会话中取得可复核包，再逐字段比较。
