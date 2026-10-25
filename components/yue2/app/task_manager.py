# -*- coding: utf-8 -*-
"""
task_manager.py
===============
任务队列层:把「提交一个 ComfyUI 工作流」封装成任务,放进一个**严格串行**的队列,
由一个后台工作线程一次一个地执行。Gradio 页面通过本模块把任务加入队列、查询状态。

并发模型
--------
* 严格串行:队列里同一时刻只有一个任务在跑。工作线程取出一个 -> 执行完 -> 再取下一个。
* 「立即生成」不进队列,但必须先判断队列是否空闲(is_busy()),忙则拒绝。

对外主要接口
------------
  start()                        启动后台工作线程(入口调用一次)
  submit(name, patch, ...)       把任务加入队列,返回 task_id
  run_now(name, patch, ...)      不进队列直接前台执行(调用方需先确认 not is_busy())
  is_busy()                      队列里有排队任务,或当前有任务正在执行
  queue_size()                   仅排队中的任务数(不含正在执行的)
  snapshot()                     返回所有任务的状态快照,供页面展示
  get_task(task_id)              取单个任务
  cancel(task_id)                取消一个「排队中」的任务
  interrupt_current()            中断当前正在执行的任务
"""

import time
import uuid
import queue
import logging
import threading
from enum import Enum
from dataclasses import dataclass, field, asdict
from typing import Callable, Optional

import gradio as gr

import comfyui_service as cs

logger = logging.getLogger("task_manager")


# ---------------------------------------------------------------------------
# 通用格式化(供「队列详情」页展示)
# ---------------------------------------------------------------------------
def _fmt_hms(ts) -> str:
    """时间戳 -> 'HH:MM:SS'。"""
    return time.strftime("%H:%M:%S", time.localtime(ts)) if ts else "-"


def _fmt_dur(sec) -> str:
    """秒数 -> '1时02分03秒' 形式的耗时文本。"""
    if sec is None:
        return "-"
    sec = int(sec)
    h, m, s = sec // 3600, (sec % 3600) // 60, sec % 60
    if h:
        return f"{h}时{m:02d}分{s:02d}秒"
    if m:
        return f"{m}分{s:02d}秒"
    return f"{s}秒"


class TaskStatus(str, Enum):
    QUEUED    = "排队中"
    RUNNING   = "执行中"
    DONE      = "已完成"
    FAILED    = "失败"
    CANCELLED = "已取消"


@dataclass
class Task:
    id: str
    workflow_name: str                       # 工作流文件名,如 "txt2img.json"
    patch: dict                              # {"节点id.inputs.key": value} 形式的改动
    label: str = ""                          # 给人看的备注(比如提示词摘要)
    status: TaskStatus = TaskStatus.QUEUED
    created_at: float = field(default_factory=time.time)
    started_at: Optional[float] = None
    finished_at: Optional[float] = None
    outputs: Optional[dict] = None           # ComfyUI 原始 outputs
    files: list = field(default_factory=list)  # collect_output_files 拍平后的产物列表
    error: Optional[str] = None

    def to_row(self) -> list:
        """转成 Gradio Dataframe 的一行。"""
        def fmt(ts):
            return time.strftime("%H:%M:%S", time.localtime(ts)) if ts else "-"
        return [self.id[:8], self.workflow_name, self.label,
                self.status.value, fmt(self.created_at),
                fmt(self.started_at), fmt(self.finished_at),
                self.error or ""]


def apply_patch(workflow: dict, patch: dict) -> dict:
    """
    把 patch 应用到工作流 dict 上。patch 的 key 支持两种写法:
      "6.inputs.text"      -> workflow["6"]["inputs"]["text"] = value  (点号路径)
      ("6","inputs","text")-> 同上(元组路径)
    找不到路径会抛 KeyError,方便尽早发现工作流改错了。
    """
    for path, value in patch.items():
        keys = path.split(".") if isinstance(path, str) else list(path)
        node = workflow
        for k in keys[:-1]:
            node = node[k]
        node[keys[-1]] = value
    return workflow


class TaskManager:
    def __init__(self):
        self._queue: "queue.Queue[Task]" = queue.Queue()
        self._tasks: dict[str, Task] = {}          # id -> Task,全量记录(含历史)
        self._order: list[str] = []                # 任务加入顺序,用于展示
        self._current: Optional[Task] = None       # 正在执行的任务
        self._lock = threading.Lock()
        self._worker: Optional[threading.Thread] = None
        self._running = False
        # 「立即生成」也要占用同一个执行权,用一把执行锁保证全局一次只跑一个任务。
        self._exec_lock = threading.Lock()
        # 任务状态变化时的回调,供页面刷新(可选)。
        self.on_change: Optional[Callable[[], None]] = None

    # ---- 生命周期 ---------------------------------------------------------
    def start(self) -> None:
        """启动后台工作线程(幂等)。"""
        if self._running:
            return
        self._running = True
        self._worker = threading.Thread(target=self._loop, daemon=True, name="task-worker")
        self._worker.start()
        logger.info("任务队列工作线程已启动。")

    def _notify(self) -> None:
        if self.on_change:
            try:
                self.on_change()
            except Exception:
                logger.exception("on_change 回调异常")

    # ---- 状态查询 ---------------------------------------------------------
    def is_busy(self) -> bool:
        """队列里有排队任务,或当前正有任务在执行。'立即生成' 靠它判断能否执行。"""
        with self._lock:
            return self._current is not None or not self._queue.empty()

    def queue_size(self) -> int:
        """仅排队中的任务数(不含正在执行的)。"""
        return self._queue.qsize()

    def get_task(self, task_id: str) -> Optional[Task]:
        with self._lock:
            return self._tasks.get(task_id)

    def snapshot(self) -> list[Task]:
        """按加入顺序返回所有任务(含历史),供页面展示。"""
        with self._lock:
            return [self._tasks[i] for i in self._order]

    def queue_detail(self) -> dict:
        """
        「队列详情」页所需的数据(纯数据,与 UI 无关,方便复用):
          running —— 当前正在执行的任务 {workflow, label, elapsed(秒)},空闲则为 None
          stats   —— 统计 {total, queued, running, done, failed, cancelled}
          rows    —— 任务明细表(新任务在前),含实时「耗时」列
        """
        now = time.time()
        with self._lock:
            cur = self._current
            tasks = [self._tasks[i] for i in self._order]

        running = None
        if cur is not None and cur.status == TaskStatus.RUNNING:
            running = {
                "workflow": cur.workflow_name,
                "label": cur.label,
                "elapsed": (now - cur.started_at) if cur.started_at else 0.0,
            }

        stats = {
            "total": len(tasks),
            "queued": sum(1 for t in tasks if t.status == TaskStatus.QUEUED),
            "running": 1 if running else 0,
            "done": sum(1 for t in tasks if t.status == TaskStatus.DONE),
            "failed": sum(1 for t in tasks if t.status == TaskStatus.FAILED),
            "cancelled": sum(1 for t in tasks if t.status == TaskStatus.CANCELLED),
        }

        rows = []
        for t in reversed(tasks):  # 新任务在前
            if t.status == TaskStatus.RUNNING:
                dur = _fmt_dur((now - t.started_at) if t.started_at else 0)
            elif t.status in (TaskStatus.DONE, TaskStatus.FAILED):
                dur = _fmt_dur(
                    (t.finished_at - t.started_at)
                    if (t.finished_at and t.started_at) else None
                )
            elif t.status == TaskStatus.QUEUED:
                dur = "已等待 " + _fmt_dur(now - t.created_at)
            else:
                dur = "-"
            rows.append([
                t.status.value,
                t.workflow_name,
                t.label or "-",
                dur,
                _fmt_hms(t.created_at),
                _fmt_hms(t.started_at),
                _fmt_hms(t.finished_at),
                t.error or "",
            ])
        return {"running": running, "stats": stats, "rows": rows}

    # ---- 提交 -------------------------------------------------------------
    def submit(self, workflow_name: str, patch: dict, label: str = "") -> str:
        """把任务加入队列,立刻返回 task_id;真正执行由后台线程串行处理。"""
        task = Task(id=uuid.uuid4().hex, workflow_name=workflow_name,
                    patch=patch or {}, label=label)
        with self._lock:
            self._tasks[task.id] = task
            self._order.append(task.id)
        self._queue.put(task)
        logger.info("任务入队 %s (%s) label=%s", task.id[:8], workflow_name, label)
        self._notify()
        return task.id

    def run_now(self, workflow_name: str, patch: dict, label: str = "",
                stop_event: Optional[threading.Event] = None) -> Task:
        """
        不进队列,直接在调用线程里前台执行一个任务并返回结果。
        注意:调用前应先确认 not is_busy();这里再用 exec_lock 兜底,
        若拿不到锁说明有任务正在跑,直接拒绝,避免破坏串行保证。
        """
        if not self._exec_lock.acquire(blocking=False):
            raise RuntimeError("当前有任务正在执行,无法立即生成。")
        task = Task(id=uuid.uuid4().hex, workflow_name=workflow_name,
                    patch=patch or {}, label=label or "立即生成")
        with self._lock:
            self._tasks[task.id] = task
            self._order.append(task.id)
            self._current = task
        try:
            self._notify()
            self._execute(task, stop_event=stop_event)
            return task
        finally:
            with self._lock:
                self._current = None
            self._exec_lock.release()
            self._notify()

    # ---- 取消 / 中断 ------------------------------------------------------
    def cancel(self, task_id: str) -> bool:
        """取消一个仍在排队的任务。正在执行的用 interrupt_current()。"""
        with self._lock:
            task = self._tasks.get(task_id)
            if task and task.status == TaskStatus.QUEUED:
                task.status = TaskStatus.CANCELLED
                task.finished_at = time.time()
                logger.info("任务已取消 %s", task_id[:8])
                self._notify()
                return True
        return False

    def interrupt_current(self) -> str:
        """中断 ComfyUI 当前正在执行的任务。"""
        return cs.interrupt()

    # ---- 后台执行 ---------------------------------------------------------
    def _loop(self) -> None:
        while self._running:
            try:
                task = self._queue.get(timeout=0.5)
            except queue.Empty:
                continue
            # 排队期间可能已被取消
            if task.status == TaskStatus.CANCELLED:
                self._queue.task_done()
                continue
            with self._exec_lock:                # 与 run_now 互斥,保证全局串行
                with self._lock:
                    self._current = task
                self._notify()
                try:
                    self._execute(task)
                finally:
                    with self._lock:
                        self._current = None
                    self._notify()
            self._queue.task_done()

    def _execute(self, task: Task, stop_event: Optional[threading.Event] = None) -> None:
        """真正跑一个任务:加载工作流 -> 打补丁 -> 提交 -> 等结果。异常写进 task.error。"""
        task.status = TaskStatus.RUNNING
        task.started_at = time.time()
        logger.info("开始执行任务 %s (%s)", task.id[:8], task.workflow_name)
        try:
            wf = cs.load_workflow(task.workflow_name)
            apply_patch(wf, task.patch)
            outputs = cs.run_workflow(wf, stop_event=stop_event)
            task.outputs = outputs
            task.files = cs.collect_output_files(outputs)
            task.status = TaskStatus.DONE
            logger.info("任务完成 %s,产物 %d 个", task.id[:8], len(task.files))
        except Exception as e:
            task.status = TaskStatus.FAILED
            task.error = str(e)
            logger.exception("任务失败 %s: %s", task.id[:8], e)
        finally:
            task.finished_at = time.time()


# 全局单例,供各页面直接 import 使用。
manager = TaskManager()


# ---------------------------------------------------------------------------
# 「队列详情」Tab
# ---------------------------------------------------------------------------
# 明细表列。datatype 全为 str,值由 queue_detail() 直接给出。
_QUEUE_HEADERS = ["状态", "工作流", "备注", "耗时", "创建", "开始", "结束", "错误"]


def _queue_summary_md(d: dict) -> str:
    """把 queue_detail() 的结果渲染成顶部一句话状态 + 一行统计。"""
    r = d["running"]
    if r:
        title = r["label"] or r["workflow"]
        cur = f"🟢 正在执行：**{title}** · 已运行 **{_fmt_dur(r['elapsed'])}**"
    else:
        cur = "⚪ 当前空闲，暂无任务在执行"
    s = d["stats"]
    stat = (
        f"排队中 **{s['queued']}** · 已完成 {s['done']} · "
        f"失败 {s['failed']} · 已取消 {s['cancelled']} · 累计 {s['total']}"
    )
    return f"{cur} . {stat}"


def _queue_snapshot():
    """采集一次数据,返回(顶部状态 markdown, 明细表 rows)。"""
    d = manager.queue_detail()
    return _queue_summary_md(d), d["rows"]


def build_queue_tab() -> None:
    """
    构建「队列详情」页:顶部一句话状态 + 明细表,每 2 秒自动刷新。

    用法(在 webui.py 中):
        from task_manager import build_queue_tab
        with gr.Tab("队列详情"):
            build_queue_tab()
    """
    d0 = manager.queue_detail()

    with gr.Row(equal_height=True):
        refresh_btn = gr.Button("🔄 刷新", variant="secondary", size="sm", scale=0, min_width=160)
        status_md = gr.Markdown(_queue_summary_md(d0), elem_classes="height-center")
    table = gr.Dataframe(
        value=d0["rows"],
        headers=_QUEUE_HEADERS,
        datatype=["str"] * len(_QUEUE_HEADERS),
        interactive=False,
        wrap=True,
        column_widths=["72px", None, None, "120px", "88px", "88px", "88px", None],
        label="任务明细",
        show_label=True,
    )

    outputs = [status_md, table]
    refresh_btn.click(_queue_snapshot, outputs=outputs)
    # 每 2 秒自动刷新一次(耗时列会随之跳动)。
    gr.Timer(2.0).tick(_queue_snapshot, outputs=outputs)
