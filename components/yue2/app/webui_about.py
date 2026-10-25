# -*- coding: utf-8 -*-
"""
webui_about.py
==============
「关于」Tab:免责声明与相关链接。

"""

import gradio as gr

import webui_base as base


def build_about_tab() -> None:
    """构建「关于」页内容。"""
    gr.Markdown(
        f"""

{base.APP_DESC}。

#### 免责声明
- 本项目 **仅供学习与技术交流使用,严禁用于任何商业用途**。
- 请勿使用本工具处理、传播任何违法、侵权或违反公序良俗的内容；所处理内容的版权归原作者所有。
- 因使用本工具而产生的一切后果，均由使用者自行承担，作者不承担任何责任。
- 一旦下载、安装或使用本工具，即视为你已阅读并同意本声明。若不同意，请立即停止使用。

#### 相关链接
- 作者主页：[{base.AUTHOR_NAME}]({base.AUTHOR_URL})
- 原始项目地址：
{base.project_links_md()}
""",
        elem_classes=["about-box"],
    )
