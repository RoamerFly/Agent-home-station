import argparse
import gc
import signal
import sys
import time
import torch

"""
仅仅用作测试使用，用于控制显存占用。python 解释器需要有 torch
示例： python --occupy 8
"""

GiB = 1024 ** 3


def format_size(n):
    n = float(n)
    for unit in ("B", "KiB", "MiB", "GiB"):
        if n < 1024:
            return f"{n:.2f} {unit}"
        n /= 1024
    return f"{n:.2f} TiB"


def get_memory(device):
    free, total = torch.cuda.mem_get_info(device)
    return total, total - free, free


def allocate_memory(device, target_bytes, chunk_bytes):
    """分块申请并写入显存，返回 tensor 列表（持有引用即占用显存）。"""
    chunks = []
    allocated = 0
    print(f"目标占用: {format_size(target_bytes)}，块大小: {format_size(chunk_bytes)}\n")
    try:
        while allocated < target_bytes:
            size = min(chunk_bytes, target_bytes - allocated)
            t = torch.empty(size, dtype=torch.uint8, device=device)
            t.fill_(0)  # 确保真正提交物理显存
            chunks.append(t)
            allocated += size
            print(f"\r正在分配: {format_size(allocated)} / {format_size(target_bytes)}",
                  end="", flush=True)
        torch.cuda.synchronize(device)
        print()
        return chunks
    except BaseException:
        # 分配中途失败/被中断：立即释放已申请的部分
        chunks.clear()
        gc.collect()
        torch.cuda.empty_cache()
        print()
        raise


def touch_memory(chunks, device):
    """轻量访问每个块的首字节（几乎不占算力）。出错返回 False。"""
    try:
        for t in chunks:
            t[0].add_(1)  # uint8 溢出回绕，无影响
        torch.cuda.synchronize(device)
        return True
    except Exception as e:
        print(f"\nWARNING: CUDA 显存访问异常: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(description="NVIDIA GPU 固定显存占用工具")
    g = parser.add_mutually_exclusive_group(required=True)
    g.add_argument("--occupy", type=float, help="占用多少 GiB 显存，例如 --occupy 8")
    g.add_argument("--leave-free", type=float,
                   help="占用到只剩多少 GiB 可用，例如 --leave-free 2")
    parser.add_argument("--gpu", type=int, default=0, help="GPU 编号，默认 0")
    parser.add_argument("--chunk", type=float, default=0.25, help="每块大小(GiB)，默认 0.25")
    parser.add_argument("--interval", type=float, default=1.0, help="保活间隔(秒)，默认 1")
    args = parser.parse_args()

    if not torch.cuda.is_available():
        print("错误：没有检测到 CUDA GPU")
        return 1
    if not 0 <= args.gpu < torch.cuda.device_count():
        print(f"错误：GPU {args.gpu} 不存在，当前共 {torch.cuda.device_count()} 张")
        return 1
    if args.chunk <= 0 or args.interval <= 0:
        print("错误：--chunk 和 --interval 必须大于 0")
        return 1

    device = torch.device(f"cuda:{args.gpu}")
    torch.cuda.set_device(device)

    total, used, free = get_memory(device)
    print(f"GPU {args.gpu}: {torch.cuda.get_device_name(args.gpu)}")
    print(f"启动前  总: {format_size(total)} | 已用: {format_size(used)} | 可用: {format_size(free)}")

    if args.occupy is not None:
        if args.occupy <= 0:
            print("错误：--occupy 必须大于 0")
            return 1
        target_bytes = int(args.occupy * GiB)
    else:
        if args.leave_free < 0:
            print("错误：--leave-free 不能为负数")
            return 1
        target_bytes = free - int(args.leave_free * GiB)
        if target_bytes <= 0:
            print("当前可用显存已不超过目标剩余量，无需占用。")
            return 1

    if target_bytes > free:
        print(f"显存不足：需要 {format_size(target_bytes)}，可用 {format_size(free)}")
        return 1

    # 让 SIGTERM（kill / 任务管理器结束）也能走正常释放流程
    def _on_term(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, _on_term)

    chunks = None
    exit_code = 0
    try:
        chunks = allocate_memory(device, target_bytes, int(args.chunk * GiB))

        total, used, free = get_memory(device)
        print("\n显存占用成功")
        print(f"本程序占用: {format_size(target_bytes)} | "
              f"GPU 已用: {format_size(used)} | GPU 可用: {format_size(free)}")
        print("按 Ctrl+C 释放显存并退出。\n")

        last_report = time.monotonic()
        while True:
            if not touch_memory(chunks, device):
                print("显存保活失败（可能是驱动重置或 GPU 出错），程序退出。")
                exit_code = 2
                break

            now = time.monotonic()
            if now - last_report >= 5:
                try:
                    _, used, free = get_memory(device)
                    print(f"[VRAM] 本程序: {format_size(target_bytes)} | "
                          f"GPU 已用: {format_size(used)} | GPU 可用: {format_size(free)}")
                except Exception as e:
                    print(f"[VRAM] 查询失败: {e}")
                last_report = now

            time.sleep(args.interval)

    except KeyboardInterrupt:
        print("\n正在退出...")
    except RuntimeError as e:
        print(f"\n显存分配失败: {e}")
        exit_code = 1
    finally:
        print("正在释放显存...")
        try:
            if chunks is not None:
                chunks.clear()
            chunks = None
            gc.collect()
            torch.cuda.empty_cache()
            torch.cuda.synchronize(device)
        except Exception:
            pass
        print("显存已释放。")

    return exit_code


if __name__ == "__main__":
    sys.exit(main())