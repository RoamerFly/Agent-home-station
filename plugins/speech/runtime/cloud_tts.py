"""Cloud TTS request policy.  Network and credentials remain host-owned."""
import re
import urllib.parse


def _text(value, code, minimum, maximum):
    if not isinstance(value, str) or not minimum <= len(value) <= maximum or not value.strip() or "\x00" in value:
        raise ValueError(code)
    return value


def _identifier(value, code):
    value = _text(value, code, 1, 128)
    if any(char in value for char in "\r\n"):
        raise ValueError(code)
    return value


def _url(value):
    value = _text(value, "tts_response_invalid", 1, 4096)
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.fragment or "\\" in value or any(char.isspace() for char in value):
        raise ValueError("tts_response_invalid")
    return value


def compose_instruction(default_instruction, instruction):
    if default_instruction is None:
        default_instruction = ""
    if not isinstance(default_instruction, str) or len(default_instruction) > 1500 or "\x00" in default_instruction:
        raise ValueError("tts_instruction_not_configured")
    if instruction is None or instruction == "" or instruction == default_instruction:
        return default_instruction
    instruction = _text(instruction, "invalid_tts_instruction", 1, 1500)
    if default_instruction:
        combined = default_instruction + "\n本次表达要求（保持上述角色声线，只调整语气与节奏）：" + instruction
        if len(combined) > 3100:
            raise ValueError("tts_instruction_combined_too_long")
        return combined
    return instruction


def synthesize(args, config, request):
    allowed = {"text", "instruction", "timeout_seconds"}
    if not isinstance(args, dict) or set(args) - allowed:
        raise ValueError("invalid_tts_arguments")
    text = _text(args.get("text"), "invalid_tts_text", 1, 2000)
    model = _identifier(config.get("tts_model"), "tts_model_not_configured")
    voice = _identifier(config.get("default_voice"), "tts_voice_not_configured")
    instruction = compose_instruction(config.get("default_instruction", ""), args.get("instruction"))
    timeout = args.get("timeout_seconds", 180)
    if type(timeout) is not int or timeout < 1:
        raise ValueError("invalid_tts_timeout")
    body = {
        "model": model,
        "input": {
            "text": text,
            "voice": voice,
            "format": "wav",
            "sample_rate": 24000,
            "instruction": instruction,
        },
    }
    reply = request({"route": "synthesize", "body": body, "max_bytes": 1024 * 1024, "timeout_seconds": timeout})
    if not isinstance(reply, dict) or reply.get("error"):
        raise ValueError("tts_provider_failed")
    output = reply.get("output")
    audio = output.get("audio") if isinstance(output, dict) else None
    url = audio.get("url") if isinstance(audio, dict) else None
    return {
        "ok": True,
        "status": "provider_accepted",
        "provider_audio_url": _url(url),
        "provider_model": reply.get("model") if isinstance(reply.get("model"), str) else None,
        "voice": voice,
        "coverage": "provider_tts_response",
        "untrusted_external_content": True,
    }
