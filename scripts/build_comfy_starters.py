"""Build the versioned ComfyUI API graphs bundled for the fish workspace."""

import hashlib
import json
from pathlib import Path


OUT = Path(__file__).resolve().parents[1] / "internal/station/comfy_starters"
QWEN = "qwen_image_2.1_int8_convrot.safetensors"
QWEN_CLIP = "qwen3vl_8b_int8_convrot-文本编码器-提示理解.safetensors"
QWEN_VAE = "qwen_image_2.1_vae_bf16-解码VAE-图像解码.safetensors"
H3 = "minimax_h3_ref2va_pruned_int8_convrot-视频扩散-视频生成.safetensors"
H3_CLIP = "qwen3vl_32b_minimax_h3_nvfp4_awq-文本编码器-文本理解.safetensors"
H3_VAE = "minimax_h3_video_vae_int8_convrot-解码VAE-视频VAE-视频解码.safetensors"
H3_AUDIO = "minimax_h3_audio_vae_fp32-解码VAE-视频VAE-视频解码.safetensors"


def node(kind, **inputs):
    return {"class_type": kind, "inputs": inputs}


def link(num, slot=0):
    return [str(num), slot]


def parameter(pid, label, kind, num, field, **options):
    return {"id": pid, "label": label, "kind": kind, "node": str(num), "input": field, **options}


def qwen_graph(mode):
    g = {
        "1": node("UNETLoader", unet_name=QWEN, weight_dtype="default"),
        "2": node("CLIPLoader", clip_name=QWEN_CLIP, type="qwen_image", device="default"),
        "3": node("VAELoader", vae_name=QWEN_VAE),
        "4": node("TextEncodeQwenImage21", clip=link(2), prompt="Detailed, coherent composition, natural light, refined textures", negative_prompt="", resolution=1024),
        "6": node("KSampler", model=link(1), positive=link(4), negative=link(4, 1), latent_image=link(5), seed=20261124, steps=40, cfg=1.0, sampler_name="euler", scheduler="simple", denoise=1.0),
        "7": node("VAEDecode", samples=link(6), vae=link(3)),
        "8": node("SaveImage", images=link(7), filename_prefix="qwen21"),
    }
    params = [parameter("prompt", "画面描述", "string", 4, "prompt", max_length=8192),
              parameter("negative_prompt", "排除内容", "string", 4, "negative_prompt", max_length=4096),
              parameter("seed", "随机种子", "integer", 6, "seed", minimum=0, maximum=4294967295),
              parameter("steps", "采样步数", "integer", 6, "steps", minimum=1, maximum=100),
              parameter("cfg", "引导强度", "number", 6, "cfg", minimum=1, maximum=10)]
    if mode == "text-to-image":
        g["5"] = node("EmptyLatentImage", width=1536, height=1024, batch_size=1)
        params += [parameter("width", "宽度", "integer", 5, "width", minimum=256, maximum=2048),
                   parameter("height", "高度", "integer", 5, "height", minimum=256, maximum=2048)]
    else:
        g["9"] = node("LoadImage", image="source.png")
        g["4"]["inputs"].update(vae=link(3), **{"images.image_1": link(9)})
        g["5"] = node("SetLatentNoiseMask", samples=link(11), mask=link(10)) if mode == "mask-inpaint" else node("Reroute", **{"": link(4, 2)})
        if mode != "mask-inpaint":
            del g["5"]
            g["6"]["inputs"]["latent_image"] = link(4, 2)
        else:
            g["10"] = node("LoadImageMask", image="mask.png", channel="red")
            g["11"] = node("VAEEncode", pixels=link(9), vae=link(3))
            params.append(parameter("mask_image", "白色修复区域的蒙版", "image", 10, "image", required=True))
        params += [parameter("input_image", "参考底图", "image", 9, "image", required=True),
                   parameter("resolution", "参考图处理分辨率", "integer", 4, "resolution", minimum=256, maximum=2048),
                   parameter("denoise", "改动强度", "number", 6, "denoise", minimum=0, maximum=1)]
        g["6"]["inputs"]["denoise"] = 1.0 if mode == "image-to-image" else 0.8
    return g, params


def h3_graph(mode, image_count=0):
    g = {
        "1": node("UNETLoader", unet_name=H3, weight_dtype="default"),
        "2": node("CLIPLoader", clip_name=H3_CLIP, type="minimax", device="default"),
        "3": node("VAELoader", vae_name=H3_VAE),
        "4": node("VAELoader", vae_name=H3_AUDIO),
        "5": node("MiniMaxH3ReferenceToVideo", clip=link(2), vae=link(3), audio_vae=link(4), prompt="A cinematic scene with coherent movement and natural sound", width=1344, height=768, length=124, ref_image_size="match"),
        "6": node("RandomNoise", noise_seed=20261124),
        "7": node("BasicGuider", model=link(1), conditioning=link(5)),
        "8": node("KSamplerSelect", sampler_name="res_multistep"),
        "9": node("BasicScheduler", model=link(1), scheduler="simple", steps=20, denoise=1.0),
        "10": node("SamplerCustomAdvanced", noise=link(6), guider=link(7), sampler=link(8), sigmas=link(9), latent_image=link(5, 1)),
        "11": node("VAEDecode", samples=link(10), vae=link(3)),
        "12": node("VAEDecodeAudio", samples=link(10), vae=link(4)),
        "13": node("CreateVideo", images=link(11), audio=link(12), fps=24),
        "14": node("SaveVideo", video=link(13), filename_prefix="minimax-h3", format="mp4", codec={"codec": "h264"}),
    }
    params = [parameter("prompt", "视频描述，可用 <Picture 1> 和 <Video 1>", "string", 5, "prompt", max_length=8192),
              parameter("width", "宽度", "integer", 5, "width", minimum=256, maximum=1344),
              parameter("height", "高度", "integer", 5, "height", minimum=256, maximum=1344),
              parameter("length", "帧数，124 约为 5 秒", "integer", 5, "length", minimum=5, maximum=362),
              parameter("steps", "采样步数", "integer", 9, "steps", minimum=1, maximum=50),
              parameter("seed", "随机种子", "integer", 6, "noise_seed", minimum=0, maximum=4294967295)]
    for i in range(image_count):
        nid = 20 + i
        g[str(nid)] = node("LoadImage", image=f"reference-{i+1}.png")
        g["5"]["inputs"][f"ref_images.ref_image_{i}"] = link(nid)
        params.append(parameter(f"input_image_{i+1}", f"参考图 {i+1}", "image", nid, "image", required=True))
    if mode in ("reference-video", "universal-single", "universal-multi"):
        g["30"] = node("LoadVideo", file="reference.mp4")
        g["31"] = node("GetVideoComponents", video=link(30))
        g["5"]["inputs"]["ref_videos.ref_video_0"] = link(31)
        params.append(parameter("input_video", "参考视频文件名", "string", 30, "file", max_length=256, required=True))
    return g, params


def build():
    items = []
    recipes = [
        ("image", "qwen21-text-to-image-hq", "文生图 · Qwen Image 2.1 高质量", *qwen_graph("text-to-image")),
        ("image", "qwen21-image-to-image-hq", "图生图 · Qwen Image 2.1", *qwen_graph("image-to-image")),
        ("image", "qwen21-mask-inpaint-hq", "蒙版修复 · Qwen Image 2.1", *qwen_graph("mask-inpaint")),
        ("video", "h3-text-to-video", "文生视频 · MiniMax H3", *h3_graph("text-video")),
        ("video", "h3-image-to-video", "图生视频 · MiniMax H3", *h3_graph("image-video", 1)),
        ("video", "h3-reference-video", "参考视频生视频 · MiniMax H3", *h3_graph("reference-video")),
        ("video", "h3-universal-single", "全能参考 · 单图加视频", *h3_graph("universal-single", 1)),
        ("video", "h3-universal-multi", "全能参考 · 多图加视频", *h3_graph("universal-multi", 3)),
    ]
    for directory, rid, label, graph, params in recipes:
        path = OUT / directory / f"{rid}.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        data = (json.dumps(graph, ensure_ascii=False, indent=2) + "\n").encode()
        path.write_bytes(data)
        items.append({"id": rid, "label": label, "graph": f"{directory}/{path.name}",
                      "sha256": hashlib.sha256(data).hexdigest(), "save_node": "8" if directory == "image" else "14", "parameters": params})
    registry = {"schema_version": 1, "comfyui_version": "0.37.2", "workflows": items}
    (OUT / "registry.json").write_text(json.dumps(registry, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    build()
