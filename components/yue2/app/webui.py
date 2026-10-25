# -*- coding: utf-8 -*-
"""
webui.py
========
gradio 主页面(模板项目入口)。启动顺序:
  1. 配置统一日志(控制台)
  2. 启动 ComfyUI 并等待就绪(阻塞,失败则不启动页面)
  3. 启动任务队列工作线程
  4. launch gradio
"""

import sys
import os
os.environ["GRADIO_ANALYTICS_ENABLED"] = "False"
import argparse
import logging
import gradio as gr
import comfyui_service as cs
from task_manager import manager, TaskStatus, build_queue_tab
import webui_base as base
import webui_history
import webui_about
import system_check

system_check.main()

logger = logging.getLogger("webui")

# ---------------------------------------------------------------------------
# 项目相关
# ---------------------------------------------------------------------------
# 下拉框未选择时的占位项:选它等价于「什么都没选」。
NO_SELECTION = "---点击选择---"

# 输出分辨率: 常用枚举 + 默认值 + 上限。
RESOLUTION_CHOICES = [480, 720, 1080, 1440, 1920, 2160, 2560, 3072, 4096]

# ★ 工作流文件名 + 节点路径。
WORKFLOWS: dict[str, dict] = {
    "SEEDVR2": {
        "workflow": "seedvr2_3b_int8_upscale_image.json",
        "input_image": ("1", "inputs", "image"),    # 输入图片文件名
        "scale": ("66:57", "inputs", "resize_type.multiplier"),    # 放大倍数
    },
    "text2music": {
        "workflow": "audio_yue2_text_to_music.json",
        "style": ("33:36", "inputs", "value"),
        "lyrics": ("33:37", "inputs", "value"),
        "seed": ("33:34", "inputs", "seed"),
    },
    "puremusic": {
        "workflow": "audio_yue2_pure_music.json",
        "style1": ("33:36", "inputs", "value"),
        "style2": ("57:47", "inputs", "style"),
        "seed1": ("33:34", "inputs", "seed"),
        "seed2": ("57:53", "inputs", "seed"),
    },
    "covermusic": {
        "workflow": "audio_yue2_music_cover.json",
        "audio": ("45", "inputs", "audio"),
        "style": ("33:25", "inputs", "style"),
        "lyrics": ("33:25", "inputs", "lyrics"),
        "seed": ("33:34", "inputs", "seed"),
    },
}

# 种子滑块的通用取值范围。
SEED_MIN, SEED_MAX = 0, 2147483647

# ★ Style / Lyrics 默认示例(格式与 suno 一致:Style 用逗号分隔的标签,
#   Lyrics 用 [Verse] / [Chorus] / [Bridge] / [Outro] 等段落标记)。
DEFAULT_T2M_STYLE = (
    "warm pop, acoustic guitar, soft female vocals, gentle piano, "
    "uplifting, intimate, mid-tempo, 92 bpm"
)
DEFAULT_T2M_LYRICS = """[Verse 1]
晚风穿过了窗台
带来一点点意外
你笑着对我说
今夜星光不错

[Chorus]
我们沿着这条路向前走
穿过万家灯火的温柔
哪怕风雨还在路口等候
也要把这首歌唱到尽头

[Verse 2]
城市灯光流成河
心事藏在沉默着
你牵着我的手
说一切都值得

[Chorus]
我们沿着这条路向前走
穿过万家灯火的温柔
哪怕风雨还在路口等候
也要把这首歌唱到尽头

[Bridge]
如果明天还很远
就让此刻多停留一点

[Outro]
我们沿着这条路向前走
这首歌永远不会结束"""

DEFAULT_PM_STYLE = (
    "ambient instrumental, cinematic piano, soft strings, warm pads, "
    "relaxing, peaceful, slow tempo, 70 bpm, no vocals"
)

DEFAULT_CM_STYLE = (
    "acoustic cover, emotional female vocal, fingerstyle guitar, "
    "minimal production, intimate, slow tempo, 75 bpm"
)
DEFAULT_CM_LYRICS = """[Verse]
月光洒满了肩膀
风很轻 像一场梦一样
你的名字在心上
悄悄发着光

[Chorus]
就让这旋律带我飞翔
穿过云层看见星光
无论未来多么远方
有你的歌就不会彷徨

[Outro]
就让这旋律带我飞翔
留在心里永远珍藏"""



EXAMPLES1 = [
    (
        "流行音乐",
        "[Intro]\n霓虹亮起在午夜的街\n风吹过熟悉的季节\n车窗倒映着城市的脸\n这一刻 好像时间慢了一些\n\n[Verse 1]\n你说今晚的月亮很特别\n像小时候见过的夏夜\n街角咖啡还亮着灯\n有人轻轻哼着旧唱片\n"
     ),
    (
        "流行, 抒情, 女声, 钢琴伴奏, 慢板, 温柔治愈",
        "[Verse 1]\n夜晚的风轻轻吹过\n带走了白天的喧嚣\n路灯把影子拉得很长\n我一个人走在熟悉的街道\n\n[Chorus]\n我在这里等着你\n月光洒满了街角\n哪怕时间再怎么漂泊\n也要把思念说给你听\n\n[Verse 2]\n手机里存着旧的消息\n舍不得删也不敢再看\n那些曾经说好的以后\n如今都变成了回忆里的片段\n\n[Chorus]\n我在这里等着你\n月光洒满了街角\n哪怕时间再怎么漂泊\n也要把思念说给你听\n\n[Bridge]\n如果有一天风停了\n如果有一天你也累了\n请记得回头看看\n这里一直为你留着灯\n\n[Chorus]\n我在这里等着你\n月光洒满了街角\n哪怕时间再怎么漂泊\n也要把思念说给你听"
    ),
    (
        "摇滚, 电吉他, 男声, 高能, 鼓点强劲, 燃向励志",
        "[Verse 1]\n点燃心中的火焰\n冲破黑暗的边界\n多少次跌倒又爬起来\n谁说失败就不能再来\n\n[Chorus]\n我们永不停歇\n直到梦想实现\n用尽全力呐喊出声\n这是属于我们的时代\n\n[Verse 2]\n汗水浸湿了衣衫\n脚步却从未停歇\n别人笑我太过疯癫\n我却笑他们不懂信念\n\n[Chorus]\n我们永不停歇\n直到梦想实现\n用尽全力呐喊出声\n这是属于我们的时代\n\n[Bridge]\n风雨再大又如何\n我的心里有团火\n没有到不了的明天\n只有不敢迈出的步伐\n\n[Chorus]\n我们永不停歇\n直到梦想实现\n用尽全力呐喊出声\n这是属于我们的时代"
    ),
    (
        "民谣, 吉他弹唱, 温柔, 叙事感, 清新自然",
        "[Verse 1]\n小镇的午后阳光正好\n老屋檐下猫儿打盹\n巷口的老树又长高了一些\n年少的我们还没走远\n\n[Chorus]\n时光慢慢地走\n我们不说再见\n就把故事留在这个夏天\n留在无忧无虑的从前\n\n[Verse 2]\n邮筒里还装着旧信件\n写满了那年的梦和期待\n如今大家都各奔东西\n却依然记得彼此的名字\n\n[Chorus]\n时光慢慢地走\n我们不说再见\n就把故事留在这个夏天\n留在无忧无虑的从前\n\n[Bridge]\n也许多年以后再相见\n皱纹爬上了彼此的脸\n可那些单纯的笑容啊\n永远都不会改变\n\n[Chorus]\n时光慢慢地走\n我们不说再见\n就把故事留在这个夏天\n留在无忧无虑的从前"
    ),
    (
        "电子, EDM, 高潮迭起, 舞曲, 节奏明快, 俱乐部氛围",
        "[Verse 1]\n灯光闪烁节奏跳动\n人群随着音乐摇摆\n心跳跟着鼓点加速\n今晚属于我们自己\n\n[Chorus]\n今夜不眠不休\n尽情释放自由\n让音乐带我们飞起来\n忘掉所有的烦恼忧愁\n\n[Verse 2]\n霓虹灯下的脸庞\n每个人都在尽情释放\n不需要理由不需要顾虑\n只要跟着节奏往前冲\n\n[Chorus]\n今夜不眠不休\n尽情释放自由\n让音乐带我们飞起来\n忘掉所有的烦恼忧愁\n\n[Bridge]\n当节拍逐渐升高\n当灯光开始闪耀\n就让这一刻定格\n成为永远的记号\n\n[Chorus]\n今夜不眠不休\n尽情释放自由\n让音乐带我们飞起来\n忘掉所有的烦恼忧愁"
    ),
    (
        "说唱, 嘻哈, 节奏感强, 男声, 低音贝斯, 街头风格",
        "[Verse 1]\n街头的故事我来讲\n汗水浇灌着梦想\n从小巷子里走出来的我\n没有谁能替我扛\n\n[Chorus]\n不服就往前闯\n没有谁能阻挡\n这条路我自己走\n每一步都算数\n\n[Verse 2]\n别人质疑我的选择\n我用成绩回答一切\n深夜的灯光见证努力\n这是属于我的节拍\n\n[Chorus]\n不服就往前闯\n没有谁能阻挡\n这条路我自己走\n每一步都算数\n\n[Bridge]\n曾经的质疑都成了动力\n现在站在这里不后悔\n未来的路还很长\n我会一直往前追\n\n[Chorus]\n不服就往前闯\n没有谁能阻挡\n这条路我自己走\n每一步都算数"
    ),
    (
        "古风, 中国风, 笛子, 女声, 悠扬婉转, 意境空灵",
        "[Verse 1]\n烟雨江南一叶舟\n青石板上听风流\n谁家庭院花正开\n一曲离殇诉衷肠\n\n[Chorus]\n此情此景难忘怀\n愿与君共白头\n纵使山高水又长\n我心依旧为你留\n\n[Verse 2]\n月下独酌影成双\n笔墨写不尽思量\n远山含黛水悠悠\n何时才能再相逢\n\n[Chorus]\n此情此景难忘怀\n愿与君共白头\n纵使山高水又长\n我心依旧为你留\n\n[Bridge]\n一曲笛声穿云去\n带着思念到天涯\n等到梨花开满枝\n愿你归来认得家\n\n[Chorus]\n此情此景难忘怀\n愿与君共白头\n纵使山高水又长\n我心依旧为你留"
    ),
    (
        "爵士, 慢板, 萨克斯风, 复古, 夜晚情调, 浪漫慵懒",
        "[Verse 1]\n昏黄的灯光下\n你的笑容如此温暖\n杯中的酒还未凉\n我们的故事才刚刚开始\n\n[Chorus]\n让我们慢慢跳舞\n这一刻永远停留\n不需要太多言语\n眼神已经说明一切\n\n[Verse 2]\n萨克斯风轻轻响起\n像是在诉说心事\n城市的喧嚣都远去\n只剩下你我的呼吸\n\n[Chorus]\n让我们慢慢跳舞\n这一刻永远停留\n不需要太多言语\n眼神已经说明一切\n\n[Bridge]\n如果时间可以暂停\n我愿停在这个夜晚\n和你一起听着音乐\n忘记明天忘记昨天\n\n[Chorus]\n让我们慢慢跳舞\n这一刻永远停留\n不需要太多言语\n眼神已经说明一切"
    ),
    (
        "乡村, 吉他, 轻快, 叙事, 公路旅行, 阳光质感",
        "[Verse 1]\n开着老卡车驶向远方\n路边的麦田金黄一片\n收音机里放着老歌\n窗外的风景不断后退\n\n[Chorus]\n自由的风吹过脸庞\n这就是我想要的生活\n没有地图也没关系\n只要方向是向前的\n\n[Verse 2]\n路过一个又一个小镇\n遇见陌生又友善的笑容\n生活original本该如此简单\n不必急着去往哪里\n\n[Chorus]\n自由的风吹过脸庞\n这就是我想要的生活\n没有地图也没关系\n只要方向是向前的\n\n[Bridge]\n也许有一天会停下来\n找个地方安顿下来\n但现在就让我继续走\n看看世界有多辽阔\n\n[Chorus]\n自由的风吹过脸庞\n这就是我想要的生活\n没有地图也没关系\n只要方向是向前的"
    ),
    (
        "R&B, 灵魂乐, 慵懒, 女声, 律动感强, 深情款款",
        "[Verse 1]\n夜深人静的时候\n想起了你的温柔\n手机屏幕亮了又暗\n却始终没有勇气拨出\n\n[Chorus]\n爱像潮水涌上心头\n我愿为你停留\n哪怕只是一个转身\n也要把你记在心头\n\n[Verse 2]\n我们之间的距离\n像是隔着一整个季节\n想说的话堆积成山\n却不知从何说起\n\n[Chorus]\n爱像潮水涌上心头\n我愿为你停留\n哪怕只是一个转身\n也要把你记在心头\n\n[Bridge]\n也许爱情就是这样\n明知结局还是飞蛾扑火\n如果有机会重来一次\n我还是会选择爱你\n\n[Chorus]\n爱像潮水涌上心头\n我愿为你停留\n哪怕只是一个转身\n也要把你记在心头"
    )
]

EXAMPLES2 = [
    "钢琴, 纯音乐, 治愈, 冥想, 轻柔, no vocals, instrumental",
    "弦乐, 电影配乐, 史诗感, 恢弘大气, 管弦乐, no vocals, instrumental",
    "Modern handpan instrumental, contemporary chill electronic music, catchy handpan hooks, deep rounded electronic bass, crisp organic percussion, subtle downtempo beats, atmospheric synthesizers, minimalist groove, hypnotic rhythmic layers, warm analog textures, stylish and futuristic mood, immersive stereo production, relaxing, no vocals, no singing, purely instrumental.",
    "Lo-Fi, 爵士, 慵懒, 咖啡馆氛围, 放松, no vocals, instrumental beats",
    "Romantic handpan instrumental, beautiful emotional handpan melodies, delicate piano accompaniment, soft acoustic percussion, warm ambient strings, gentle rhythmic pulse, golden sunset atmosphere, dreamy cinematic harmonies, intimate and nostalgic mood, elegant melodic progression, spacious reverb, delicate dynamic build, peaceful seaside evening feeling, melodic and deeply touching, no vocals, no singing, purely instrumental.",
    "新世纪, 自然音效, 森林雨声, 冥想音乐, 疗愈, no vocals, instrumental ambient",
    "轻音乐, 吉他指弹, 清新, 午后阳光, 温暖, no vocals, instrumental",
    "电子, Synthwave, 复古未来感, 霓虹氛围, 节奏律动, no vocals, instrumental",
    "古典, 大提琴独奏, 忧伤深情, 空灵, no vocals, instrumental",
    "中国风, 古筝与笛子, 空灵悠远, 禅意, 国风意境, no vocals, instrumental",
    "氛围音乐, 太空感, 悬浮漂浮, 极简主义, 深邃, no vocals, instrumental ambient",
    "爵士钢琴三重奏, 夜晚情调, 浪漫复古, 即兴律动, no vocals, instrumental jazz",
]


# ---------------------------------------------------------------------------
# 公共小工具
# ---------------------------------------------------------------------------
def _resolve_workflow(model: str, table: dict[str, dict]):
    """
    校验 workflow, 返回 (配置dict, 错误消息)。其一为 None:
    """
    if not model or model == NO_SELECTION:
        return None, "⚠️ 请先传入工作流名称。"
    cfg = table.get(model)
    if cfg is None:
        return None, f"🚧 「{model}」工作流尚未配置。"
    return cfg, None


def _pick_output(task, kind: str) -> str | None:
    """ 从任务文件里挑出指定类型(image / video ...)的文件路径，优先取正式产物。"""
    outputs = [f for f in task.files if f.get("folder_type", "output") == "output"]
    others = [f for f in task.files if f not in outputs]
    for item in outputs + others:
        path = cs.output_file_path(item)
        if path.exists() and base.file_kind(path) == kind:
            return str(path)
    return None


# ---------------------------------------------------------------------------
# 检测输入，并上传图片到 ComfyUI
# ---------------------------------------------------------------------------
def _prepare_image_task(model: str, image_path: str, scale):
    """
    「图片放大」的公共前置:校验 -> 上传图片 -> 组装 (工作流名, patch, label)。
    scale / resolution 两个控件都会传进来,按所选模型的 param_kind 取用对应那个。
    vram 为「显存预算」选出的权重文件名,仅带 model_path 的模型(SeedVR2)会用到。
    返回 (workflow_name, patch, label, error_msg)。error_msg 非 None 时前面几项为 None。
    """
    cfg, err = _resolve_workflow(model, WORKFLOWS)
    if err:
        return None, None, None, err
    if not image_path:
        return None, None, None, "⚠️ 请先上传一张图片。"
    if err:
        return None, None, None, err

    try:
        uploaded_name = cs.upload_image(image_path, overwrite=True)
    except Exception as e:
        logger.exception("上传图片失败")
        return None, None, None, f"❌ 上传图片到 ComfyUI 失败:{e}"

    patch = {cfg["input_image"]: uploaded_name, cfg["scale"]: scale}
    label = f"{model} 图片 x{scale}"
    return cfg["workflow"], patch, label, None


def on_image_generate(model: str, image_path: str, scale):
    """
    立即执行图片放大:校验 -> 上传 -> 前台执行工作流(run_now 内有执行锁,全局串行)。
    返回 输出图片 + 原图/放大图对比(ImageSlider) + 状态提示。
    """
    workflow, patch, label, err = _prepare_image_task(model, image_path, scale)
    if err:
        return None, None, err

    try:
        task = manager.run_now(workflow, patch, label=label)
    except RuntimeError as e:
        return None, None, f"🚫 {e}"

    if task.status != TaskStatus.DONE:
        return None, None, f"❌ 生成失败:{task.error}"

    out_path = _pick_output(task, "image")
    if not out_path:
        return None, None, "❌ 生成完成但未找到输出图片,请查看控制台日志。"

    return out_path, (image_path, out_path), f"✅ {label} 放大完成。"


def on_image_enqueue(model: str, image_path: str, scale):
    """把图片放大任务加入队列(后台串行执行),立即返回提示。"""
    workflow, patch, label, err = _prepare_image_task(model, image_path, scale)
    if err:
        return err
    manager.submit(workflow, patch, label=label)
    return f"✅ 已加入队列:{label}。可到「队列详情」查看进度。"


# ---------------------------------------------------------------------------
# 「歌曲创作」业务逻辑
# ---------------------------------------------------------------------------
def _validate_style_lyrics(style: str, lyrics: str) -> "str | None":
    """校验 Style / Lyrics 是否已填写,返回错误提示;都合法则返回 None。"""
    if not style or not style.strip():
        return "⚠️ 请先填写 Style 风格描述。"
    if not lyrics or not lyrics.strip():
        return "⚠️ 请先填写 Lyrics 歌词。"
    return None


def on_text2music_generate(style: str, lyrics: str, seed):
    """立即执行歌曲创作:校验 -> 前台执行工作流 -> 返回音频 + 状态提示。"""
    cfg, err = _resolve_workflow("text2music", WORKFLOWS)
    if err:
        yield None, err
        return
    err = _validate_style_lyrics(style, lyrics)
    if err:
        yield None, err
        return

    seed = int(seed)
    patch = {cfg["style"]: style, cfg["lyrics"]: lyrics, cfg["seed"]: seed}
    label = f"歌曲创作 seed={seed}"
    logger.info("歌曲创作任务开始:%s", label)
    yield None, f"🎼 {label},歌曲生成中,请稍候……"

    try:
        task = manager.run_now(cfg["workflow"], patch, label=label)
    except RuntimeError as e:
        yield None, f"🚫 {e}"
        return

    if task.status != TaskStatus.DONE:
        yield None, f"❌ 生成失败:{task.error}"
        return

    out_path = _pick_output(task, "audio")
    if not out_path:
        yield None, "❌ 生成完成但未找到输出音频,请查看控制台日志。"
        return

    logger.info("歌曲创作任务完成:%s -> %s", label, out_path)
    yield out_path, f"✅ {label} 生成完成。"


def on_text2music_enqueue(style: str, lyrics: str, seed):
    """把歌曲创作任务加入队列(后台串行执行),立即返回提示。"""
    cfg, err = _resolve_workflow("text2music", WORKFLOWS)
    if err:
        return err
    err = _validate_style_lyrics(style, lyrics)
    if err:
        return err

    seed = int(seed)
    patch = {cfg["style"]: style, cfg["lyrics"]: lyrics, cfg["seed"]: seed}
    label = f"歌曲创作 seed={seed}"
    manager.submit(cfg["workflow"], patch, label=label)
    logger.info("歌曲创作任务入队:%s", label)
    return f"✅ 已加入队列:{label}。可到「队列详情」查看进度。"


# ---------------------------------------------------------------------------
# 「纯音乐」业务逻辑
# ---------------------------------------------------------------------------
def on_puremusic_generate(style: str, seed):
    """立即执行纯音乐生成:style 同时写入 style1 / style2,seed 同时写入 seed1 / seed2。"""
    cfg, err = _resolve_workflow("puremusic", WORKFLOWS)
    if err:
        yield None, err
        return
    if not style or not style.strip():
        yield None, "⚠️ 请先填写 Style 风格描述。"
        return

    seed = int(seed)
    patch = {cfg["style1"]: style, cfg["style2"]: style, cfg["seed1"]: seed, cfg["seed2"]: seed}
    label = f"纯音乐 seed={seed}"
    logger.info("纯音乐任务开始:%s", label)
    yield None, f"🪗 {label},纯音乐生成中,请稍候……"

    try:
        task = manager.run_now(cfg["workflow"], patch, label=label)
    except RuntimeError as e:
        yield None, f"🚫 {e}"
        return

    if task.status != TaskStatus.DONE:
        yield None, f"❌ 生成失败:{task.error}"
        return

    out_path = _pick_output(task, "audio")
    if not out_path:
        yield None, "❌ 生成完成但未找到输出音频,请查看控制台日志。"
        return

    logger.info("纯音乐任务完成:%s -> %s", label, out_path)
    yield out_path, f"✅ {label} 生成完成。"


def on_puremusic_enqueue(style: str, seed):
    """把纯音乐任务加入队列(后台串行执行),立即返回提示。"""
    cfg, err = _resolve_workflow("puremusic", WORKFLOWS)
    if err:
        return err
    if not style or not style.strip():
        return "⚠️ 请先填写 Style 风格描述。"

    seed = int(seed)
    patch = {cfg["style1"]: style, cfg["style2"]: style, cfg["seed1"]: seed, cfg["seed2"]: seed}
    label = f"纯音乐 seed={seed}"
    manager.submit(cfg["workflow"], patch, label=label)
    logger.info("纯音乐任务入队:%s", label)
    return f"✅ 已加入队列:{label}。可到「队列详情」查看进度。"


# ---------------------------------------------------------------------------
# 「歌曲翻唱」业务逻辑
# ---------------------------------------------------------------------------
def _validate_covermusic(audio_path: str, style: str, lyrics: str) -> "str | None":
    if not audio_path:
        return "⚠️ 请先上传待翻唱的音乐。"
    return _validate_style_lyrics(style, lyrics)


def on_covermusic_generate(audio_path: str, style: str, lyrics: str, seed):
    """立即执行歌曲翻唱:校验 -> 上传待翻唱音乐 -> 前台执行工作流。"""
    cfg, err = _resolve_workflow("covermusic", WORKFLOWS)
    if err:
        yield None, err
        return
    err = _validate_covermusic(audio_path, style, lyrics)
    if err:
        yield None, err
        return

    logger.info("歌曲翻唱:正在上传待翻唱音乐 %s", audio_path)
    yield None, "📤 正在上传待翻唱音乐……"
    try:
        uploaded_name = cs.upload_image(audio_path, overwrite=True)
    except Exception as e:
        logger.exception("上传待翻唱音乐失败")
        yield None, f"❌ 上传音乐到 ComfyUI 失败:{e}"
        return

    seed = int(seed)
    patch = {cfg["audio"]: uploaded_name, cfg["style"]: style, cfg["lyrics"]: lyrics, cfg["seed"]: seed}
    label = f"歌曲翻唱 seed={seed}"
    logger.info("歌曲翻唱任务开始:%s", label)
    yield None, f"🎤 {label},翻唱生成中,请稍候……"

    try:
        task = manager.run_now(cfg["workflow"], patch, label=label)
    except RuntimeError as e:
        yield None, f"🚫 {e}"
        return

    if task.status != TaskStatus.DONE:
        yield None, f"❌ 生成失败:{task.error}"
        return

    out_path = _pick_output(task, "audio")
    if not out_path:
        yield None, "❌ 生成完成但未找到输出音频,请查看控制台日志。"
        return

    logger.info("歌曲翻唱任务完成:%s -> %s", label, out_path)
    yield out_path, f"✅ {label} 翻唱完成。"


def on_covermusic_enqueue(audio_path: str, style: str, lyrics: str, seed):
    """把歌曲翻唱任务加入队列(后台串行执行):先上传音乐,再入队。"""
    cfg, err = _resolve_workflow("covermusic", WORKFLOWS)
    if err:
        return err
    err = _validate_covermusic(audio_path, style, lyrics)
    if err:
        return err

    try:
        uploaded_name = cs.upload_image(audio_path, overwrite=True)
    except Exception as e:
        logger.exception("上传待翻唱音乐失败")
        return f"❌ 上传音乐到 ComfyUI 失败:{e}"

    seed = int(seed)
    patch = {cfg["audio"]: uploaded_name, cfg["style"]: style, cfg["lyrics"]: lyrics, cfg["seed"]: seed}
    label = f"歌曲翻唱 seed={seed}"
    manager.submit(cfg["workflow"], patch, label=label)
    logger.info("歌曲翻唱任务入队:%s", label)
    return f"✅ 已加入队列:{label}。可到「队列详情」查看进度。"


# ---------------------------------------------------------------------------
# 「图片放大」页
# ---------------------------------------------------------------------------
def _build_image_upscale_tab():
    with gr.Row(equal_height=True):
        model_select = gr.Dropdown(label="模型选择", choices=[NO_SELECTION, "SEEDVR2"], value=NO_SELECTION)
        scale_in = gr.Slider(label="放大倍数", minimum=1, maximum=8, step=1, value=4, visible=True)

    with gr.Row(equal_height=True):
        with gr.Column(scale=1):
            image_in = gr.Image(label="输入图片", type="filepath", height=360)
            with gr.Row(equal_height=True):
                run_btn = gr.Button("立即执行", variant="primary")
                queue_btn = gr.Button("加入队列", variant="secondary")
            msg = gr.Markdown("")
        with gr.Column(scale=1):
            image_out = gr.Image(label="输出图片", type="filepath", height=360, interactive=False)

    compare = gr.ImageSlider(label="原图 / 放大图 对比", type="filepath", interactive=False)

    params = [model_select, image_in, scale_in]
    run_btn.click(on_image_generate, inputs=params, outputs=[image_out, compare, msg])
    queue_btn.click(on_image_enqueue, inputs=params, outputs=[msg])


def _build_text2music_tab():
    with gr.Row(equal_height=True):
        with gr.Column(scale=1):
            style = gr.TextArea(label="Style 风格", lines=5, max_lines=5, value=DEFAULT_T2M_STYLE)
            lyrics = gr.TextArea(label="Lyrics 歌词", lines=15, max_lines=15, value=DEFAULT_T2M_LYRICS)
            with gr.Row(equal_height=True):
                run_btn = gr.Button("立即执行", variant="primary")
                queue_btn = gr.Button("加入队列", variant="secondary")
        with gr.Column(scale=1):
            seed = gr.Slider(value=1, minimum=SEED_MIN, maximum=SEED_MAX, step=1,
                             label="种子", info="修改后再次生成可得到不同的结果", interactive=True)
            output_audio = gr.Audio(label="输出结果", type="filepath", interactive=False)
    msg = gr.Markdown("")

    def on_select(evt: gr.SelectData, data):
        row_index = evt.index[0]  # 被点击的行号
        s = data.iloc[row_index, 0]
        l = data.iloc[row_index, 1]
        return s, l

    table = gr.Dataframe(
        label="提示词示例（点击后自动回填到上方输入框）",
        headers=["Style 风格", "Lyrics 歌词"],
        value=EXAMPLES1,
        datatype=["str", "str"],
        interactive=False,  # 表格本身不可编辑，只用于点击选择
        wrap=True,
        max_chars=40,
        elem_classes="table-centered",
    )
    table.select(fn=on_select, inputs=table, outputs=[style, lyrics])

    params = [style, lyrics, seed]
    run_btn.click(on_text2music_generate, inputs=params, outputs=[output_audio, msg])
    queue_btn.click(on_text2music_enqueue, inputs=params, outputs=[msg])


def _build_puremusic_tab():
    with gr.Row(equal_height=True):
        with gr.Column(scale=1):
            style = gr.TextArea(label="Style 风格", lines=10, max_lines=10, value=DEFAULT_PM_STYLE)
            with gr.Row(equal_height=True):
                run_btn = gr.Button("立即执行", variant="primary")
                queue_btn = gr.Button("加入队列", variant="secondary")
        with gr.Column(scale=1):
            seed = gr.Slider(value=1, minimum=SEED_MIN, maximum=SEED_MAX, step=1,
                             label="种子", info="修改后再次生成可得到不同的结果", interactive=True)
            output_audio = gr.Audio(label="输出结果", type="filepath", interactive=False)
    msg = gr.Markdown("")

    def on_select(evt: gr.SelectData, data):
        row_index = evt.index[0]  # 被点击的行号
        s = data.iloc[row_index, 0]
        return s

    table = gr.Dataframe(
        label="提示词示例（点击后自动回填到上方输入框）",
        headers=["Style 风格"],
        value=EXAMPLES2,
        datatype=["str"],
        interactive=False,  # 表格本身不可编辑，只用于点击选择
        wrap=True,
        max_chars=90,
        elem_classes="table-centered",
    )
    table.select(fn=on_select, inputs=table, outputs=[style])

    params = [style, seed]
    run_btn.click(on_puremusic_generate, inputs=params, outputs=[output_audio, msg])
    queue_btn.click(on_puremusic_enqueue, inputs=params, outputs=[msg])


def _build_covermusic_tab():
    with gr.Row(equal_height=True):
        with gr.Column(scale=1):
            audio  = gr.Audio(label="待翻唱音乐", type="filepath", interactive=True)

            style = gr.TextArea(label="Style 风格", lines=5, max_lines=5, value=DEFAULT_CM_STYLE)
            with gr.Row(equal_height=True):
                run_btn = gr.Button("立即执行", variant="primary")
                queue_btn = gr.Button("加入队列", variant="secondary")
        with gr.Column(scale=1):
            lyrics = gr.TextArea(label="Lyrics 歌词", lines=15, max_lines=15, value=DEFAULT_CM_LYRICS)
            seed = gr.Slider(value=1, minimum=SEED_MIN, maximum=SEED_MAX, step=1,
                             label="种子", info="修改后再次生成可得到不同的结果", interactive=True)
            output_audio = gr.Audio(label="输出结果", type="filepath", interactive=False)
    msg = gr.Markdown("")

    params = [audio, style, lyrics, seed]
    run_btn.click(on_covermusic_generate, inputs=params, outputs=[output_audio, msg])
    queue_btn.click(on_covermusic_enqueue, inputs=params, outputs=[msg])


def _build_readme_tab():
    readme = r"""

    一次生成主要由两部分决定：**风格 style** 和 **歌词 lyrics**。写得越具体，结果越稳。
    不知道怎么写？可使用任意 AI 帮写，提示词格式和 suno 音乐完全一致。

    ## 一、风格 style 怎么写

    把下面这些信息用**逗号分隔**塞进一句话里，越具体越稳定：

    | 维度 | 示例 |
    |---|---|
    | 语言（放最前面最稳） | `Mandarin` / `Cantonese` / `English` / `Japanese` |
    | 曲风 | `warm piano pop` / `city pop` / `lo-fi` / `rock ballad` / `folk` |
    | 人声 | `expressive female voice` / `intimate male vocal` / `airy vocal` |
    | 乐器 | `acoustic piano` / `guitar` / `saxophone` / `synths` / `drums` |
    | 情绪 | `uplifting` / `melancholic` / `dreamy` / `warm` |
    | 速度 | `88 BPM` / `relaxed groove` |
    | 排除项 | `no guitar`（想去掉某乐器时很有用） |

    > **示例**
    > ```
    > Mandarin, warm piano pop, expressive female voice, acoustic piano,
    > rounded bass and light drums, memorable melody, 88 BPM
    > ```

    小贴士：**语言标签放最前面**；

    ## 二、歌词 lyrics 怎么写

    1. 用 **段落标签** 组织结构：`[Verse]`（主歌）、`[Chorus]`（副歌）、`[Bridge]`（过渡）。
    2. **段落之间空一行**。
    3. 每段别太长——一段大约对应 30 秒。
    4. 建议**从 `[Verse]` 或 `[Chorus]` 开始**（`[intro]` 相对不稳定）。

    > **示例**
    > ```
    > [Verse]
    > 晚风轻轻吹过窗前
    > 你留下的笑还在昨天
    >
    > [Chorus]
    > 让这首歌陪你走远
    > 每一条路都因你而甜
    > ```


    ## 三、想做“纯音乐”（没有歌词）

    > **纯音乐风格示例**
    > ```
    > instrumental, no vocals, 纯音乐 无人声, 舒缓的钢琴独奏, 温柔抒情, 70 BPM
    > ```

    > 说明：YuE2 本身不支持纯音乐，我是通过去除人声以后，再次翻唱的方式生成的，详情可见 ComfyUI 工作流。
    > 想让乐器更“抢戏”，可在风格里强调主奏乐器（如 `主奏钢琴`、`萨克斯 solo`、`电吉他 solo` 等）。

    ## 四、翻唱怎么用

    翻唱＝**保留旋律、换新风格 / 新词重唱**。：

    1. **给它一段旋律或者歌曲**。
    2. **填目标风格 + 目标歌词**（必做），点“生成翻唱”，目标风格可以和原来歌曲的风格往前不同。

    ## 六、常见问题
    - **中文/粤语**：把 `Mandarin` / `Cantonese` 放在风格最前面。
    """
    return gr.Markdown(readme)


# ---------------------------------------------------------------------------
# gradio 页面
# ---------------------------------------------------------------------------
def build_ui():
    with gr.Blocks(title=base.APP_NAME, head=base.SIDEBAR_HEAD) as demo:
        gr.HTML(base.head_html())
        # elem_classes="sidebar-tabs":把标签页渲染成左侧竖排导航栏(样式见 webui.css)。
        # 每个标签名以 emoji 开头:折叠态只显示这个 emoji(图标),展开态显示完整标签。
        # emoji 从哪找:Windows 按 Win + . 调出表情面板;或 https://emojipedia.org。
        with gr.Tabs(elem_classes="sidebar-tabs"):
            with gr.Tab("📀 歌曲创作"):
                _build_text2music_tab()

            with gr.Tab("🖼️ 图片放大", visible=False):
                _build_image_upscale_tab()

            with gr.Tab("🪗 纯音乐"):
                _build_puremusic_tab()
            with gr.Tab("✍️ 歌曲翻唱"):
                _build_covermusic_tab()
            with gr.Tab("🏆 最佳实践"):
                _build_readme_tab()
            with gr.Tab("🗂️ 创作记录"):
                webui_history.build_history_tab()
            with gr.Tab("⌛ 队列详情"):
                build_queue_tab()
            with gr.Tab("✉️ 关于"):
                webui_about.build_about_tab()

    return demo


# ---------------------------------------------------------------------------
# 命令行参数
# ---------------------------------------------------------------------------
def parse_args(argv=None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="app 应用",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    p.add_argument("--server_name", default="127.0.0.1", help="gradio 监听 IP。设 0.0.0.0 可被局域网访问。")
    p.add_argument("--server_port", type=int, default=9000, help="gradio 端口。")
    p.add_argument("--share", action="store_true", help="不生成一个公网可访问的 gradio 临时链接。")
    p.add_argument("--inbrowser", action="store_true", help="启动后不自动打开浏览器。")

    # --- 其它 ---
    p.add_argument("--log-level", default="INFO", choices=["DEBUG", "INFO", "WARNING", "ERROR"], help="日志级别。")
    return p.parse_args(argv)


# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------
def main(argv=None):
    args = parse_args(argv)

    # 1) 统一日志:ComfyUI + 本程序都打到这个控制台。
    cs.setup_logging(level=getattr(logging, args.log_level))

    # 1.5) 把 start.conf 里的目录类相对路径(模型/缓存)统一解析成绝对路径,
    #      保证本程序与 ComfyUI 子进程(工作目录不同)指向同一位置。
    #      对通过启动器/start.bat/start.sh 的各种启动方式都生效。
    cs.normalize_dir_env()

    # 2) 启动 ComfyUI 并等待就绪。
    #    若启动前就检测到已有 ComfyUI 在运行,则不再继续启动流程:打印哨兵标记
    #    通知启动器弹窗提示用户,然后直接退出(此时提示不能放在 gradio 里, 因为页面根本不会起来)。
    logger.info("正在启动 ComfyUI(%s)……", cs.BASE)
    try:
        if sys.platform.startswith('linux'):
            cs.start_comfyui(wait=True, reuse_existing=True)
        else:
            cs.start_comfyui(wait=True, reuse_existing=False)
            pass
    except cs.ComfyUIAlreadyRunningError as e:
        logger.error("%s", e)
        # 单独一行打印哨兵,并 flush,确保启动器能立刻扫描到。
        print(base.COMFY_CONFLICT_SENTINEL, flush=True)
        sys.stdout.flush()
        sys.exit(3)

    # 3) 启动任务队列工作线程。
    manager.start()

    # 4) 启动
    logger.info("正在启动页面")
    demo = build_ui()
    demo.queue()  # 允许长任务占用请求而不阻塞其它人查看
    demo.launch(
        server_name=args.server_name,
        server_port=args.server_port,
        inbrowser=args.inbrowser,
        share=args.share,
        css_paths=base.CSS_PATH,
        theme=gr.themes.Ocean(),
        allowed_paths=[str(base.OUTPUT_DIR), str(base.INPUT_DIR)],
    )


if __name__ == "__main__":
    main()
