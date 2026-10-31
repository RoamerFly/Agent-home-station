"""Bailian ASR protocol. Audio bytes and credentials remain host-owned."""
import re


def transcribe(args, config, request):
    if not isinstance(args, dict) or set(args) - {"audio", "timeout_seconds"}:
        raise ValueError("invalid_transcription_arguments")
    audio = args.get("audio")
    if not isinstance(audio, dict) or set(audio) != {"media_ref", "sha256", "version", "mime"}:
        raise ValueError("invalid_audio_reference")
    if not isinstance(audio["media_ref"], str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", audio["media_ref"]):
        raise ValueError("invalid_audio_reference")
    if not isinstance(audio["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", audio["sha256"]) or type(audio["version"]) is not int or audio["version"] < 1 or audio["mime"] != "audio/wav":
        raise ValueError("invalid_audio_reference")
    model = config.get("asr_model")
    if not isinstance(model, str) or not model.strip() or len(model) > 128 or any(c in model for c in "\r\n\x00"):
        raise ValueError("asr_model_not_configured")
    timeout = args.get("timeout_seconds", 180)
    if type(timeout) is not int or timeout < 1:
        raise ValueError("invalid_transcription_timeout")
    body = {"model": model, "input": {"messages": [{"role": "user", "content": [{"type": "input_audio", "input_audio": {"data": {"$media_ref": audio}}}]}]}, "parameters": {"format": "wav", "sample_rate": "16000"}}
    reply = request({"route": "transcription", "body": body, "max_bytes": 1048576, "timeout_seconds": timeout})
    if not isinstance(reply, dict):
        raise ValueError("asr_response_invalid")
    if reply.get("code") == "ASR_RESPONSE_HAVE_NO_WORDS":
        return {"ok": True, "status": "no_recognizable_speech", "text": "", "sources": [audio], "coverage": "provided_audio_only", "untrusted_external_content": True}
    if reply.get("code"):
        raise ValueError("asr_provider_failed")
    output = reply.get("output")
    if not isinstance(output, dict):
        raise ValueError("asr_response_invalid")
    text = output.get("text")
    nested = output.get("output")
    if not text and isinstance(nested, dict):
        text = nested.get("text")
    if not isinstance(text, str) or not text.strip():
        raise ValueError("asr_response_empty")
    # Transport bounds the response. Preserve all returned text, never silently
    # truncate it to the old 16,000-byte preference.
    return {"ok": True, "status": "transcribed", "text": text.strip(), "sources": [audio], "coverage": "provided_audio_only", "untrusted_external_content": True}
