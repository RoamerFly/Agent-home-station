# -*- coding: utf-8 -*-
"""
comfyui_service.py
=================
通用层:负责启动、守护 ComfyUI,并封装所有与 ComfyUI 通信的通用方法。

这里的代码与具体业务(图像 / 视频 / 音频 / 文字)无关,换任何前端页面通常都不需要
改动。各个 Gradio 页面只需 import 本文件提供的函数即可。

设计约定
--------
* 日志:ComfyUI 子进程的 stdout/stderr 全部转发到本模块的 logger,
  统一打印到「运行 Gradio 的控制台」,不再单独写日志文件。
  上层只要在入口处调用一次 setup_logging() 即可看到 ComfyUI + 本程序的全部日志。
* 工作流:load_workflow("xxx.json") -> dict,改完 key 后交给 run_workflow() 提交。

主要入口
--------
  setup_logging()            配置根 logger(控制台),入口调用一次
  start_comfyui()            启动并等待就绪
  stop_comfyui()             关闭
  is_alive()                 存活检测
  load_workflow(name)        读取 workflows_api/ 下的工作流 json,返回可修改的 dict
  run_workflow(workflow)     提交一个工作流并等待执行完成,返回 outputs
  queue_prompt / get_history / get_view_file / interrupt   更细粒度的通用 API

依赖: requests
"""

import os
import re
import sys
import json
import time
import uuid
import copy
import errno
import socket
import atexit
import logging
import threading
import subprocess
import urllib.parse
from pathlib import Path

import requests

# 匹配 ANSI 颜色 / 光标控制转义序列,转发日志前先剥掉,免得控制台里全是 \x1b[92m。
_ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")

# ============================================================================
# 日志
# ============================================================================
# 全模块共用一个 logger。ComfyUI 子进程的每一行输出会通过它转发出去,
# 于是「ComfyUI 日志」和「本程序日志」就统一在同一个控制台里了。
logger = logging.getLogger("comfyui")


class ComfyUIAlreadyRunningError(RuntimeError):
    """启动前检测到已有 ComfyUI 在运行(端口被占用且 API 可正常响应)。

    出现它意味着不应该再启动一个新的 ComfyUI —— 上层(webui.py)应捕获它、
    通知启动器弹窗提示用户,然后退出。
    """


def setup_logging(level: int = logging.INFO) -> None:
    """
    配置根 logger,让所有日志统一打印到控制台。请在程序入口(webui.py)只调用一次。

    这里配置的是 root logger,因此本模块、task_manager、gradio 等所有模块的日志
    都会走同一套格式、同一个 stdout。重复调用不会重复添加 handler。
    """
    # Windows 控制台默认是 GBK,ComfyUI 日志里的 emoji / ℹ️ 等字符会让 GBK 编码崩溃。
    # 把本进程的 stdout/stderr 重新配置成 UTF-8,遇到极端字符也用 replace 兜底而不是抛异常。
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")  # Python 3.7+
        except Exception:
            pass

    root = logging.getLogger()
    root.setLevel(level)
    # 避免重复添加 handler(比如 gradio reload / 多次 import)
    if any(getattr(h, "_comfy_console", False) for h in root.handlers):
        return
    handler = logging.StreamHandler(sys.stdout)
    handler._comfy_console = True  # type: ignore[attr-defined]
    handler.setFormatter(logging.Formatter(
        fmt="%(asctime)s | %(levelname)-7s | %(name)s | %(message)s",
        datefmt="%H:%M:%S",
    ))
    root.addHandler(handler)


# ComfyUI 输出行 -> 日志级别的粗略判定。ComfyUI 大部分是 print,没有级别前缀,
# 我们按关键字猜一个级别,主要是为了让 error/warning 在控制台里能被一眼看到。
def _guess_level(line: str) -> int:
    low = line.lower()
    if any(k in low for k in ("traceback", "error", "exception", "failed", "cuda out of memory")):
        return logging.ERROR
    if any(k in low for k in ("warning", "warn", "deprecat")):
        return logging.WARNING
    return logging.INFO


# ============================================================================
# 目录与常量
# ============================================================================
# 先拿到本文件所在目录,后续所有路径都基于它推导,避免写死绝对路径。
BASE_DIR = Path(__file__).resolve().parent

# 本文件旁边就是解压好的 Portable 目录。
COMFY_ROOT = BASE_DIR / "comfy"
PYTHON_EXE = COMFY_ROOT / "python_embeded" / "python.exe"
COMFY_APP = COMFY_ROOT / "ComfyUI"          # main.py 所在目录,默认 input/temp 都在这里
MAIN_PY = COMFY_APP / "main.py"

WORKFLOW_DIR = BASE_DIR / "workflows_api"

# 产物统一输出到「webui.py 所在目录 / outputs」(即 app/outputs,BASE_DIR 就是该目录)。
# 启动 ComfyUI 时用 --output-directory 指过去,所有 SaveImage/SaveVideo 等都会落到这里。
OUTPUT_DIR = BASE_DIR / "outputs"
# ComfyUI 的输入 / 临时目录(未覆盖,保持默认)。output_file_path 用它们把
# collect_output_files 的一项还原成磁盘路径。
INPUT_DIR = COMFY_APP / "input"
TEMP_DIR = COMFY_APP / "temp"

COMFY_HOST = "127.0.0.1"
COMFY_PORT = 8188
BASE = f"http://{COMFY_HOST}:{COMFY_PORT}"

# 每个客户端用唯一 client_id,ComfyUI 用它区分来源。
CLIENT_ID = uuid.uuid4().hex

# ComfyUI 启动参数。
EXTRA_ARGS = [
    "--windows-standalone-build",
    "--enable-manager",
    "--disable-auto-launch",
]

STARTUP_TIMEOUT = 300  # 等待 ComfyUI 就绪的最大秒数
TASK_TIMEOUT = 10800  # 单个任务等待完成的最大秒数


# 需要按“相对本 app 目录”解析成绝对路径的目录类环境变量(模型下载 / 运行缓存)。
# 之所以在 Python 侧统一处理:start.conf 里写的是相对路径(如 HF_HOME=models/huggingface),
# 而本程序(webui.py,cwd=app/)与它拉起的 ComfyUI 子进程(cwd=app/comfy)工作目录不同,
# 若各自按相对路径解析就会指向不同位置。这里在启动早期把它们统一解析为绝对路径并回写
# os.environ,于是无论用启动器还是 start.bat / start.sh 启动,两个进程都指向同一目录。
_DIR_ENV_KEYS = (
    "HF_HOME", "HUGGINGFACE_HUB_CACHE", "HF_HUB_CACHE", "HF_DATASETS_CACHE",
    "TRANSFORMERS_CACHE", "MODELSCOPE_CACHE", "TORCH_HOME",
    "GRADIO_TEMP_DIR", "MPLCONFIGDIR", "PIP_CACHE_DIR", "XDG_CACHE_HOME",
)


def normalize_dir_env(base_dir: "Path | None" = None) -> None:
    """把上述目录类环境变量里的相对路径解析为绝对路径(相对 base_dir,默认本 app 目录),
    并回写到 os.environ。已是绝对路径的保持不变。请在程序入口尽早调用一次
    (在拉起 ComfyUI、以及任何会读取这些变量的库真正下载/缓存之前)。"""
    root = base_dir or BASE_DIR
    for key in _DIR_ENV_KEYS:
        val = os.environ.get(key)
        if val and not os.path.isabs(val):
            resolved = str((root / val).resolve())
            os.environ[key] = resolved
            logger.info("规范化目录环境变量 %s -> %s", key, resolved)


def configure(host: str | None = None, port: int | None = None) -> None:
    """
    在 start_comfyui() 之前调用,用于覆盖 ComfyUI 的监听地址/端口。
    命令行传入的参数最终通过它落到模块级常量上。
    """
    global COMFY_HOST, COMFY_PORT, BASE
    if host:
        COMFY_HOST = host
    if port:
        COMFY_PORT = int(port)
    BASE = f"http://{COMFY_HOST}:{COMFY_PORT}"


# 进程句柄与内部状态。
_process: "subprocess.Popen | None" = None
_log_thread: "threading.Thread | None" = None
_stop_logging = threading.Event()
_job_handle = None  # Windows Job Object 句柄,需保持存活


# ============================================================================
# 存活 / 就绪检测
# ============================================================================
def is_port_open(host: str = COMFY_HOST, port: int = COMFY_PORT, timeout: float = 1.0) -> bool:
    """检测 TCP 端口在指定 host 上是否可连接。"""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.settimeout(timeout)
        return s.connect_ex((host, port)) == 0


def _win_port_listening(port: int) -> bool:
    """Windows:用 netstat 判断是否有任何本机 IP 在 LISTENING 该端口。"""
    try:
        # CREATE_NO_WINDOW 避免弹出黑框;-ano 输出所有连接及 PID。
        out = subprocess.run(
            ["netstat", "-ano", "-p", "tcp"],
            capture_output=True, text=True, timeout=5,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        ).stdout or ""
    except Exception as e:  # netstat 不可用等极端情况:放行(不误拦启动)
        logger.warning("netstat 端口检测失败,跳过占用判断: %s", e)
        return False
    suffix = f":{port}"
    for line in out.splitlines():
        parts = line.split()
        # 形如: TCP  0.0.0.0:8188  0.0.0.0:0  LISTENING  1234
        # 只看本地地址列(parts[1]),覆盖 0.0.0.0 / 127.0.0.1 / 局域网 IP / [::]。
        if len(parts) >= 4 and "LISTENING" in parts and parts[1].endswith(suffix):
            return True
    return False


def is_port_in_use(port: int = COMFY_PORT) -> bool:
    """
    检测本机 port 是否已被占用(覆盖所有网卡 / IP,而不仅仅是 127.0.0.1)。

    Windows 直接查 netstat 的 LISTENING 记录 —— 这是最可靠的方式:
      * 不受代理 / TUN 网卡(如 198.18.x.x 对任意端口 connect 都成功)影响;
      * 也不受 SO_REUSEADDR 影响(http.server / ComfyUI 都设了它,同一用户下
        再 bind 同端口在 Windows 上会“成功”,导致 bind 探测漏判)。
    其它平台退回“绑定测试”:posix 上未设 SO_REUSEADDR 的 bind 对已占用端口会失败。
    """
    if os.name == "nt":
        return _win_port_listening(port)
    # posix 兜底:绑定 0.0.0.0 失败即视为占用。
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("0.0.0.0", port))
        return False
    except OSError as e:
        return e.errno in (errno.EADDRINUSE, errno.EACCES)


def is_comfy_ready(host: str = COMFY_HOST, port: int = COMFY_PORT) -> bool:
    """端口可连且 HTTP API 能正常响应,才算真正就绪。"""
    if not is_port_open(host, port):
        return False
    try:
        r = requests.get(f"http://{host}:{port}/system_stats", timeout=2)
        return r.status_code == 200
    except requests.RequestException:
        return False


def is_alive() -> bool:
    """子进程未退出,且 API 仍可响应。"""
    if _process is not None and _process.poll() is not None:
        return False
    return is_comfy_ready()


# ============================================================================
# 工作流加载(需求 3)
# ============================================================================
def load_workflow(name: str) -> dict:
    """
    读取 workflows_api/ 目录下的「API 格式」工作流 json,返回一个可修改的 dict。

    用法:
        wf = load_workflow("txt2img.json")
        wf["6"]["inputs"]["text"] = "a cute cat"     # 改正向提示词
        wf["3"]["inputs"]["seed"] = 12345            # 改种子
        outputs = run_workflow(wf)

    * name 可带或不带 .json 后缀。
    * 每次调用都返回一份深拷贝,多个任务并发改 key 不会互相污染。
    """
    if not name.endswith(".json"):
        name += ".json"
    path = WORKFLOW_DIR / name
    if not path.exists():
        available = [p.name for p in WORKFLOW_DIR.glob("*.json")] if WORKFLOW_DIR.exists() else []
        raise FileNotFoundError(
            f"找不到工作流: {path}\n可用的工作流: {available or '(workflows_api 目录为空或不存在)'}"
        )
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    # 简单校验:API 格式的工作流应是 {节点id: {"inputs": {...}, "class_type": "..."}}
    if not isinstance(data, dict) or not data:
        raise ValueError(f"{name} 不是有效的工作流(应为非空 dict)。")
    sample = next(iter(data.values()))
    if not (isinstance(sample, dict) and "class_type" in sample):
        raise ValueError(
            f"{name} 看起来不是「API 格式」工作流。\n"
            "请在 ComfyUI 里用「保存(API 格式)/ Save (API Format)」导出,而不是普通保存。"
        )
    return copy.deepcopy(data)


def list_workflows() -> list[str]:
    """列出 workflows_api/ 下所有可用的工作流文件名。"""
    if not WORKFLOW_DIR.exists():
        return []
    return sorted(p.name for p in WORKFLOW_DIR.glob("*.json"))


# ============================================================================
# 通用 ComfyUI API
# ============================================================================
def queue_prompt(workflow: dict) -> dict:
    """提交一个工作流到 ComfyUI,返回 {prompt_id, number, node_errors}。"""
    payload = {"prompt": workflow, "client_id": CLIENT_ID}
    r = requests.post(f"{BASE}/prompt", json=payload, timeout=30)
    if r.status_code != 200:
        raise RuntimeError(f"提交失败({r.status_code}): {r.text[:300]}")
    data = r.json()
    if data.get("node_errors"):
        raise RuntimeError(f"工作流校验报错: {data['node_errors']}")
    return data


def get_history(prompt_id: str) -> dict:
    """查询某次提交的历史记录。"""
    r = requests.get(f"{BASE}/history/{prompt_id}", timeout=10)
    return r.json() if r.status_code == 200 else {}


def get_view_file(filename: str, subfolder: str = "", folder_type: str = "output") -> bytes:
    """从 /view 端点取一个产物文件(图片、音频、视频等)的原始字节。"""
    params = {"filename": filename, "subfolder": subfolder, "type": folder_type}
    url = f"{BASE}/view?" + urllib.parse.urlencode(params)
    return requests.get(url, timeout=60).content


def interrupt() -> str:
    """中断 ComfyUI 当前正在执行的任务。"""
    try:
        requests.post(f"{BASE}/interrupt", timeout=5)
        return "已发送中断信号。"
    except requests.RequestException as e:
        return f"中断失败: {e}"


def wait_for_outputs(prompt_id: str, timeout: int = TASK_TIMEOUT,
                     stop_event: "threading.Event | None" = None) -> dict:
    """轮询历史记录,直到该任务执行完成,返回它的 outputs。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if stop_event is not None and stop_event.is_set():
            raise RuntimeError("已请求停止")
        if _process is not None and _process.poll() is not None:
            raise RuntimeError("ComfyUI 进程已退出")
        entry = get_history(prompt_id).get(prompt_id)
        if entry:
            status_str = entry.get("status", {}).get("status_str")
            if status_str == "error":
                raise RuntimeError("ComfyUI 执行出错,请查看控制台 ComfyUI 日志。")
            if entry.get("outputs"):
                return entry["outputs"]
        time.sleep(1.5)
    raise TimeoutError(f"等待任务完成超时({timeout}s)。")


def upload_image(filepath, subfolder="", overwrite=False) -> str:
    """上传图片到 ComfyUI 的 input 目录,返回它实际保存的文件名。"""
    with open(filepath, "rb") as f:
        files = {"image": (Path(filepath).name, f, "application/octet-stream")}
        data = {"type": "input", "overwrite": str(overwrite).lower()}
        if subfolder:
            data["subfolder"] = subfolder
        r = requests.post(f"{BASE}/upload/image", files=files, data=data, timeout=60)
    r.raise_for_status()
    info = r.json()  # {"name": "...", "subfolder": "", "type": "input"}
    # 放进子目录时,工作流引用要用 "subfolder/name"
    return f"{info['subfolder']}/{info['name']}" if info.get("subfolder") else info["name"]


def upload_video(filepath, subfolder="", overwrite=False) -> str:
    """
    上传视频到 ComfyUI 的 input 目录,返回它实际保存的文件名。

    ComfyUI 的视频加载节点(VHS_LoadVideo 等)与图片共用同一个 /upload/image 端点
    (表单字段名固定为 image),因此这里直接复用 upload_image,仅换个语义清晰的名字
    供「视频放大」业务调用。
    """
    return upload_image(filepath, subfolder=subfolder, overwrite=overwrite)


def audio_duration(filepath) -> float:
    """返回音频时长(秒),失败返回 0.0。优先 mutagen,退回 wave(仅 wav)。"""
    try:
        from mutagen import File as MutagenFile
        info = MutagenFile(filepath)
        if info is not None and info.info is not None:
            return float(info.info.length)
    except Exception:
        pass
    try:
        import wave, contextlib
        with contextlib.closing(wave.open(str(filepath), "rb")) as w:
            return w.getnframes() / float(w.getframerate())
    except Exception:
        return 0.0


def collect_output_files(outputs: dict) -> list[dict]:
    """
    把 run_workflow 返回的原始 outputs 里的所有产物文件抽出来,拍平成一个列表。
    每项形如 {"type": "images"/"gifs"/"audio"/..., "filename","subfolder","folder_type"}。
    上层可据此调用 get_view_file() 下载,不必自己去解析 outputs 的嵌套结构。
    """
    files = []
    for node_output in outputs.values():
        for key, items in node_output.items():
            if not isinstance(items, list):
                continue
            for it in items:
                if isinstance(it, dict) and "filename" in it:
                    files.append({
                        "type": key,
                        "filename": it["filename"],
                        "subfolder": it.get("subfolder", ""),
                        "folder_type": it.get("type", "output"),
                    })
    return files


def output_file_path(item: dict) -> Path:
    """
    把 collect_output_files 里的一项(含 filename/subfolder/folder_type)还原成磁盘上的
    实际路径,供 Gradio 直接展示,免去再走 /view 下载一遍。

    * folder_type=="output" -> OUTPUT_DIR(已重定向到「根目录/outputs」)
    * folder_type=="input"  -> ComfyUI 输入目录
    * folder_type=="temp"   -> ComfyUI 临时目录
    """
    base = {
        "output": OUTPUT_DIR,
        "input": INPUT_DIR,
        "temp": TEMP_DIR,
    }.get(item.get("folder_type", "output"), OUTPUT_DIR)
    subfolder = item.get("subfolder", "")
    return (base / subfolder / item["filename"]) if subfolder else (base / item["filename"])


def run_workflow(workflow: dict, timeout: int = TASK_TIMEOUT,
                 stop_event: "threading.Event | None" = None) -> dict:
    """提交工作流并等待执行完成,返回原始 outputs。上层自行决定如何解析成最终结果。"""
    res = queue_prompt(workflow)
    logger.info("已提交任务 prompt_id=%s", res.get("prompt_id"))
    return wait_for_outputs(res["prompt_id"], timeout=timeout, stop_event=stop_event)


# ============================================================================
# 启动 / 关闭
# ============================================================================
def _stream_output(proc: subprocess.Popen) -> None:
    """把子进程的 stdout 逐行转发到本模块 logger(按内容猜级别),从而统一到控制台。"""
    if proc.stdout is None:
        return
    try:
        logger.info("========== ComfyUI 子进程日志开始转发 ==========")
        for line in iter(proc.stdout.readline, ""):
            if _stop_logging.is_set():
                break
            line = _ANSI_RE.sub("", line.rstrip("\n"))  # 去掉 ANSI 颜色码
            if line:
                logger.log(_guess_level(line), "[ComfyUI] %s", line)
    except Exception as e:
        logger.exception("日志转发线程异常: %s", e)


def _wait_until_ready(timeout: int = STARTUP_TIMEOUT) -> None:
    """轮询直到 ComfyUI 就绪;子进程中途退出或超时则抛异常。"""
    logger.info("正在等待 ComfyUI 就绪(最长 %ss)……", timeout)
    start = time.time()
    while time.time() - start < timeout:
        if _process is not None and _process.poll() is not None:
            raise RuntimeError(
                f"ComfyUI 进程已退出(退出码 {_process.returncode})。请查看上方控制台日志。"
            )
        if is_comfy_ready():
            logger.info("ComfyUI 已就绪!耗时 %.1fs -> %s", time.time() - start, BASE)
            return
        time.sleep(1.5)
    raise TimeoutError(f"等待 ComfyUI 启动超时({timeout}s)。")


def _bind_child_lifecycle_to_parent(proc: subprocess.Popen) -> None:
    """
    把 ComfyUI 的生命周期绑定到当前进程。即便本进程被强制结束(atexit 来不及执行),
    操作系统也会连带结束 ComfyUI,避免它变成孤儿进程继续占用显存和端口。

    Windows 的做法是把子进程放进一个带 KILL_ON_JOB_CLOSE 的 Job Object:只要持有
    job 句柄的本进程消失,系统就会关闭 job,从而结束里面的所有进程。其它平台暂不处理
    (仍有 atexit 兜底),Linux 可改用 PR_SET_PDEATHSIG 达到类似效果。
    """
    global _job_handle
    if os.name != "nt":
        return
    try:
        import ctypes
        from ctypes import wintypes

        k32 = ctypes.windll.kernel32
        k32.CreateJobObjectW.restype = wintypes.HANDLE
        k32.CreateJobObjectW.argtypes = [wintypes.LPVOID, wintypes.LPCWSTR]
        k32.SetInformationJobObject.restype = wintypes.BOOL
        k32.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int,
                                                wintypes.LPVOID, wintypes.DWORD]
        k32.AssignProcessToJobObject.restype = wintypes.BOOL
        k32.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]

        class BASIC(ctypes.Structure):
            _fields_ = [
                ("PerProcessUserTimeLimit", ctypes.c_int64),
                ("PerJobUserTimeLimit", ctypes.c_int64),
                ("LimitFlags", wintypes.DWORD),
                ("MinimumWorkingSetSize", ctypes.c_size_t),
                ("MaximumWorkingSetSize", ctypes.c_size_t),
                ("ActiveProcessLimit", wintypes.DWORD),
                ("Affinity", ctypes.c_size_t),
                ("PriorityClass", wintypes.DWORD),
                ("SchedulingClass", wintypes.DWORD),
            ]

        class IO_COUNTERS(ctypes.Structure):
            _fields_ = [("ReadOperationCount", ctypes.c_uint64),
                        ("WriteOperationCount", ctypes.c_uint64),
                        ("OtherOperationCount", ctypes.c_uint64),
                        ("ReadTransferCount", ctypes.c_uint64),
                        ("WriteTransferCount", ctypes.c_uint64),
                        ("OtherTransferCount", ctypes.c_uint64)]

        class EXTENDED(ctypes.Structure):
            _fields_ = [("BasicLimitInformation", BASIC),
                        ("IoInfo", IO_COUNTERS),
                        ("ProcessMemoryLimit", ctypes.c_size_t),
                        ("JobMemoryLimit", ctypes.c_size_t),
                        ("PeakProcessMemoryUsed", ctypes.c_size_t),
                        ("PeakJobMemoryUsed", ctypes.c_size_t)]

        JobObjectExtendedLimitInformation = 9
        JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000

        job = k32.CreateJobObjectW(None, None)
        if not job:
            logger.warning("创建 Job Object 失败,跳过(仍有 atexit 兜底)。")
            return

        info = EXTENDED()
        info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        k32.SetInformationJobObject(job, JobObjectExtendedLimitInformation,
                                    ctypes.byref(info), ctypes.sizeof(info))

        if not k32.AssignProcessToJobObject(job, int(proc._handle)):
            logger.warning("绑定子进程到 Job 失败,跳过(仍有 atexit 兜底)。")
            return

        _job_handle = job  # 保持句柄存活,本进程一旦消失即触发连带回收
        logger.info("已绑定 ComfyUI 生命周期:本程序退出或被结束时会连带关闭 ComfyUI。")
    except Exception as e:
        logger.warning("绑定生命周期异常(忽略,改用 atexit 兜底): %s", e)


def start_comfyui(wait: bool = True, startup_timeout: int = STARTUP_TIMEOUT,
                  reuse_existing: bool = False):
    """
    启动 ComfyUI,返回子进程对象。

    * reuse_existing=False(默认):启动前若检测到已有 ComfyUI 在运行,直接抛
      ComfyUIAlreadyRunningError,不再启动新实例(由上层决定如何提示用户)。
    * reuse_existing=True:检测到已有实例则复用并返回 None(旧行为,供调试用)。
    """
    global _process, _log_thread

    # 允许复用且本机 API 可响应时,直接复用现有实例(调试用)。
    if reuse_existing and is_comfy_ready():
        logger.info("检测到 ComfyUI 已在 %s 运行,直接复用。", BASE)
        return None

    # 端口占用检测:不只看 127.0.0.1,而是判断 8188 是否被本机任意 IP 占用。
    # 这样另一个绑在局域网 IP 上的 ComfyUI 也能被发现,避免继续启动导致端口冲突。
    if is_port_in_use(COMFY_PORT):
        raise ComfyUIAlreadyRunningError(
            f"ComfyUI 端口 {COMFY_PORT} 已被占用(可能有 ComfyUI 正在运行),请先关闭后再启动。"
        )

    if not PYTHON_EXE.exists():
        raise FileNotFoundError(
            f"找不到内嵌 Python: {PYTHON_EXE}\n请确认 ComfyUI_windows_portable 与本文件在同一目录。"
        )
    if not MAIN_PY.exists():
        raise FileNotFoundError(f"找不到 ComfyUI 主程序: {MAIN_PY}")

    # 把所有产物输出目录固定到「项目根目录 / outputs」。目录不存在时先建好。
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    cmd = [str(PYTHON_EXE), "-s", str(MAIN_PY), "--port", str(COMFY_PORT),
           "--output-directory", str(OUTPUT_DIR), *EXTRA_ARGS]
    logger.info("启动命令: %s", " ".join(cmd))
    logger.info("ComfyUI 产物输出目录: %s", OUTPUT_DIR)

    # 强制 ComfyUI 子进程用 UTF-8 输出,否则它的自定义节点在 Windows 上会按 GBK 打印,
    # 我们再按 utf-8 解就会得到 \ufffd 乱码。设这两个环境变量即可让两端编码一致。
    env = os.environ.copy()
    env["PYTHONUTF8"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"

    creationflags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    _process = subprocess.Popen(
        cmd, cwd=str(COMFY_ROOT), env=env,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", errors="replace", bufsize=1,
        creationflags=creationflags,
    )

    # 绑定生命周期:本程序意外结束时连带关闭 ComfyUI。
    _bind_child_lifecycle_to_parent(_process)

    # 守护线程:把 ComfyUI 日志实时转发到本程序控制台。
    _stop_logging.clear()
    _log_thread = threading.Thread(target=_stream_output, args=(_process,), daemon=True)
    _log_thread.start()

    # 正常退出 / Ctrl+C 时的优雅关闭兜底。
    atexit.register(stop_comfyui)

    if wait:
        _wait_until_ready(startup_timeout)
    return _process


def stop_comfyui() -> None:
    """优雅关闭。Windows 用 taskkill /F /T 结束整棵进程树,其它平台先 terminate 再 kill。"""
    global _process
    _stop_logging.set()
    if _process is None or _process.poll() is not None:
        _process = None
        return
    logger.info("正在关闭 ComfyUI ……")
    try:
        if os.name == "nt":
            subprocess.run(["taskkill", "/F", "/T", "/PID", str(_process.pid)], capture_output=True)
        else:
            _process.terminate()
        _process.wait(timeout=10)
    except Exception:
        try:
            _process.kill()
        except Exception:
            pass
    _process = None


if __name__ == "__main__":
    # 单独运行本文件:只启动 ComfyUI 并在前台持续转发日志。
    setup_logging()
    start_comfyui(wait=True)
    logger.info("ComfyUI 正在前台运行,按 Ctrl+C 退出。")
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        stop_comfyui()
