"""Cloud music request policy. Network and credentials remain host-owned."""
import re
import urllib.parse


def _has_surrogate(value):
    return any(0xD800 <= ord(char) <= 0xDFFF for char in value)


def _text(value, code, maximum, allow_empty=False):
    if not isinstance(value, str) or len(value) > maximum or "\x00" in value or _has_surrogate(value):
        raise ValueError(code)
    if not allow_empty and not value.strip():
        raise ValueError(code)
    return value


def _identifier(value, code):
    value = _text(value, code, 128)
    if any(char in value for char in "\r\n"):
        raise ValueError(code)
    return value


def _url(value):
    value = _text(value, "music_response_invalid", 4096)
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or
            parsed.password or parsed.fragment or "\\" in value or any(char.isspace() for char in value)):
        raise ValueError("music_response_invalid")
    return value


def generate(args, config, request):
    allowed = {"prompt", "lyrics", "timeout_seconds"}
    if not isinstance(args, dict) or set(args) - allowed:
        raise ValueError("invalid_music_arguments")
    prompt = _text(args.get("prompt", ""), "invalid_music_prompt", 2000, allow_empty=True)
    lyrics = args.get("lyrics")
    if lyrics is not None:
        lyrics = _text(lyrics, "invalid_music_lyrics", 2000, allow_empty=True)
    if not (prompt.strip() or (lyrics or "").strip()):
        raise ValueError("invalid_music_prompt")
    model = _identifier(config.get("music_model"), "music_model_not_configured")
    timeout = args.get("timeout_seconds", 180)
    if type(timeout) is not int or timeout < 1:
        raise ValueError("invalid_music_timeout")
    input_data = {"prompt": prompt, "gender": "female", "format": "mp3"}
    if lyrics:
        input_data["lyrics"] = lyrics
    reply = request({"route": "generate", "body": {"model": model, "input": input_data}, "max_bytes": 1024 * 1024, "timeout_seconds": timeout})
    if not isinstance(reply, dict) or reply.get("error"):
        raise ValueError("music_provider_failed")
    output = reply.get("output")
    audio = output.get("audio") if isinstance(output, dict) else None
    url = audio.get("url") if isinstance(audio, dict) else None
    provider_model = reply.get("model")
    if provider_model is not None and not isinstance(provider_model, str):
        raise ValueError("music_response_invalid")
    return {
        "ok": True,
        "status": "provider_accepted",
        "provider_audio_url": _url(url),
        "provider_model": provider_model,
        "coverage": "provider_music_response",
        "untrusted_external_content": True,
    }
