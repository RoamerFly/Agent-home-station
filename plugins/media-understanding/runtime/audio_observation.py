"""Bounded audio-content observation via the host model route."""
import re


def _positive(value):
    return type(value) is int and value > 0


def _audio_reference(value):
    if not isinstance(value, dict) or set(value) != {"media_ref", "sha256", "version", "mime"}:
        raise ValueError("invalid_audio_reference")
    if not isinstance(value.get("media_ref"), str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", value["media_ref"]):
        raise ValueError("invalid_audio_reference")
    if not isinstance(value.get("sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", value["sha256"]):
        raise ValueError("invalid_audio_reference")
    if type(value.get("version")) is not int or value["version"] < 1 or value.get("mime") != "audio/wav":
        raise ValueError("invalid_audio_reference")
    return value


def observe(args, config, request):
    if not isinstance(args, dict) or set(args) - {"audio", "prompt", "max_tokens", "timeout_seconds"}:
        raise ValueError("invalid_audio_observation_arguments")
    audio = _audio_reference(args.get("audio"))
    prompt = args.get("prompt")
    # Python's string length and Go's RuneCount both count Unicode code points.
    # This is the package contract, not a provider byte or token limit.
    if not isinstance(prompt, str) or not prompt.strip() or len(prompt) > 8192:
        raise ValueError("invalid_audio_observation_prompt")
    model = config.get("audio_review_model") if isinstance(config, dict) else None
    if not isinstance(model, str) or not model.strip() or len(model) > 128 or any(char in model for char in "\r\n\x00"):
        raise ValueError("audio_review_model_not_configured")
    tokens = args.get("max_tokens", config.get("max_tokens", 768))
    timeout = args.get("timeout_seconds", 90)
    if not _positive(tokens) or not _positive(timeout):
        raise ValueError("invalid_audio_observation_parameters")
    body = {
        "model": model,
        "stream": True,
        "enable_thinking": False,
        "max_tokens": tokens,
        "messages": [{"role": "user", "content": [
            {"type": "text", "text": prompt},
            {"type": "input_audio", "input_audio": {"data": {"$media_ref": audio}, "format": "wav"}},
        ]}],
    }
    reply = request({"route": "audio-review", "body": body, "max_bytes": 1024 * 1024, "timeout_seconds": timeout})
    if not isinstance(reply, dict) or reply.get("error"):
        raise ValueError("audio_review_provider_failed")
    choices = reply.get("choices")
    if not isinstance(choices, list) or len(choices) != 1 or not isinstance(choices[0], dict):
        raise ValueError("audio_review_response_invalid")
    choice = choices[0]
    message = choice.get("message")
    if not isinstance(message, dict) or "tool_calls" in message or "function_call" in message:
        raise ValueError("audio_review_response_invalid")
    observation, finish = message.get("content"), choice.get("finish_reason")
    if finish not in ("stop", "length") or not isinstance(observation, str) or not observation.strip():
        raise ValueError("audio_review_response_incomplete")
    return {
        "ok": True,
        "status": "partial" if finish == "length" else "observed",
        "observation": observation.strip(),
        "sources": [audio],
        "coverage": "provided_audio_only",
        "untrusted_external_content": True,
    }
