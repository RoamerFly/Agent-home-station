"""Run the registered Comfy entry using a private Unix socket, with no TCP.

Designed for managed_music_runner's private network namespace. The trusted
music broker can reach this single socket without granting inference access
to localhost management ports, credentials or host networking.
"""
import os
from pathlib import Path
import runpy
import sys


def main():
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", required=True)
    parser.add_argument("--entry", required=True)
    args, rest = parser.parse_known_args()
    sock = Path(args.socket)
    if not sock.is_absolute() or sock.exists():
        raise ValueError("comfy_socket_requires_new_absolute_path")
    from aiohttp import web
    class PrivateSite(web.UnixSite):
        def __init__(self, runner, host=None, port=None, **options):
            super().__init__(runner, str(sock), **options)
    web.TCPSite = PrivateSite
    entry = Path(args.entry).resolve(strict=True)
    os.chdir(entry.parent)
    sys.path.insert(0, str(entry.parent))
    sys.argv = [str(entry), *rest]
    runpy.run_path(str(entry), run_name="__main__")


if __name__ == "__main__":
    main()
