"""Expose installed image checkpoints and sampler controls in the ComfyUI catalog."""

import argparse
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--registration", type=Path, required=True)
    args = parser.parse_args()
    registration = json.loads(args.registration.read_text(encoding="utf-8"))
    root = Path(registration["workflows"])
    registry_path = root / "registry.json"
    registry = json.loads(registry_path.read_text(encoding="utf-8"))
    model_dirs = [Path(registration["models"]) / "checkpoints"]
    if registration.get("model_library"):
        model_dirs.append(Path(registration["model_library"]) / "image" / "checkpoints")
    names = sorted({p.name for folder in model_dirs if folder.is_dir()
                    for p in folder.iterdir() if p.is_file() and p.suffix.lower() in {".safetensors", ".ckpt"}})
    if not names:
        raise SystemExit("No installed image checkpoints found")
    workflow = next(item for item in registry["workflows"] if item["id"] == "text-image")
    existing = {item["id"]: item for item in workflow["parameters"]}
    additions = [
        dict(id="model", label="图像模型", kind="string", max_length=256, enum=names, node="1", input="ckpt_name"),
        dict(id="negative_prompt", label="排除内容", kind="string", max_length=4000, node="3", input="text"),
        dict(id="width", label="宽度", kind="integer", minimum=64, maximum=2048, node="4", input="width"),
        dict(id="height", label="高度", kind="integer", minimum=64, maximum=2048, node="4", input="height"),
        dict(id="seed", label="随机种子", kind="integer", minimum=0, maximum=4294967295, node="5", input="seed"),
        dict(id="steps", label="采样步数", kind="integer", minimum=1, maximum=100, node="5", input="steps"),
        dict(id="cfg", label="提示词引导强度", kind="number", minimum=1, maximum=30, node="5", input="cfg"),
        dict(id="denoise", label="去噪强度", kind="number", minimum=0, maximum=1, node="5", input="denoise"),
        dict(id="sampler", label="采样器", kind="string", max_length=40, enum=["euler", "euler_ancestral", "dpmpp_2m", "dpmpp_sde"], node="5", input="sampler_name"),
        dict(id="scheduler", label="调度器", kind="string", max_length=40, enum=["normal", "karras", "exponential", "sgm_uniform"], node="5", input="scheduler"),
    ]
    workflow["parameters"] = list(existing.values()) + [item for item in additions if item["id"] not in existing]
    for item in workflow["parameters"]:
        if item["id"] == "model":
            item["enum"] = names
    registry_path.write_text(json.dumps(registry, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"text-image: {len(workflow['parameters'])} parameters, {len(names)} checkpoints")


if __name__ == "__main__":
    main()
