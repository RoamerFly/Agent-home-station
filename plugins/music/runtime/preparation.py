"""Pure host-bound music preparation decisions.

This module intentionally has no filesystem, media, network, model, or host
callback access.  The host supplies verified provider/status snapshots and is
the only component allowed to materialize audio or submit the resulting plan.
"""
import copy
import math
import re


FAITHFUL_COVER_STYLE = "保持原曲调和原歌词 (preserve original melody and lyrics; follow the reference melody, rhythm and phrasing; no new melody or rewritten lyrics)"
_VOCAL_GENDER = re.compile(r"\b(?:fe)?male\s+(?:vocals?|singer|voice)\b|男声|女声", re.IGNORECASE)
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_ID = re.compile(r"[A-Za-z0-9_@-]{1,128}\Z")


def _fail(code):
    raise ValueError(code)


def _mapping(value, keys, code="music_preparation_invalid"):
    if not isinstance(value, dict) or set(value) != set(keys):
        _fail(code)
    _json(value, code)
    return value


def _json(value, code, depth=0):
    if depth > 16:
        _fail(code)
    if value is None or isinstance(value, (bool, int)):
        return
    if isinstance(value, float):
        if not math.isfinite(value):
            _fail(code)
        return
    if isinstance(value, str):
        if "\x00" in value or any(0xD800 <= ord(char) <= 0xDFFF for char in value):
            _fail(code)
        return
    if isinstance(value, list):
        for item in value:
            _json(item, code, depth + 1)
        return
    if isinstance(value, dict):
        for key, item in value.items():
            if not isinstance(key, str) or key in ("target", "token", "path"):
                _fail(code)
            _json(item, code, depth + 1)
        return
    _fail(code)


def _string(value, code):
    if not isinstance(value, str):
        _fail(code)
    _json(value, code)
    return value


def _finite(value, code, minimum=None, positive=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        _fail(code)
    if minimum is not None and value < minimum or positive and value <= 0:
        _fail(code)
    return value


def _sha(value, code):
    if not isinstance(value, str) or not _SHA256.fullmatch(value):
        _fail(code)
    return value


def _id(value, code):
    if not isinstance(value, str) or not _ID.fullmatch(value):
        _fail(code)
    return value


def _style(arguments):
    value = _mapping(arguments, ("action", "style", "gender", "mode"))
    if value["action"] != "style" or value["gender"] not in ("female", "male") or value["mode"] not in ("original", "cover"):
        _fail("music_style_invalid")
    style = _string(value["style"], "music_style_invalid")
    if value["mode"] == "cover" and FAITHFUL_COVER_STYLE not in style:
        style = style.strip() + ", " + FAITHFUL_COVER_STYLE
    style = _VOCAL_GENDER.sub("", style).strip(" ,;，；\n\r\t")
    return {"style": "" if not style else style + ", " + value["gender"] + " vocal"}


def _rvc_request(arguments):
    value = _mapping(arguments, ("action", "health", "title", "voice_profile_id", "reference_sha256", "reference_start_seconds", "reference_seconds", "audio_ref"))
    if value["action"] != "rvc_request":
        _fail("music_preparation_invalid")
    health = value["health"]
    allowed_health = {"ready", "bridge_version", "voice_profiles", "private_voice_profiles"}
    if not isinstance(health, dict) or not {"ready", "bridge_version"}.issubset(health) or not set(health).issubset(allowed_health) or not isinstance(health["ready"], bool) or isinstance(health["bridge_version"], bool) or not isinstance(health["bridge_version"], int) or any(not isinstance(health.get(key, False), bool) for key in ("voice_profiles", "private_voice_profiles")):
        _fail("rvc_bridge_not_ready")
    if not health["ready"] or health["bridge_version"] < 9:
        _fail("rvc_bridge_not_ready")
    title = _string(value["title"], "music_preparation_invalid")
    if not title.strip():
        _fail("music_preparation_invalid")
    profile = _string(value["voice_profile_id"], "music_preparation_invalid")
    if profile and not _ID.fullmatch(profile):
        _fail("music_preparation_invalid")
    if profile and (health["bridge_version"] < 12 or not health.get("voice_profiles", False)):
        _fail("rvc_profile_bridge_required")
    if profile.startswith("private-") and (health["bridge_version"] < 13 or not health.get("private_voice_profiles", False)):
        _fail("rvc_private_profile_bridge_required")
    source = _sha(value["reference_sha256"], "music_preparation_invalid")
    start = _finite(value["reference_start_seconds"], "music_preparation_invalid", minimum=0)
    seconds = _finite(value["reference_seconds"], "music_preparation_invalid", positive=True)
    audio = value["audio_ref"]
    if not isinstance(audio, dict) or set(audio) != {"ref", "sha256", "bytes"} or not isinstance(audio["ref"], str) or not re.fullmatch(r"reference_0", audio["ref"]) or _sha(audio["sha256"], "music_preparation_invalid") != audio["sha256"] or isinstance(audio["bytes"], bool) or not isinstance(audio["bytes"], int) or audio["bytes"] <= 0:
        _fail("music_preparation_invalid")
    body = {"title": title, "reference_base64": {"$music_ref": copy.deepcopy(audio)}, "reference_source_sha256": source, "reference_start_seconds": start, "reference_seconds": seconds, "reference_pretrimmed": True}
    if profile:
        body["voice_profile_id"] = profile
    return {"method": "POST", "path": "/rvc-jobs", "body": body}


def _resume_plan(arguments):
    value = _mapping(arguments, ("action", "id", "status", "restart_missing", "restart_failed"))
    if value["action"] != "resume_plan":
        _fail("music_resume_state_invalid")
    job_id = _id(value["id"], "music_resume_identity_invalid")
    status = value["status"]
    if not isinstance(status, dict) or status.get("id") != job_id:
        _fail("music_resume_identity_invalid")
    engine, state = status.get("engine"), status.get("state")
    if engine not in ("rvc", "yue"):
        _fail("music_resume_engine_invalid")
    if state not in ("queued", "running", "completed", "failed", "needs_review"):
        _fail("music_resume_state_invalid")
    missing, failed = value["restart_missing"], value["restart_failed"]
    if not isinstance(missing, bool) or not isinstance(failed, bool):
        _fail("music_resume_restart_invalid")
    if engine == "rvc" and (missing or failed):
        _fail("music_resume_restart_invalid")
    submit = state in ("failed", "needs_review")
    options = {} if engine == "rvc" else {"restart_missing": missing}
    if engine == "yue" and failed:
        options["restart_failed"] = True
    return {"engine": engine, "state": state, "submit": submit, "options": options}


def _resume_result(arguments):
    value = _mapping(arguments, ("action", "id", "engine", "status"))
    if value["action"] != "resume_result":
        _fail("music_resume_state_invalid")
    job_id = _id(value["id"], "music_resume_identity_invalid")
    engine = value["engine"]
    status = value["status"]
    if engine not in ("rvc", "yue"):
        _fail("music_resume_engine_invalid")
    if not isinstance(status, dict) or status.get("id") != job_id or status.get("engine") != engine:
        _fail("music_resume_identity_invalid")
    return {"status": copy.deepcopy(status)}


def _recover_recipe(arguments):
    value = _mapping(arguments, ("action", "id", "status"))
    if value["action"] != "recover_recipe":
        _fail("music_recovery_recipe_invalid")
    job_id = _id(value["id"], "music_recovery_recipe_invalid")
    status = value["status"]
    if not isinstance(status, dict) or status.get("id") != job_id or status.get("state") != "completed":
        _fail("music_recovery_recipe_invalid")
    recipe = status.get("recipe")
    if not isinstance(recipe, dict):
        _fail("music_recovery_recipe_invalid")
    title, mode, engine = recipe.get("title"), recipe.get("mode"), recipe.get("engine", "")
    if not isinstance(title, str) or not title.strip() or mode not in ("original", "cover") or engine not in ("", "rvc", "yue"):
        _fail("music_recovery_recipe_invalid")
    if engine != "rvc" and (not isinstance(recipe.get("lyrics"), str) or not recipe["lyrics"]):
        _fail("music_recovery_recipe_invalid")
    reference = recipe.get("reference_sha256")
    if mode == "cover" and (status.get("reference_used") is not True or not isinstance(reference, str) or not _SHA256.fullmatch(reference)):
        _fail("music_recovery_recipe_invalid")
    result = copy.deepcopy(recipe)
    source = recipe.get("reference_source_sha256")
    if isinstance(source, str) and _SHA256.fullmatch(source):
        result["reference_sha256"] = source
    return {"id": job_id, "title": title, "recipe": result}


def prepare(arguments):
    """Validate and prepare one host-bound music operation without side effects."""
    if not isinstance(arguments, dict) or not isinstance(arguments.get("action"), str):
        _fail("music_preparation_invalid")
    action = arguments["action"]
    if action == "style":
        return _style(arguments)
    if action == "rvc_request":
        return _rvc_request(arguments)
    if action == "resume_plan":
        return _resume_plan(arguments)
    if action == "resume_result":
        return _resume_result(arguments)
    if action == "recover_recipe":
        return _recover_recipe(arguments)
    _fail("music_preparation_invalid")
