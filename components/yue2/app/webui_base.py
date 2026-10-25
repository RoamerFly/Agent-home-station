# -*- coding: utf-8 -*-
"""
webui_base.py
=============
模板项目的**公共层**:应用/作者信息、路径、文件类型判定、通用格式化工具，以及
公共的页头 HTML。

设计约定
--------
* 所有 UI 子模块（webui_history / webui_about / …）与主文件 webui.py 都只从这里
  导入公共内容，**不要反向导入 webui.py**，从而彻底避免循环导入。
* 本文件只依赖标准库与 comfyui_service（后者不依赖任何 webui_* 模块）。

以后基于本模板做新项目时，通常只需改动本文件顶部的「应用/作者信息」与
「文件类型」配置即可。
"""

import time
from pathlib import Path

import comfyui_service as cs

# ---------------------------------------------------------------------------
# 应用 / 作者信息（做新项目时改这里）
# ---------------------------------------------------------------------------
APP_NAME = "🔥 YuE2 最强 AI 音乐开源 整合包"
APP_DESC = "⚡ YuE2 一键生成音乐，翻唱、纯音乐、全新生成"
AUTHOR_NAME = "余子越Talk"
AUTHOR_URL = "https://space.bilibili.com/3493266750179909"
# 原始项目地址(可能有多个)。填写一个 URL 列表即可,「关于」页会逐条渲染成链接。
PROJECT_URLS = [
    ("YuE2", "https://github.com/multimodal-art-projection/YuE")
]


def project_links_md() -> str:
    """把 PROJECT_URLS 渲染成 Markdown 列表项(每个地址一行)。"""
    lines = []
    for item in PROJECT_URLS:
        if isinstance(item, (list, tuple)) and len(item) == 2:
            name, url = item
        else:
            name, url = item, item
        lines.append(f"  - [{name}]({url})")
    return "\n".join(lines) if lines else "  - （暂无）"

# webui.css 与本文件同目录。
CSS_PATH = str(Path(__file__).with_name("webui.css"))

# 与启动器约定的“哨兵”标记:检测到 ComfyUI 已在运行时,主程序把它单独打印到
# stdout 再退出;启动器(src/main/process-manager.ts)扫描到它即弹窗提示用户。
# ★ 修改此字符串必须同步修改 process-manager.ts 里的 COMFY_CONFLICT_SENTINEL。
COMFY_CONFLICT_SENTINEL = "[[YZY_COMFY_ALREADY_RUNNING]]"

# ---------------------------------------------------------------------------
# 目录（统一从 comfyui_service 取，避免各处重复推导）
# ---------------------------------------------------------------------------
OUTPUT_DIR: Path = cs.OUTPUT_DIR
INPUT_DIR: Path = cs.INPUT_DIR

# ---------------------------------------------------------------------------
# 文件类型
# ---------------------------------------------------------------------------
IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".webp", ".bmp", ".gif", ".avif", ".ico"}
VIDEO_EXTS = {".mp4", ".webm", ".mov", ".m4v", ".mkv", ".avi", ".ogv"}
AUDIO_EXTS = {".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a", ".opus"}

_KIND_ICON = {"image": "🖼️", "video": "🎬", "audio": "🎵", "other": "📄"}


def file_kind(path) -> str:
    """返回文件大类:image / video / audio / other。"""
    ext = Path(path).suffix.lower()
    if ext in IMAGE_EXTS:
        return "image"
    if ext in VIDEO_EXTS:
        return "video"
    if ext in AUDIO_EXTS:
        return "audio"
    return "other"


def kind_icon(kind: str) -> str:
    """大类对应的 emoji 图标。"""
    return _KIND_ICON.get(kind, "📄")


# ---------------------------------------------------------------------------
# 通用格式化
# ---------------------------------------------------------------------------
def fmt_ts(ts) -> str:
    """时间戳(秒) -> 'YYYY-MM-DD HH:MM:SS'。"""
    return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(ts)) if ts else "-"


def fmt_size(num_bytes) -> str:
    """字节数 -> 人类可读大小(B / KB / MB / GB / TB)。"""
    try:
        n = float(num_bytes)
    except (TypeError, ValueError):
        return "-"
    if n < 1024:
        return f"{int(n)} B"
    for unit in ("KB", "MB", "GB", "TB"):
        n /= 1024.0
        if n < 1024:
            return f"{n:.1f} {unit}"
    return f"{n:.1f} PB"


# ---------------------------------------------------------------------------
# 公共页头
# ---------------------------------------------------------------------------
def head_html() -> str:
    """页面顶部的品牌头(应用名 / 简介 / Packaged by 作者)。样式见 webui.css。"""
    return f"""
    <div class="app-head">
      <div class="app-head-title">{APP_NAME}</div>
      <div class="app-head-sub">
        {APP_DESC}
        <span class="app-head-sep">·</span>
        Packaged by
        <a href="{AUTHOR_URL}" target="_blank" rel="noopener">{AUTHOR_NAME}</a>
      </div>
    </div>
    """


# ---------------------------------------------------------------------------
# 侧边栏折叠/展开:注入到 <head> 的脚本(样式见 webui.css 第 5b 节)
# ---------------------------------------------------------------------------
# 纯 CSS 无法保存「折叠状态」,所以用一小段 JS:在 .sidebar-tabs 的导航卡片顶部
# 插入一个折叠按钮,点击切换 .collapsed 类,并用 localStorage 记住上次的选择。
# 默认折叠(只显示图标),想默认展开把 DEFAULT_COLLAPSED 改成 false 即可。
SIDEBAR_HEAD = """
<script>
(function () {
  var KEY = 'yz_sidebar_collapsed';
  var DEFAULT_COLLAPSED = true;
  function setup() {
    var tabs = document.querySelector('.sidebar-tabs');
    if (!tabs) return false;
    var card = tabs.querySelector('.tab-container[role="tablist"]');
    if (!card) return false;
    if (card.querySelector('.sidebar-toggle')) return true;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'sidebar-toggle';
    btn.setAttribute('aria-label', '折叠或展开侧边栏');
    btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" '
      + 'stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">'
      + '<path d="m11 17-5-5 5-5"/><path d="m18 17-5-5 5-5"/></svg>';
    var collapsed;
    try {
      var s = localStorage.getItem(KEY);
      collapsed = (s === null) ? DEFAULT_COLLAPSED : (s === '1');
    } catch (e) { collapsed = DEFAULT_COLLAPSED; }
    tabs.classList.toggle('collapsed', collapsed);
    btn.addEventListener('click', function () {
      collapsed = !collapsed;
      try { localStorage.setItem(KEY, collapsed ? '1' : '0'); } catch (e) {}
      tabs.classList.toggle('collapsed', collapsed);
    });
    card.appendChild(btn);
    return true;
  }
  function boot() {
    if (setup()) return;
    var obs = new MutationObserver(function () { if (setup()) obs.disconnect(); });
    obs.observe(document.documentElement, { childList: true, subtree: true });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else { boot(); }
})();
</script>
"""
