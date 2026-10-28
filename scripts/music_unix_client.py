"""HTTP client for only the configured isolated Comfy Unix socket."""
import http.client
import socket
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
