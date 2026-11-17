# 运行时协议

`main.py` 是 MCP stdio 入口。它要求宿主在 `_meta.wechat/context` 中绑定账号、会话、请求者、目的和请求键，并在 `_meta.wechat/document` 中绑定本次允许读取的 `inputs` 及受控 `output_dir`。

输入中的 `path` 只允许由宿主传入，模型工具参数只能给 `artifact_id`。输出中的临时路径只用于宿主登记素材，不能回流为下一轮模型可见的自由路径。

`document_runtime.py` 是受控运行环境桥：`doctor` 检查 Python PDF 依赖、字体、PDF 读取与 OCR；`convert` 在当前受管 Python 进程将受支持的 DOCX 导出 PDF；`verify`/`inspect` 可选地渲染并检查 PDF。它不会启动 LibreOffice、Office 或任何桌面程序。转换器不可用或既有 DOCX 布局超出首版支持范围时必须返回稳定错误码，不可创建伪造的 PDF。

`reportlab_image.py` 是没有 Pillow 原生扩展时的受控图片桥。它只解码有明确边界的 PNG，或把已核验的 JPEG 原字节交给 ReportLab 的 JPEG 路径；不支持的编码与超限图片会失败，不会被静默删掉。它不访问网络、临时目录或素材路径，调用者只传入已从 DOCX 容器读取的字节。

## Agent 脚本与版式

管理员请求可用 artifact_workspace 取得任务输入、输出、脚本目录及 Python、SDK、依赖和字体路径。通过 server_write 写脚本，server_windows_process 执行并检查退出码及输出，再用 artifact_publish 登记输出目录中的文件。该流程复用工作站进程工具和现有授权，生成不会自动发送。

reconstruct 的 layout.page 支持 width_mm/height_mm 和 top_mm/bottom_mm/left_mm/right_mm。段落支持 alignment、font_family、font_size_pt、bold、first_line_indent_pt、left_indent_pt、right_indent_pt、space_before_pt、space_after_pt、line_spacing_pt、page_break_before。field 块使用 label/value；blank_form=true 或 blank=true 将值留空。识别图片时需将印刷正文和手写填写项分开建模。PDF 导出保留页面尺寸、缩进、对齐和段落间距；字体采用导出配置，复杂版式可用脚本自定义。结构校验不能替代渲染后的逐页检查。
