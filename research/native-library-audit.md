# libv08.so 离线静态核查

样本 SHA-256：`c89374360a439abb24bf551eda04c3001fda4b58a2bf33d9e610222382796ddf`。ELF64 x86-64 共享库，242120 字节。使用 WSL readelf、nm、strings、objdump 静态读取，随后完成下文的有限 Ghidra 反编译；未执行样本。以下文件均为本地工具输出，非 REA Evidence。

动态依赖为 libstdc++.so.6、libgcc_s.so.1、libc.so.6；导入包含 C++ 文件输入流，不能仅凭名称判定读取何种文件。动态导出包括 `rqtx` (0x96c3)、`rqtx_init` (0x95c8)、`rqtx_deinit` (0x95ea)、`si` (0xb192)、`encode_string` (0x67a7)、`decode_string` (0x68cb) 和 C++ `rqtx_calc(long,long*)` (0x77a1)。符号名并不证明函数语义或协议用途。

对 `rqtx` 的反汇编可见 0x96ed 调用 `__init()`、0x9749 调用 memset、0x989c 调用 `rqtx_calc(long,long*)`，提示至少有本地计算路径；参数语义、计算用途及主程序调用边界尚未确认。动态导入表未见直接命名的 socket/connect/send/recv 导入；这不能排除动态解析、系统调用、调用方代为联网、其他库或其他进程通信，因此无法证明无网络行为或无远程依赖。

证据：`evidence/libv08-readelf.txt`、`libv08-symbols.txt`、`libv08-strings.txt`、`libv08-rqtx-disassembly.txt`、`libv08-sha256.txt`。后续需追踪主程序对各导出函数的调用，以及反编译初始化、文件读取和计算函数，才能判断输入来源和远端依赖。

## Ghidra 有限反编译补充

主程序对本库的动态加载与 rqtx 调用边界已进一步确认，见 [最终报告](closed-protocol-final-2026-10-23.md)。上文“尚未确认”保留为早期阶段记录；算法语义与等价复现仍未完成。

使用临时完整 OpenJDK 21 与 Linux Ghidra 12.1.4 headless，首次全自动分析显示成功，但日志含反汇编告警；结果不是 REA Evidence。原先 Java 21 被拒是因为缺少 JDK 工具，并非版本过低。伪代码及日志分别在 `evidence/libv08-decompiled.txt`、`libv08-ghidra-log.txt` 和 `libv08-ghidra-rerun-log.txt`，地址为 Ghidra 映射地址（ELF 相对地址加 0x100000）。

`__init`（0010916b）读取相对路径 `rqtx.dat`，调用 `fromHex` 后复制前 0x200 字节及余下数据到分配的缓冲区并建立内部指针。`rqtx`（001096c3）传入一个指针和 0x20 字节长度，调用 `rqtx_calc`（001077a1），返回 32 位值。Ghidra 将输入变量命名为 md5，不构成确认调用者确实传入 MD5 的证据。这个路径依赖本地数据文件；其来源、计算语义、数据完整性以及远端交互仍需追踪，不能判断是否足以独立复现。反编译为近似表达且含警告，不代替动态验证。
