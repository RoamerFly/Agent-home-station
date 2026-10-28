"""Run the registered Comfy entry using a private Unix socket, with no TCP.

Designed for managed_music_runner's private network namespace. The trusted
music broker can reach this single socket without granting inference access
to localhost management ports, credentials or host networking.
"""
import os
from pathlib import Path
import runpy
import sys
import typing


def schema_alias(annotation):
    """Map typing.List forms to their equivalent PEP 585 spellings."""
    origin, args = typing.get_origin(annotation), typing.get_args(annotation)
    if origin is list:
        return list[schema_alias(args[0])]
    if origin is typing.Union:
        return typing.Union[tuple(schema_alias(arg) for arg in args)]
    return annotation


def register_torch_schema_aliases():
    # Torch 2.6's schema registry recognizes typing.List[int], while the
    # registered Comfy revision also uses list[int]. Keep the same schema and
    # validation; add only equivalent aliases, without changing tensor ops.
    import importlib
    schema = importlib.import_module('torch._library.infer_schema')
    for name in ('SUPPORTED_PARAM_TYPES', 'SUPPORTED_RETURN_TYPES'):
        registry = getattr(schema, name, None)
        if isinstance(registry, dict):
            for annotation, value in list(registry.items()):
                registry.setdefault(schema_alias(annotation), value)


def flash_device_guard(available):
    """CPU-offloaded YuE caches use ordinary attention, not CUDA flash decode.

    Some comfy-kitchen releases query CUDA capability for every device when a
    CUDA extension is installed. Keep their GPU decision unchanged.
    """
    if getattr(available, '_wechat_device_guard', False):
        return available
    def guarded(device=None):
        kind = getattr(device, 'type', None)
        if isinstance(device, str):
            kind = device.partition(':')[0]
        if kind is not None and kind != 'cuda':
            return False
        return available(device)
    guarded._wechat_device_guard = True
    return guarded


def register_flash_device_guard():
    import importlib
    try:
        kitchen = importlib.import_module('comfy_kitchen')
    except ImportError:
        return  # Older registered Comfy environments do not use this helper.
    for module, name in ((kitchen, 'flash_attention_decode_is_available'),
                         (sys.modules.get('comfy_kitchen.flash_attention'), 'is_available')):
        current = getattr(module, name, None)
        if callable(current):
            setattr(module, name, flash_device_guard(current))


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
    # Preserve Comfy's allocator/device setup before importing Torch. Loading
    # Torch first and changing PYTORCH_CUDA_ALLOC_CONF afterwards breaks CUDA.
    import comfy.options
    comfy.options.enable_args_parsing()
    import cuda_malloc  # noqa: F401 - applies the registered project's settings
    register_torch_schema_aliases()
    register_flash_device_guard()
    runpy.run_path(str(entry), run_name="__main__")


if __name__ == "__main__":
    main()
