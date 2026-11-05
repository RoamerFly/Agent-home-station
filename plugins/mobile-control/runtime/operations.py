"""Pure input grammar for the packaged native operation state machine.

The host independently binds all physical I/O to the original request.
"""
import re


SHA256 = re.compile(r"[0-9a-f]{64}\Z")
IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
OBSERVATION = re.compile(r"obs-[a-f0-9]{32}\Z")
ARTIFACT = re.compile(r"clip-[a-f0-9]{32}\Z")
OPERATIONS = {"device.list", "device.observe", "device.act", "device.record", "device.read_artifact"}
SOURCE_SCHEMA = {"type":"string", "pattern":"^[0-9a-f]{64}$"}


def _object(properties, required):
    return {"type":"object", "properties":properties, "required":required, "additionalProperties":False}


def tool_schema(operation):
    common = {"device_id":{"type":"string"}, "observation_id":{"type":"string"}, "request_key":{"type":"string"}, "action":{"type":"string"}}
    if operation == "device.list":
        request = _object({}, [])
    elif operation == "device.observe":
        request = _object({"device_id":{"type":"string"}}, ["device_id"])
    elif operation == "device.act":
        request = _object({**common, "x":{"type":"integer","minimum":0}, "y":{"type":"integer","minimum":0}, "x2":{"type":"integer","minimum":0}, "y2":{"type":"integer","minimum":0}, "duration_ms":{"type":"integer","minimum":100,"maximum":1500}, "package":{"type":"string"}, "url":{"type":"string","maxLength":8192}, "text":{"type":"string","maxLength":200}}, ["device_id", "observation_id", "request_key", "action"])
    elif operation == "device.record":
        request = _object({**common, "seconds":{"type":"integer","minimum":2,"maximum":12}}, ["device_id", "observation_id", "request_key", "action", "seconds"])
    elif operation == "device.read_artifact":
        request = _object({"device_id":{"type":"string"}, "artifact_id":{"type":"string"}, "request_key":{"type":"string"}, "offset":{"type":"integer","minimum":0}, "bytes":{"type":"integer","minimum":1}}, ["device_id", "artifact_id", "request_key", "offset"])
    else:
        _error("capability_unavailable")
    return _object({"source_version":SOURCE_SCHEMA, "request":request}, ["source_version", "request"])


def _error(code):
    raise ValueError(code)


def _string(value, code, pattern=None, maximum=8192):
    if not isinstance(value, str) or not value or len(value) > maximum or (pattern is not None and not pattern.fullmatch(value)):
        _error(code)
    return value


def _integer(value, code, minimum=0, maximum=None):
    if type(value) is not int or value < minimum or (maximum is not None and value > maximum):
        _error(code)
    return value


def _exact(value, keys, code):
    if not isinstance(value, dict) or set(value) != set(keys):
        _error(code)
    return value


def _act_request(value, capture):
    base = {"device_id", "observation_id", "request_key", "action"}
    if not isinstance(value, dict) or not base <= set(value):
        _error("device_request_invalid")
    _string(value["device_id"], "device_request_invalid", IDENTIFIER, 128)
    _string(value["observation_id"], "device_request_invalid", OBSERVATION, 64)
    _string(value["request_key"], "device_request_invalid", IDENTIFIER, 128)
    action = value["action"]
    if capture:
        _exact(value, base | {"seconds"}, "device_request_invalid")
        if action != "capture":
            _error("device_request_invalid")
        _integer(value["seconds"], "device_request_invalid", 2, 12)
        return value
    shapes = {
        "tap": base | {"x", "y"},
        "text": base | {"text"}, "back": base, "home": base,
        "launch": base | {"package"}, "open_url": base | {"package", "url"},
    }
    if action == "swipe":
        if set(value) not in (base | {"x", "y", "x2", "y2"}, base | {"x", "y", "x2", "y2", "duration_ms"}):
            _error("device_request_invalid")
    elif action not in shapes or set(value) != shapes[action]:
        _error("device_request_invalid")
    if action in {"tap", "swipe"}:
        for key in ("x", "y", "x2", "y2"):
            if key in value:
                _integer(value[key], "device_request_invalid", 0)
        if action == "swipe" and "duration_ms" in value:
            _integer(value["duration_ms"], "device_request_invalid", 100, 1500)
    elif action == "text":
        _string(value["text"], "device_request_invalid", maximum=200)
    elif action == "launch":
        _string(value["package"], "device_request_invalid", maximum=256)
    elif action == "open_url":
        _string(value["package"], "device_request_invalid", maximum=256)
        _string(value["url"], "device_request_invalid", maximum=8192)
    return value


def validate_request(operation, arguments):
    if operation not in OPERATIONS:
        _error("capability_unavailable")
    _exact(arguments, {"source_version", "request"}, "device_operation_arguments_invalid")
    source_version = _string(arguments["source_version"], "device_source_version_invalid", SHA256, 64)
    request = arguments["request"]
    if operation == "device.list":
        _exact(request, set(), "device_request_invalid")
    elif operation == "device.observe":
        _exact(request, {"device_id"}, "device_request_invalid")
        _string(request["device_id"], "device_request_invalid", IDENTIFIER, 128)
    elif operation == "device.act":
        _act_request(request, False)
    elif operation == "device.record":
        _act_request(request, True)
    else:
        if not isinstance(request, dict) or set(request) not in ({"device_id", "artifact_id", "request_key", "offset"}, {"device_id", "artifact_id", "request_key", "offset", "bytes"}):
            _error("device_request_invalid")
        _string(request["device_id"], "device_request_invalid", IDENTIFIER, 128)
        _string(request["artifact_id"], "device_request_invalid", ARTIFACT, 64)
        _string(request["request_key"], "device_request_invalid", IDENTIFIER, 128)
        _integer(request["offset"], "device_request_invalid", 0)
        if "bytes" in request:
            _integer(request["bytes"], "device_request_invalid", 1)
    return source_version, request
