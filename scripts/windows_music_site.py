"""Small CLI compatibility setup for the explicitly authorized music runtime."""
import os
from pathlib import Path


def initialize():
    if os.name != "nt" or not (os.environ.get("WECHAT_WINDOWS_MUSIC_POLICY") == "registry-read-private-network-v1"
                               or os.environ.get("WECHAT_WINDOWS_MUSIC_RUNTIME") == "1"):
        return
    # LPAC cannot reopen the global NUL device. Libraries use os.devnull for
    # disposable diagnostics; place that sink in the account's own scratch.
    if not os.environ.get("TMP"):
        return  # a trusted control process may use the real NUL device
    scratch = Path(os.environ["TMP"])
    if not scratch.is_absolute():
        raise ValueError("music_scratch_requires_absolute_path")
    scratch.mkdir(parents=True, exist_ok=True)
    sink = scratch / ("null-" + str(os.getpid()) + ".tmp")
    sink.write_bytes(b"")
    os.devnull = str(sink)
    if os.environ.get("WECHAT_WINDOWS_MUSIC_RUNTIME") == "1":
        # Python's default multiprocessing pipes use the global pipe namespace,
        # which AppContainer rejects. Parent and workers share this account's
        # AppContainer namespace; handles and ACL boundaries remain unchanged.
        import multiprocessing.connection as connection
        original = connection.arbitrary_address
        if not getattr(original, "_wechat_local_pipe", False):
            def address(family):
                value = original(family)
                prefix = "\\\\.\\pipe\\"
                return prefix + "LOCAL\\" + value[len(prefix):] if family == "AF_PIPE" else value
            address._wechat_local_pipe = True
            connection.arbitrary_address = address
