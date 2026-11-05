"""Portable offline formatter. Only the host chooses the runtime directory."""
import sys
from pathlib import Path


def main():
    root = Path(sys.argv[1])
    sys.path.insert(0, str(root / "packages"))
    import sherpa_onnx as sdk

    model = sdk.OfflinePunctuationModelConfig(
        ct_transformer=str(root / "model.int8.onnx"), provider="cpu", num_threads=2)
    engine = sdk.OfflinePunctuation(sdk.OfflinePunctuationConfig(model=model))
    raw = sys.stdin.buffer.read().decode("utf-8")
    sys.stdout.buffer.write(engine.add_punctuation(raw).encode("utf-8"))


if __name__ == "__main__":
    main()
