# 公共文档工坊：文件读取与处理

文档工坊属于工作站公共工具，发行源位于 `publictools/document-workshop-文档处理`；便携依赖位于 `env/document-workshop`。工作站项目工具管理安装、启用、启动和停止；微信仅把收到的附件、会话来源和任务授权交给同一公共插件，通过任务隔离的运行实例执行。

- DOCX：正文、段落和表格版式，可按 `text_offset/text_limit` 续读。
- XLSX：工作表名称、单元格坐标、共享/内联字符串、数值和公式缓存；不计算公式，没有缓存的公式明确报告 `unavailable`。正文可续读；旧二进制 XLS 不在本版解析范围。
- PDF：便携 pypdf 提取文本及页数。扫描件仍需要另外登记 OCR 运行环境，不把空文本当作成功理解。
- 处理：按已有版式生成 DOCX，修改标题，通过 ReportLab 导出支持的 DOCX 版式为 PDF，检查输出文件。处理后登记产物，由应用另行发送。

Windows 文档任务使用已授权解释器目录作为进程工作目录，解决深任务目录造成的 CreateProcessW 267 错误；具体输入和输出使用 Windows 扩展路径传递，保持原文件来源、哈希和任务目录边界。公开文件路径不作为模型参数。

发行源更新之后，工作站公共安装目录与微信任务宿主的已安装快照均需升级到相同 manifest 摘要。仅复制 publictools 不等于运行中的安装快照已更新。可使用 `scripts/update_installed_public_tool.py` 更新已安装的微信工具并保留配置；工作站通过项目工具的升级操作更新。

验证：插件 Python 单元测试、Go 文档适配/投递续办测试；设置 `STATION_DOCUMENT_TEST_RELEASE` 指向本机发行包，运行 `TestDocumentPortableTaskRuntime` 可检查真实 Windows 隔离进程和合成文档读取，全程不提交微信消息。真实群聊文件验收单独核对每次 document_inspect 成功记录与实际回复，不以脱离宿主的 Python 解析代替。
