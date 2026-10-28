"""Offline SenseVoice ONNX transcription. Paths come from a managed manifest.

No download, network or API key. Input is normalized 16kHz mono PCM16 WAV.
Chunk duration, CPU threads and language are defaults exposed as CLI options.
"""
import argparse
import json
import re
import sys
import time
import wave


def clean_text(raw):
    return re.sub(r"<\|[^|]*\|>", "", raw).strip()


def speech_detected(samples, args, sherpa):
    if not args.vad_model:
        return True
    config = sherpa.VadModelConfig()
    config.silero_vad.model = args.vad_model
    config.silero_vad.threshold = args.vad_threshold
    config.silero_vad.min_silence_duration = .25
    config.silero_vad.min_speech_duration = args.vad_min_speech
    config.silero_vad.max_speech_duration = args.chunk_seconds
    config.sample_rate = 16000
    config.num_threads = args.threads
    vad = sherpa.VoiceActivityDetector(config, buffer_size_in_seconds=args.chunk_seconds + 2)
    step = config.silero_vad.window_size
    for start in range(0, len(samples), step):
        vad.accept_waveform(samples[start:start+step])
        if not vad.empty():
            return True
    vad.flush()
    return not vad.empty()


def transcribe(args):
    if args.threads < 1 or args.chunk_seconds <= 0:
        raise ValueError("invalid_asr_parameters")
    if args.packages:
        sys.path.insert(0, args.packages)
    import numpy as np
    import sherpa_onnx
    started = time.monotonic()
    with wave.open(args.input, "rb") as audio:
        if (audio.getnchannels(), audio.getsampwidth(), audio.getframerate()) != (1, 2, 16000):
            raise ValueError("asr_requires_pcm16_mono_16000")
        recognizer = None
        total = audio.getnframes()
        batch = max(1, round(args.chunk_seconds * 16000))
        segments = []
        position = 0
        while position < total:
            raw = audio.readframes(batch)
            samples = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
            end = position + len(samples)
            detected = speech_detected(samples, args, sherpa_onnx)
            text, tags = "", []
            if detected:
                if recognizer is None:
                    recognizer = sherpa_onnx.OfflineRecognizer.from_sense_voice(
                        model=args.model, tokens=args.tokens, num_threads=args.threads,
                        use_itn=True, language=args.language, provider="cpu", debug=False)
                stream = recognizer.create_stream()
                stream.accept_waveform(16000, samples)
                recognizer.decode_stream(stream)
                text = clean_text(stream.result.text)
                tags = re.findall(r"<\|[^|]*\|>", stream.result.text)
            segments.append({"start_seconds": position / 16000, "end_seconds": end / 16000,
                             "text": text, "raw_tags": tags, "speech_detected": detected})
            position = end
    text = "\n".join(x["text"] for x in segments if x["text"])
    return {"text": text, "state": "transcribed" if text else "no_recognizable_speech",
            "segments": segments, "processed_seconds": position / 16000, "range_complete": position == total,
            "backend": "sensevoice_onnx", "provider": "cpu", "threads": args.threads,
            "chunk_seconds": args.chunk_seconds, "language": args.language,
            "vad_enabled": bool(args.vad_model),
            "elapsed_seconds": round(time.monotonic() - started, 3)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("input", "model", "tokens"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--packages")
    parser.add_argument("--threads", type=int, default=2)
    parser.add_argument("--language", default="auto", choices=["auto", "zh", "en", "ja", "ko", "yue"])
    parser.add_argument("--chunk-seconds", type=float, default=30)
    parser.add_argument("--vad-model")
    parser.add_argument("--vad-threshold", type=float, default=.5)
    parser.add_argument("--vad-min-speech", type=float, default=.25)
    args = parser.parse_args()
    try:
        print(json.dumps(transcribe(args), ensure_ascii=False))
    except Exception as error:
        # No input text, paths or raw decoder diagnostics in the public result.
        print(json.dumps({"error": "local_asr_inference_failed", "error_type": type(error).__name__}))
        raise SystemExit(1)


if __name__ == "__main__":
    main()
