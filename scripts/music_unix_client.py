"""HTTP client for only the configured isolated Comfy Unix socket."""
import http.client
import socket
import sys
import io
import time
import re
import urllib.error
from urllib.parse import urlsplit


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        try:
            self.sock.connect(self.path)
        except BaseException:
            self.sock.close()
            raise


class PipeStream(io.RawIOBase):
    """Bounded overlapped byte-stream I/O; HTTP stays inside one account pipe."""
    def __init__(self, path, timeout):
        import _winapi
        self.timeout = timeout
        deadline = time.monotonic() + timeout
        while True:
            try:
                self.handle = _winapi.CreateFile(path, 0xC0000000, 0, 0, 3, 0x40000000, 0)
                break
            except OSError as error:
                if error.winerror not in (2, 231) or time.monotonic() >= deadline:
                    raise
                time.sleep(.02)

    def readable(self):
        return True

    def writable(self):
        return True

    def transfer(self, payload, read):
        import _winapi
        operation, error = (_winapi.ReadFile(self.handle, payload, True) if read else
                            _winapi.WriteFile(self.handle, payload, True))
        if error == 997 and _winapi.WaitForSingleObject(operation.event, round(self.timeout * 1000)) != 0:
            operation.cancel()
            operation.GetOverlappedResult(True)
            raise TimeoutError("music_named_pipe_timeout")
        count, error = operation.GetOverlappedResult(True)
        if error in (109, 232):
            return b"" if read else 0
        if error:
            raise OSError(error, "music_named_pipe_io_failed")
        return operation.getbuffer()[:count] if read else count

    def readinto(self, buffer):
        data = self.transfer(len(buffer), True)
        buffer[:len(data)] = data
        return len(data)

    def write(self, data):
        return self.transfer(bytes(data), False)

    def close(self):
        if not self.closed and getattr(self, "handle", None) is not None:
            import _winapi
            _winapi.CloseHandle(self.handle)
            self.handle = None
        super().close()


class PipeSocket:
    def __init__(self, path, timeout):
        self.stream = PipeStream(path, timeout)

    def sendall(self, data):
        while data:
            size = self.stream.write(data)
            if not size:
                raise ConnectionError("music_named_pipe_closed")
            data = data[size:]

    def makefile(self, *unused, **options):
        return io.BufferedReader(self.stream)

    def close(self):
        self.stream.close()


class PipeConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.path = path

    def connect(self):
        self.sock = PipeSocket(self.path, self.timeout)


class Reply:
    def __init__(self, response, connection):
        self.response, self.connection = response, connection
        self.headers, self.status = response.headers, response.status

    def read(self, size=None):
        return self.response.read(size)

    def close(self):
        self.response.close()
        self.connection.close()

    def __enter__(self):
        return self

    def __exit__(self, *unused):
        self.close()


def open_request(path, request, timeout=20):
    url = request.full_url if hasattr(request, "full_url") else request
    parsed = urlsplit(url)
    if parsed.scheme != "http" or parsed.netloc != "127.0.0.1:8188":
        raise ValueError("comfy_unix_unexpected_destination")
    body = getattr(request, "data", None)
    headers = dict(request.header_items()) if hasattr(request, "header_items") else {}
    if sys.platform == "win32":
        if not re.fullmatch(r"\\\\\.\\pipe\\Sessions\\[0-9]+\\AppContainerNamedObjects\\S-1-15-2(?:-[0-9]+)+\\WechatAgentMusic-[a-f0-9-]+", str(path)):
            raise ValueError("invalid_music_named_pipe")
        connection = PipeConnection(str(path), timeout)
    else:
        connection = UnixConnection(path, timeout)
    try:
        connection.request("POST" if body is not None else "GET", parsed.path + ("?" + parsed.query if parsed.query else ""), body, headers)
        response = connection.getresponse()
        if response.status >= 400:
            # Preserve the real HTTP failure without exposing broker credentials.
            raise urllib.error.HTTPError(url, response.status, response.reason, response.headers, None)
        return Reply(response, connection)
    except BaseException:
        connection.close()
        raise
