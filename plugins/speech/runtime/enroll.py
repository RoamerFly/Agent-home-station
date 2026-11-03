"""Private GPT-SoVITS enrollment worker.

The host runs this signed package in a private job directory with
``--manifest``.  Samples are relative to that directory; project, runtime and
base-model paths are host-bound absolute paths.  It never discovers recordings,
opens credentials, or sends WeChat messages.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import wave
import struct

PROTOCOL_VERSION = 1
RESULT = "result.json"
RUNNING = ".running.json"
CANCEL = "cancel.json"
HEX = set("0123456789abcdef")


def sha(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def write_json(path, value):
    with tempfile.NamedTemporaryFile("w", encoding="utf8", dir=path.parent, delete=False) as f:
        pending = Path(f.name)
        json.dump(value, f, ensure_ascii=False, sort_keys=True, indent=2)
        f.flush(); os.fsync(f.fileno())
    try: os.replace(pending, path)
    finally: pending.unlink(missing_ok=True)


def text(value, field, maximum=4000):
    if not isinstance(value, str) or not value.strip() or len(value) > maximum or "\0" in value:
        raise ValueError(field + "_invalid")
    return value.strip()


def hex64(value, field):
    if not isinstance(value, str) or len(value) != 64 or set(value) - HEX:
        raise ValueError(field + "_invalid")
    return value


def rel(job, value, field):
    if not isinstance(value, str) or not value or "\0" in value or Path(value).is_absolute():
        raise ValueError(field + "_must_be_relative")
    path = (job / value).resolve()
    if not path.is_relative_to(job): raise ValueError(field + "_outside_job")
    return path


def absolute(value, field, directory=False):
    if not isinstance(value, str) or not value or "\0" in value or not Path(value).is_absolute():
        raise ValueError(field + "_must_be_absolute")
    path = Path(value).resolve()
    if (directory and not path.is_dir()) or (not directory and not path.is_file()) or path.is_symlink():
        raise ValueError(field + "_missing")
    return path


def manifest(path):
    path = path.resolve()
    raw = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(raw, dict) or raw.get("protocol_version") != PROTOCOL_VERSION:
        raise ValueError("protocol_version_unsupported")
    if raw.get("action") not in {"prepare", "preview", "train", "status", "cancel"}:
        raise ValueError("action_invalid")
    text(raw.get("job_id"), "job_id", 128); text(raw.get("profile_id"), "profile_id", 128)
    if not isinstance(raw.get("revision"), int) or raw["revision"] < 1: raise ValueError("revision_invalid")
    return path.parent, raw


def base(raw, state, reason=""):
    return {"protocol_version": PROTOCOL_VERSION, "state": state, "reason": reason,
            "job_id": raw["job_id"], "profile_id": raw["profile_id"], "revision": raw["revision"], "wechat_sends": 0}


def sample_metrics(path):
    if path.suffix.lower() != ".wav": raise ValueError("sample_decode_required")
    try:
        with wave.open(str(path), "rb") as f:
            rate, frames, channels, width = f.getframerate(), f.getnframes(), f.getnchannels(), f.getsampwidth()
            if rate <= 0 or frames <= 0 or channels < 1 or width not in {1, 2, 3, 4}: raise ValueError("sample_audio_invalid")
            seconds = frames / rate
            # Coarse local VAD only: this is useful for spotting all-silence
            # samples but is not a speaker diarizer.  A host-provided,
            # versioned diarization observation is required to claim one
            # speaker before training.
            block = max(1, int(rate * .02))
            peak, blocks = 0.0, []
            for start in range(0, frames, block):
                raw = f.readframes(min(block, frames - start))
                if not raw: continue
                if width == 1:
                    values = [(x - 128) / 128.0 for x in raw]
                elif width == 2:
                    values = [x[0] / 32768.0 for x in struct.iter_unpack("<h", raw)]
                elif width == 3:
                    values = []
                    for pos in range(0, len(raw) - 2, 3):
                        value = int.from_bytes(raw[pos:pos + 3], "little", signed=True)
                        values.append(value / 8388608.0)
                else:
                    values = [x[0] / 2147483648.0 for x in struct.iter_unpack("<i", raw)]
                rms = (sum(value * value for value in values) / max(1, len(values))) ** .5
                peak = max(peak, rms); blocks.append((len(raw) / (width * channels) / rate, rms))
            threshold = max(.004, peak * .06)
            voiced = sum(duration for duration, rms in blocks if rms >= threshold)
            return {"seconds": seconds, "effective_voiced_seconds": round(voiced, 3),
                    "silence_seconds": round(max(0.0, seconds - voiced), 3),
                    "signal": "silent_or_near_silent" if voiced < min(.3, seconds * .1) else "speech_candidate"}
    except wave.Error as e: raise ValueError("sample_audio_invalid") from e


def samples(job, raw):
    values = raw.get("samples")
    if not isinstance(values, list) or not values: raise ValueError("samples_required")
    out, seen = [], set()
    for item in values:
        if not isinstance(item, dict): raise ValueError("sample_invalid")
        audio = rel(job, item.get("audio"), "sample_audio")
        if not audio.is_file() or audio.is_symlink(): raise ValueError("sample_audio_missing")
        expected = hex64(item.get("sha256"), "sample_sha256")
        actual = sha(audio)
        if actual != expected: raise ValueError("sample_hash_mismatch")
        if actual in seen: raise ValueError("sample_duplicate")
        seen.add(actual)
        metrics = sample_metrics(audio)
        observation = item.get("speaker_observation")
        attestation = item.get("owner_attestation")
        speaker_state, speaker_count = "unknown_requires_diarization", None
        if isinstance(observation, dict) and observation.get("audio_sha256") == actual and isinstance(observation.get("analyzer_version"), str):
            count = observation.get("speaker_count")
            if isinstance(count, int) and 1 <= count <= 32:
                speaker_count, speaker_state = count, "analyzer_reported"
        # A verified owner may explicitly attest that an otherwise
        # un-diarized sample is their own single-speaker recording.  This is a
        # distinct state, never presented as a model/analyser result, and is
        # bound to both the immutable sample and the originating instruction.
        if speaker_count is None and isinstance(attestation, dict) and attestation.get("kind") == "owner_attested_single_speaker":
            if attestation.get("audio_sha256") == actual and attestation.get("message_id") == item.get("message_id") and isinstance(attestation.get("instruction_message_id"), str) and attestation["instruction_message_id"]:
                speaker_count, speaker_state = 1, "owner_attested_single_speaker"
        out.append({"audio": audio, "sha256": actual, "transcript": text(item.get("transcript"), "sample_transcript", 1600),
                    "message_id": text(item.get("message_id"), "sample_message_id", 256), "speaker_count": speaker_count,
                    "speaker_state": speaker_state, **metrics})
    return out


def copy(source, target):
    target.parent.mkdir(parents=True, exist_ok=True); target.unlink(missing_ok=True)
    try: os.link(source, target)
    except OSError: shutil.copy2(source, target)


def models(raw, train=False):
    values = raw.get("base_models")
    if not isinstance(values, dict): raise ValueError("base_models_required")
    result = {}
    for key in (["gpt", "sovits", "discriminator"] if train else ["gpt", "sovits"]):
        path = absolute(values.get(key + "_path"), key + "_path")
        if sha(path) != hex64(values.get(key + "_sha256"), key + "_sha256"): raise ValueError(key + "_hash_mismatch")
        result[key] = path
    return result


def runtime(raw):
    project = absolute(raw.get("project"), "project", True)
    root = absolute(raw.get("models"), "models", True)
    packages = raw.get("packages"); packages = absolute(packages, "packages", True) if packages else None
    python = absolute(raw.get("python") or sys.executable, "python")
    if not (project / "GPT_SoVITS/TTS_infer_pack/TTS.py").is_file(): raise ValueError("gpt_sovits_project_invalid")
    for name in ("chinese-roberta-wwm-ext-large", "chinese-hubert-base"):
        if not (root / name / "config.json").is_file(): raise ValueError("base_model_missing_" + name)
    return project, root, packages, python


def prepared(job, raw):
    entries = samples(job, raw)
    result = base(raw, "prepared")
    result.update({"sample_count":len(entries), "sample_seconds":round(sum(x["seconds"] for x in entries), 3),
                   "effective_voiced_seconds":round(sum(x["effective_voiced_seconds"] for x in entries), 3),
                   "silence_seconds":round(sum(x["silence_seconds"] for x in entries), 3),
                   "sample_sha256":[x["sha256"] for x in entries],
                   "speaker_check":"passed" if entries and all(x["speaker_state"] == "analyzer_reported" and x["speaker_count"] == 1 for x in entries) else ("owner_attested" if entries and all(x["speaker_state"] == "owner_attested_single_speaker" for x in entries) else "requires_diarization"),
                   "quality":"decoded_local_vad_only"})
    return entries, result


def runner():
    spec = importlib.util.spec_from_file_location("speech_enroll_gpt", Path(__file__).with_name("gpt_sovits.py"))
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module


def reference_sample(entries):
    """Choose an intact GPT-SoVITS reference; never crop against its text."""
    eligible = [entry for entry in entries
                if entry["speaker_count"] == 1
                and entry["signal"] == "speech_candidate"
                and 3.0 <= entry["seconds"] <= 10.0
                and entry["effective_voiced_seconds"] >= 1.5]
    if not eligible:
        # The caller must supply a real short utterance with its own matching
        # transcript.  Silently slicing a longer recording invalidates text
        # alignment and makes a misleading reference preview.
        raise ValueError("reference_sample_needs_clear_single_speaker_3_to_10_seconds")
    return max(eligible, key=lambda entry: (entry["effective_voiced_seconds"], -entry["silence_seconds"]))


def preview(job, raw, weights=None):
    entries, prepared_result = prepared(job, raw)
    gpt = models(raw) if weights is None else weights
    project, root, packages, _ = runtime(raw)
    reference = reference_sample(entries)
    ref = job / "reference.wav"; copy(reference["audio"], ref)
    output = job / "preview.wav"; output.unlink(missing_ok=True)
    device = raw.get("device", "cuda")
    if device not in {"cpu", "cuda", "cuda:0"}: raise ValueError("device_invalid")
    version = raw.get("version", "v2")
    if version not in {"v1","v2","v2Pro","v2ProPlus","v3","v4","v5dev","v5turbo"}: raise ValueError("version_invalid")
    args = argparse.Namespace(project=project, models=root, packages=packages, gpt=gpt["gpt"], sovits=gpt["sovits"], reference=ref,
        prompt_text=reference["transcript"], prompt_text_file=None, text=text(raw.get("preview_text"), "preview_text", 1200), output=output,
        device=device, version=version, half=device != "cpu", text_lang=raw.get("text_lang", "all_zh"), prompt_lang=raw.get("prompt_lang", "all_zh"), parameters=None, overwrite=False)
    write_json(job / RUNNING, {"job_id":raw["job_id"], "revision":raw["revision"], "stage":"reference_preview", "started_unix":time.time()})
    reply = runner().synthesize(args)
    if not output.is_file() or output.stat().st_size < 44: raise ValueError("preview_output_missing")
    result = dict(prepared_result)
    result.update(base(raw, "reference_preview_ready"))
    result.update({"preview_sha256":sha(output), "reference_sha256":sha(ref), "reference_text":reference["transcript"], "reference_sample_sha256":reference["sha256"],
                   "sample_rate":reply["sample_rate"], "seconds":reply["seconds"], "base_gpt_sha256":sha(gpt["gpt"]), "base_sovits_sha256":sha(gpt["sovits"])})
    return result


def is_cancelled(job, raw):
    try:
        value = json.loads((job / CANCEL).read_text(encoding="utf8"))
        return value.get("job_id") == raw["job_id"] and value.get("revision") == raw["revision"]
    except (OSError, json.JSONDecodeError): return False


def run(command, cwd, env, job, raw, stage):
    write_json(job / RUNNING, {"job_id":raw["job_id"],"revision":raw["revision"],"stage":stage,"started_unix":time.time()})
    # The managed Windows interpreter has a python._pth file and intentionally
    # ignores PYTHONPATH.  Bootstrap the two checkout roots explicitly for
    # upstream dataset scripts, without modifying the read-only project.
    if env.get("WECHAT_GPT_SOVITS_PATHS"):
        bootstrap = 'import os,runpy,sys,types;sys.path[:0]=os.environ["WECHAT_GPT_SOVITS_PATHS"].split(os.pathsep);sys.modules.setdefault("gradio",types.SimpleNamespace(Warning=lambda *a,**k:None));script=sys.argv[1];sys.argv=sys.argv[1:];runpy.run_path(script,run_name="__main__")'
        command = [command[0], "-X", "utf8", "-c", bootstrap, *command[1:]]
    with (job / "training.log").open("ab") as output:
        options = {}
        if os.name == "nt":
            options["creationflags"] = getattr(subprocess, "CREATE_NO_WINDOW", 0)
        proc = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                stdout=output, stderr=subprocess.STDOUT, **options)
        while proc.poll() is None:
            if is_cancelled(job, raw):
                if os.name == "nt": subprocess.run(["taskkill","/PID",str(proc.pid),"/T","/F"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
                else: proc.terminate()
                proc.wait(timeout=20); raise ValueError("cancelled")
            time.sleep(.25)
    if proc.returncode: raise ValueError("training_stage_failed_" + stage)


def stage_marker(exp, stage):
    return exp / ("." + stage + ".verified.json")


def stage_reusable(exp, stage, entries, expected):
    """Reuse preprocessing only when this exact immutable sample batch completed.

    A directory being present is not evidence that an interrupted upstream
    script wrote every required output.  The marker is deliberately local to
    a job and binds every source hash plus each non-empty output.
    """
    try:
        value = json.loads(stage_marker(exp, stage).read_text(encoding="utf8"))
    except (OSError, json.JSONDecodeError):
        return False
    if value.get("sample_sha256") != [item["sha256"] for item in entries]:
        return False
    return stage_outputs_valid(stage, entries, expected)


def stage_outputs_valid(stage, entries, expected):
    if not all(path.is_file() and path.stat().st_size > 0 for path in expected):
        return False
    # Upstream preprocessing can report an individual text failure while
    # exiting successfully.  A non-empty CRLF file is therefore not a valid
    # text stage: every immutable input must have a usable output row.
    if stage in {"prepare_text", "prepare_semantic"}:
        index = expected[0]
        try:
            rows = [line.split("\t") for line in index.read_text(encoding="utf8").splitlines() if line.strip()]
        except OSError:
            return False
        minimum = 4 if stage == "prepare_text" else 2
        names = {row[0] for row in rows if len(row) >= minimum and all(part.strip() for part in row[:minimum])}
        required = {entry["sha256"] + ".wav" for entry in entries}
        return required.issubset(names)
    return True


def mark_stage(exp, stage, entries, expected):
    write_json(stage_marker(exp, stage), {
        "sample_sha256": [item["sha256"] for item in entries],
        "outputs": [{"name": str(path.relative_to(exp)).replace("\\", "/"), "sha256": sha(path)} for path in expected],
    })


def train(job, raw):
    if raw.get("confirm_train") is not True: raise ValueError("explicit_training_confirmation_required")
    if raw.get("version", "v2") != "v2": raise ValueError("training_version_not_supported")
    entries, prepared_result = prepared(job, raw); weight = models(raw, True); project, root, packages, python = runtime(raw)
    # This overlay is registered by the host exclusively for the training
    # subprocess.  Do not add it to preview/inference: it intentionally holds
    # TensorBoard and other training-only dependencies.
    training_packages = raw.get("training_packages")
    if training_packages:
        training_packages = absolute(training_packages, "training_packages", True)
    if prepared_result["effective_voiced_seconds"] < 3: raise ValueError("training_samples_voice_too_short")
    if prepared_result["speaker_check"] not in {"passed", "owner_attested"}: raise ValueError("training_samples_speaker_check_required")
    if is_cancelled(job, raw): raise ValueError("cancelled")
    dataset, wavs = job / "dataset", job / "dataset/wavs"; wavs.mkdir(parents=True, exist_ok=True)
    rows=[]
    for entry in entries:
        target=wavs/(entry["sha256"]+".wav"); copy(entry["audio"],target)
        rows.append(f'{target}|{raw["profile_id"]}|zh|{entry["transcript"].replace(chr(10), " ")}')
    source = dataset / "name2text.txt"; source.write_text("\n".join(rows)+"\n",encoding="utf8")
    exp, output = job / "experiment", job / "trained"; exp.mkdir(exist_ok=True); output.mkdir(exist_ok=True)
    env=os.environ.copy(); env.update({"inp_text":str(source),"inp_wav_dir":str(wavs),"exp_name":raw["profile_id"],"opt_dir":str(exp),"bert_pretrained_dir":str(root/"chinese-roberta-wwm-ext-large"),"cnhubert_base_dir":str(root/"chinese-hubert-base"),"pretrained_s2G":str(weight["sovits"]),"s2config_path":str(project/"GPT_SoVITS/configs/s2.json"),"is_half":"True","_CUDA_VISIBLE_DEVICES":"0","i_part":"0","all_parts":"1","bert_path":str(root/"chinese-roberta-wwm-ext-large"),"GPT_SOVITS_G2PW_MODEL":str(root/"G2PWModel"),"GPT_SOVITS_LANG_MODEL":str(root/"fast_langdetect")})
    # Dataset scripts are executed by path and import text/module as top-level
    # packages. Make the registered checkout explicit instead of relying on
    # the caller's current directory or user-site packages.
    python_paths = [str(Path(__file__).with_name("training_shims")), str(project), str(project / "GPT_SoVITS")]
    # Do not combine the inference --target overlay with training.  Apart
    # from making a production inference dependency part of training, a
    # package named ``logging.py`` in an overlay can shadow Python's stdlib.
    # The registered training overlay is intentionally self-contained for
    # training-only extras and the managed music runtime still supplies CUDA
    # Torch underneath it.
    if training_packages:
        python_paths.insert(0, str(training_packages))
    elif packages:
        python_paths.insert(0, str(packages))
    if env.get("PYTHONPATH"): python_paths.append(env["PYTHONPATH"])
    env["PYTHONPATH"] = os.pathsep.join(python_paths)
    env["WECHAT_GPT_SOVITS_PATHS"] = os.pathsep.join(python_paths)
    stages=[("prepare_text","GPT_SoVITS/prepare_datasets/1-get-text.py","2-name2text-0.txt","2-name2text.txt"),("prepare_hubert","GPT_SoVITS/prepare_datasets/2-get-hubert-wav32k.py",None,None),("prepare_semantic","GPT_SoVITS/prepare_datasets/3-get-semantic.py","6-name2semantic-0.tsv","6-name2semantic.tsv")]
    for stage, script, part, final in stages:
        expected = [exp / final] if final else []
        if stage == "prepare_hubert":
            expected = [exp / "4-cnhubert" / (entry["sha256"] + ".wav.pt") for entry in entries]
            expected += [exp / "5-wav32k" / (entry["sha256"] + ".wav") for entry in entries]
        if stage_reusable(exp, stage, entries, expected):
            continue
        run([str(python),script],project,env,job,raw,stage)
        if part:
            if not (exp/part).is_file(): raise ValueError("training_preprocess_output_missing")
            os.replace(exp/part,exp/final)
        if not stage_outputs_valid(stage, entries, expected):
            raise ValueError("training_preprocess_output_missing")
        mark_stage(exp, stage, entries, expected)
    import yaml
    s2=json.loads((project/"GPT_SoVITS/configs/s2.json").read_text(encoding="utf8")); s2["train"].update({"batch_size":int(raw.get("batch_size",1)),"epochs":int(raw.get("sovits_epochs",8)),"pretrained_s2G":str(weight["sovits"]),"pretrained_s2D":str(weight["discriminator"]),"if_save_latest":True,"if_save_every_weights":True,"save_every_epoch":1,"gpu_numbers":"0"}); s2.update({"s2_ckpt_dir":str(exp),"save_weight_dir":str(output),"name":raw["profile_id"],"version":"v2"}); s2["model"]["version"]="v2"; s2["data"]["exp_dir"]=str(exp)
    # Upstream's atomic checkpoint helper assumes this directory already
    # exists; it otherwise completes an epoch then fails while moving G/D.
    (exp / "logs_s2_v2").mkdir(parents=True, exist_ok=True)
    s2_path=job/"s2.json"; s2_path.write_text(json.dumps(s2,ensure_ascii=False),encoding="utf8"); run([str(python),"GPT_SoVITS/s2_train.py","--config",str(s2_path)],project,env,job,raw,"train_sovits")
    s1=yaml.safe_load((project/"GPT_SoVITS/configs/s1longer-v2.yaml").read_text(encoding="utf8")); s1["train"].update({"batch_size":int(raw.get("batch_size",1)),"epochs":int(raw.get("gpt_epochs",8)),"save_every_n_epoch":1,"if_save_every_weights":True,"if_save_latest":True,"half_weights_save_dir":str(output),"exp_name":raw["profile_id"]}); s1.update({"pretrained_s1":str(weight["gpt"]),"train_semantic_path":str(exp/"6-name2semantic.tsv"),"train_phoneme_path":str(exp/"2-name2text.txt"),"output_dir":str(exp/"logs_s1_v2")})
    s1_path=job/"s1.yaml"; s1_path.write_text(yaml.safe_dump(s1,allow_unicode=True,sort_keys=False),encoding="utf8")
    # On Windows the upstream script asks Lightning to create a Gloo DDP
    # worker even when there is only one GPU.  That worker does not inherit
    # this job's no-UI import shim and can leave an orphan after the parent
    # returns.  Use a checked, job-local one-device adaptation; upstream
    # sources and shared runtimes remain untouched.
    upstream_s1 = project / "GPT_SoVITS/s1_train.py"
    adapted_s1 = job / "s1_train_single_device.py"
    source_s1 = upstream_s1.read_text(encoding="utf8")
    source_s1 = source_s1.replace("devices=-1 if torch.cuda.is_available() else 1,", "devices=1,")
    ddp = '''strategy=DDPStrategy(process_group_backend="nccl" if platform.system() != "Windows" else "gloo")
        if torch.cuda.is_available()
        else "auto",'''
    if "devices=1," not in source_s1 or ddp not in source_s1:
        raise ValueError("upstream_s1_single_device_adapter_incompatible")
    source_s1 = source_s1.replace(ddp, 'strategy="auto",')
    # GPT-SoVITS's bucket sampler directly asks torch.distributed for the
    # world size.  For the one-GPU Windows adaptation initialise a local,
    # file-backed group so it reports world-size 1 without spawning a second
    # console/process that loses the signed worker environment.
    store = (job / "s1-gloo.store").as_posix()
    anchor = 'os.environ["USE_LIBUV"] = "0"'
    init = anchor + '\n    import torch.distributed as _enroll_dist\n    if not _enroll_dist.is_initialized():\n        _enroll_dist.init_process_group("gloo", init_method="file:///' + store + '", rank=0, world_size=1)'
    if anchor not in source_s1:
        raise ValueError("upstream_s1_single_device_adapter_incompatible")
    adapted_s1.write_text(source_s1.replace(anchor, init), encoding="utf8")
    env["hz"]="25hz"; run([str(python),str(adapted_s1),"--config_file",str(s1_path)],project,env,job,raw,"train_gpt")
    gpts=sorted(output.glob("*.ckpt"),key=lambda p:p.stat().st_mtime_ns); sovits=sorted(output.glob("*.pth"),key=lambda p:p.stat().st_mtime_ns)
    if not gpts or not sovits: raise ValueError("trained_weights_missing")
    trained={"gpt":job/"gpt.ckpt","sovits":job/"sovits.pth"}; copy(gpts[-1],trained["gpt"]); copy(sovits[-1],trained["sovits"])
    preview_result=preview(job,raw,trained); result=dict(prepared_result); result.update(base(raw,"trained")); result.update({"gpt_sha256":sha(trained["gpt"]),"sovits_sha256":sha(trained["sovits"]),"preview_sha256":preview_result["preview_sha256"],"reference_sha256":preview_result["reference_sha256"],"reference_text":preview_result["reference_text"],"checkpoint":True}); return result


def dispatch(job, raw):
    if raw["action"] == "cancel": write_json(job/CANCEL,{"job_id":raw["job_id"],"revision":raw["revision"],"requested_unix":time.time()}); return base(raw,"cancel_requested")
    if raw["action"] == "status":
        if (job/RUNNING).is_file():
            result=base(raw,"running"); result["progress"]=json.loads((job/RUNNING).read_text(encoding="utf8")); return result
        return json.loads((job/RESULT).read_text(encoding="utf8")) if (job/RESULT).is_file() else base(raw,"not_started")
    if raw["action"] == "prepare": return prepared(job,raw)[1]
    if raw["action"] == "preview": return preview(job,raw)
    return train(job,raw)


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__); parser.add_argument("--manifest",required=True,type=Path); args=parser.parse_args(argv)
    job=raw=None
    try: job,raw=manifest(args.manifest); result=dispatch(job,raw)
    except Exception as error:
        if job is None: raise
        result=base(raw or {"job_id":"unknown","profile_id":"unknown","revision":0},"failed",str(error))
    finally:
        if job: (job/RUNNING).unlink(missing_ok=True)
    write_json(job/RESULT,result); print(json.dumps(result,ensure_ascii=False,separators=(",",":")))
    return 0 if result["state"] in {"prepared","reference_preview_ready","trained","cancel_requested","running","not_started"} else 2


if __name__ == "__main__": raise SystemExit(main())
