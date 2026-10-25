# -*- coding: utf-8 -*-
"""
webui_history.py
================
「历史结果」Tab:浏览 outputs 目录下的所有产物文件(图片 / 视频 / 音频 / 其他)。

功能
----
* 列表显示:文件名(含子目录相对路径)、修改时间、大小,**按修改时间倒序**。
* 预览 / 播放:点击任意一行,在右侧按类型渲染 图片 / 视频 / 音频 / 通用文件。
* 下载:图片 / 视频 / 音频组件自带下载按钮;其他类型用文件组件的下载链接。
* 批量删除:勾选多行后点「删除选中」,需再次确认(永久删除,不可恢复)。

只依赖 webui_base(公共层)与 gradio,不反向依赖 webui.py,避免循环导入。
"""

from pathlib import Path

import gradio as gr

import webui_base as base

# 表格列顺序:选择(复选框) / 文件名 / 修改时间 / 大小
_HEADERS = ["", "文件名", "修改时间", "大小"]
_DATATYPE = ["bool", "str", "str", "str"]


# ---------------------------------------------------------------------------
# 数据
# ---------------------------------------------------------------------------
def _scan() -> list[dict]:
    """递归扫描 outputs 目录下的所有文件,按修改时间倒序返回。"""
    files: list[dict] = []
    root = base.OUTPUT_DIR
    if root.exists():
        for p in root.rglob("*"):
            try:
                if not p.is_file() or p.name.startswith("."):
                    continue
                st = p.stat()
                files.append(
                    {
                        "path": str(p),
                        "rel": str(p.relative_to(root)).replace("\\", "/"),
                        "mtime": st.st_mtime,
                        "size": st.st_size,
                    }
                )
            except OSError:
                continue
    files.sort(key=lambda f: f["mtime"], reverse=True)
    return files


def _rows(files: list[dict], checked: bool = False) -> list[list]:
    """把文件列表转成表格行(可指定复选框统一勾选 / 取消)。"""
    rows = []
    for f in files:
        kind = base.file_kind(f["path"])
        name = f'{base.kind_icon(kind)} {f["rel"]}'
        rows.append([bool(checked), name, base.fmt_ts(f["mtime"]), base.fmt_size(f["size"])])
    return rows


def _count_text(files: list[dict]) -> str:
    total = sum(f["size"] for f in files)
    return f"共 **{len(files)}** 个文件 · 总计 {base.fmt_size(total)}"


def _is_checked(v) -> bool:
    """兼容 bool / 数字 / 字符串等多种复选框取值。"""
    if isinstance(v, str):
        return v.strip().lower() in ("true", "1", "✓", "yes", "是")
    try:
        return bool(v)
    except Exception:
        return False


def _selected_paths(table, files: list[dict]) -> list[str]:
    """从当前表格值 + 文件列表(按行一一对应)里取出被勾选的文件路径。"""
    paths: list[str] = []
    if table is None:
        return paths
    try:
        rows = list(table)
    except TypeError:
        return paths
    for i, row in enumerate(rows):
        if i >= len(files):
            break
        if row is not None and len(row) and _is_checked(row[0]):
            paths.append(files[i]["path"])
    return paths


# ---------------------------------------------------------------------------
# 回调
# ---------------------------------------------------------------------------
def _refresh():
    """重新扫描并重置整个页面(清空预览、隐藏确认条)。"""
    files = _scan()
    hide = gr.update(visible=False, value=None)
    return (
        gr.update(value=_rows(files)),   # table
        files,                           # files_state
        _count_text(files),              # count_md
        gr.update(visible=False),        # confirm_group
        hide,                            # prev_image
        hide,                            # prev_video
        hide,                            # prev_audio
        hide,                            # prev_file
        gr.update(visible=True),         # prev_hint
    )


def _on_select(files: list[dict], evt: gr.SelectData):
    """点击某一行 -> 按类型渲染预览 / 播放组件。"""
    hide = gr.update(visible=False, value=None)
    img = vid = aud = fil = hide
    hint = gr.update(visible=False)
    try:
        row = evt.index[0] if isinstance(evt.index, (list, tuple)) else evt.index
        f = files[row]
    except (TypeError, IndexError, KeyError):
        return hide, hide, hide, hide, gr.update(visible=True)

    path = f["path"]
    kind = base.file_kind(path)
    if kind == "image":
        img = gr.update(visible=True, value=path)
    elif kind == "video":
        vid = gr.update(visible=True, value=path)
    elif kind == "audio":
        aud = gr.update(visible=True, value=path)
    else:
        fil = gr.update(visible=True, value=path)
    return img, vid, aud, fil, hint


def _select_all(files: list[dict]):
    return gr.update(value=_rows(files, checked=True))


def _clear_sel(files: list[dict]):
    return gr.update(value=_rows(files, checked=False))


def _ask_delete(table, files: list[dict]):
    """点「删除选中」-> 校验选择并展开确认条。"""
    paths = _selected_paths(table, files)
    if not paths:
        gr.Warning("请先勾选要删除的文件")
        return gr.update(visible=False), gr.update()
    msg = f"⚠️ 确认删除选中的 **{len(paths)}** 个文件吗？此操作将**永久删除**，不可恢复。"
    return gr.update(visible=True), gr.update(value=msg)


def _do_delete(table, files: list[dict]):
    """确认后执行删除(仅限 outputs 目录内),然后刷新整个页面。"""
    paths = _selected_paths(table, files)
    root = base.OUTPUT_DIR.resolve()
    deleted = 0
    for p in paths:
        try:
            rp = Path(p).resolve()
            if root in rp.parents and rp.is_file():
                rp.unlink()
                deleted += 1
        except OSError:
            continue
    gr.Info(f"已删除 {deleted} 个文件")
    return _refresh()


def _cancel_delete():
    return gr.update(visible=False)


# ---------------------------------------------------------------------------
# 页面
# ---------------------------------------------------------------------------
def build_history_tab() -> None:
    """构建「历史结果」页。"""
    files0 = _scan()
    files_state = gr.State(files0)

    with gr.Row(equal_height=True):
        refresh_btn = gr.Button("🔄 刷新", variant="secondary", size="sm", scale=0, min_width=160)
        count_md = gr.Markdown(_count_text(files0), elem_classes="height-center")

    with gr.Row(equal_height=True):
        with gr.Column(scale=1):
            table = gr.Dataframe(
                value=_rows(files0),
                headers=_HEADERS,
                datatype=_DATATYPE,
                type="array",
                interactive=True,
                wrap=True,
                column_widths=["68px", None, "170px", "92px"],
                label="outputs 目录文件列表",
                show_label=True,
            )
            with gr.Row():
                all_btn = gr.Button("全选", scale=0)
                none_btn = gr.Button("取消全选", scale=0)
                del_btn = gr.Button("删除选中", variant="stop", scale=0)
            with gr.Group(visible=False) as confirm_group:
                confirm_md = gr.Markdown("")
                with gr.Row():
                    yes_btn = gr.Button("确认删除", variant="stop", scale=0)
                    no_btn = gr.Button("取消", scale=0)

        with gr.Column(scale=1):
            prev_image = gr.Image(label="预览", visible=False, interactive=False, height=420)
            prev_video = gr.Video(label="预览 / 播放", visible=False, interactive=False, height=420)
            prev_audio = gr.Audio(label="预览 / 播放", visible=False, interactive=False)
            prev_file = gr.File(label="下载 / 其他文件", visible=False, interactive=False)
            prev_hint = gr.Markdown(
                "点击左侧列表中的文件行即可在此预览 / 播放；图片、视频、音频组件右上角可下载。"
            )

    # 刷新 / 删除都会重置整页,共用同一组输出。
    reset_outputs = [
        table, files_state, count_md, confirm_group,
        prev_image, prev_video, prev_audio, prev_file, prev_hint,
    ]
    refresh_btn.click(_refresh, outputs=reset_outputs)
    table.select(
        _on_select,
        inputs=[files_state],
        outputs=[prev_image, prev_video, prev_audio, prev_file, prev_hint],
    )
    all_btn.click(_select_all, inputs=[files_state], outputs=[table])
    none_btn.click(_clear_sel, inputs=[files_state], outputs=[table])
    del_btn.click(_ask_delete, inputs=[table, files_state], outputs=[confirm_group, confirm_md])
    yes_btn.click(_do_delete, inputs=[table, files_state], outputs=reset_outputs)
    no_btn.click(_cancel_delete, outputs=[confirm_group])
