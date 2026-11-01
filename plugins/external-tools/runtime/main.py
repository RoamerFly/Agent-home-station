"""External-tool declaration projection.  This process never opens a socket.

The administrator-owned adapter supplies a reviewed declaration catalogue at
initialization.  HTTP credentials, endpoints and submission/recovery state stay
in the host: an HTTP tool only emits a ``wechat/http_route`` intent by its fixed
route ID.  MCP entries are parsed for a later host adapter but deliberately are
not exposed until that adapter binds their connection/session semantics.
"""
import json
import re
import sys

FRAME_LIMIT = 8 * 1024 * 1024
NAME = re.compile(r"^[a-z][a-z0-9_]{0,63}$")


def strict_object(value, code):
    if not isinstance(value, dict):
        raise ValueError(code)
    return value


def parse_declarations(raw):
    """Return the bounded, credential-free administrator catalogue.

    This validates only the projection contract.  The Go host must separately
    bind every HTTP route to its immutable package declaration and configuration
    revision before it may make a request.
    """
    doc = strict_object(raw, "external_catalog_invalid")
    if doc.get("version") != 1 or set(doc) - {"version", "tools"}:
        raise ValueError("external_catalog_invalid")
    tools = doc.get("tools")
    if not isinstance(tools, list) or len(tools) > 50:
        raise ValueError("external_catalog_invalid")
    seen, http, mcp = set(), [], []
    for item in tools:
        item = strict_object(item, "external_declaration_invalid")
        allowed = {"id", "kind", "description", "input_schema", "route", "max_bytes", "timeout_seconds", "mcp_server"}
        if set(item) - allowed:
            raise ValueError("external_declaration_invalid")
        identifier, kind = item.get("id"), item.get("kind")
        description, schema = item.get("description"), item.get("input_schema")
        if not isinstance(identifier, str) or not NAME.fullmatch(identifier) or identifier in seen or not isinstance(description, str) or not description or len(description) > 2048 or not isinstance(schema, dict):
            raise ValueError("external_declaration_invalid")
        encoded = json.dumps(schema, separators=(",", ":"), ensure_ascii=False)
        if len(encoded.encode("utf8")) > 64 * 1024 or schema.get("type") != "object":
            raise ValueError("external_declaration_invalid")
        seen.add(identifier)
        if kind == "http":
            route = item.get("route")
            maximum, timeout = item.get("max_bytes", 1024 * 1024), item.get("timeout_seconds", 20)
            if not isinstance(route, str) or not NAME.fullmatch(route) or not isinstance(maximum, int) or maximum < 1 or maximum > 4 * 1024 * 1024 or not isinstance(timeout, int) or timeout < 1 or timeout > 900 or "mcp_server" in item:
                raise ValueError("external_declaration_invalid")
            http.append({"id": identifier, "description": description, "input_schema": schema, "route": route, "max_bytes": maximum, "timeout_seconds": timeout})
        elif kind == "mcp":
            # Parse and retain the declaration, but do not advertise an
            # invocation until the host supplies a scoped MCP proxy.
            server = item.get("mcp_server")
            if not isinstance(server, str) or not NAME.fullmatch(server) or "route" in item:
                raise ValueError("external_declaration_invalid")
            mcp.append({"id": identifier, "mcp_server": server})
        else:
            raise ValueError("external_declaration_invalid")
    return {"http": http, "mcp": mcp}


def execute(catalogue, tool, arguments, host):
    if not isinstance(arguments, dict):
        raise ValueError("external_arguments_invalid")
    for declaration in catalogue["http"]:
        if tool == "external_http_" + declaration["id"]:
            response = host({"route": declaration["route"], "body": arguments, "max_bytes": declaration["max_bytes"], "timeout_seconds": declaration["timeout_seconds"]})
            response = strict_object(response, "external_host_response_invalid")
            # Host response is already verified/sanitized; retain no endpoint,
            # header, secret, account or transport object in the plugin result.
            if set(response) - {"result", "error"}:
                raise ValueError("external_host_response_invalid")
            if response.get("error"):
                raise ValueError("external_host_request_failed")
            return response.get("result", {})
    raise ValueError("external_tool_unavailable")


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0
        self.catalogue = {"http": [], "mcp": []}

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if not line.endswith("\n") or len(line.encode("utf8")) > FRAME_LIMIT:
            raise ValueError("frame_limit")
        value = json.loads(line)
        return strict_object(value, "invalid_protocol")

    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(raw.encode("utf8")) + 1 > FRAME_LIMIT:
            raise ValueError("frame_limit")
        self.outgoing.write(raw + "\n")
        self.outgoing.flush()

    def host(self, parameters):
        self.sequence += 1
        identity = "external-http-" + str(self.sequence)
        self.write({"jsonrpc": "2.0", "id": identity, "method": "wechat/http_route", "params": parameters})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != identity or "error" in reply or not isinstance(reply.get("result"), dict):
            raise ValueError("external_host_request_failed")
        return reply["result"]

    def tools(self):
        return [{"name": "external_http_" + item["id"], "description": item["description"], "inputSchema": item["input_schema"]} for item in self.catalogue["http"]]

    def serve(self):
        initialized = False
        while True:
            try:
                request = self.read()
            except EOFError:
                return
            except (ValueError, json.JSONDecodeError):
                continue
            if "id" not in request:
                continue
            try:
                if request.get("jsonrpc") != "2.0":
                    raise ValueError("invalid_protocol")
                method = request.get("method")
                if method == "initialize":
                    metadata = strict_object(request.get("params", {}).get("_meta", {}), "external_catalog_missing")
                    host_metadata = strict_object(metadata.get("wechat/host_metadata", {}), "external_catalog_missing")
                    self.catalogue = parse_declarations(host_metadata.get("external_tools"))
                    initialized = True
                    result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "external-tools", "version": "0.2.0"}}
                elif initialized and method == "ping":
                    result = {}
                elif initialized and method == "tools/list":
                    result = {"tools": self.tools()}
                elif initialized and method == "tools/call":
                    params = strict_object(request.get("params"), "external_arguments_invalid")
                    context = strict_object(params.get("_meta", {}).get("wechat/context"), "host_context_required")
                    if not all(isinstance(context.get(key), str) and context[key] for key in ("account", "thread", "requester", "purpose", "request_key")):
                        raise ValueError("host_context_required")
                    result = execute(self.catalogue, params.get("name"), params.get("arguments"), self.host)
                else:
                    raise ValueError("method_unavailable")
            except (ValueError, TypeError, KeyError) as error:
                code = str(error)
                result = {"ok": False, "error": code if re.fullmatch(r"[a-z_]{1,96}", code) else "external_request_invalid"}
            self.write({"jsonrpc": "2.0", "id": request["id"], "result": result})


if __name__ == "__main__":
    sys.stdin.reconfigure(encoding="utf8")
    sys.stdout.reconfigure(encoding="utf8")
    Wire(sys.stdin, sys.stdout).serve()
