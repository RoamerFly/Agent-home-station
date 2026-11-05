"""Pinned document-runtime boundary for the document-workshop plugin.

The plugin process calls this module through the managed Python interpreter.
DOCX parsing stays in ``document_logic.py``; supported layouts are exported by
the current Python process through ReportLab, not an Office application. PDF
inspection/OCR resources live below DOCUMENT_RUNTIME_ROOT; a development-only
Windows backend must be explicitly reported rather than treated as portable.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from typing import Any
from collections import defaultdict

# See main.py: -I is intentional for the plugin sandbox, so only the verified
# directory containing this module is restored for sibling runtime imports.
_RUNTIME_DIR = str(Path(__file__).resolve().parent)
if _RUNTIME_DIR not in sys.path:
    sys.path.insert(0, _RUNTIME_DIR)

# ReportLab/pypdf are deliberately packaged outside plugin code below the
# host-authorized document runtime root.  The Windows launcher permits only
# DOCUMENT_RUNTIME_ROOT, so this cannot be redirected by a model argument or
# ordinary plugin configuration.
_PACKAGED_PYTHON = os.environ.get("DOCUMENT_RUNTIME_ROOT", "")
if _PACKAGED_PYTHON:
    _PACKAGED_PYTHON = str(Path(_PACKAGED_PYTHON).resolve() / "python-packages")
    if _PACKAGED_PYTHON not in sys.path:
        sys.path.insert(0, _PACKAGED_PYTHON)

from native_pdf import NativePDFError, export_docx


SCHEMA_VERSION = 2


def _background_run(argv, **kwargs):
    # AppContainer denies opening the DOS ``nul`` device in the document
    # profile. A private pipe still supplies immediate EOF, does not inherit a
    # console, and lets project-local Tesseract run without widening device
    # permissions.
    kwargs.setdefault("stdin", subprocess.PIPE)
    if os.name == "nt":
        kwargs["creationflags"] = subprocess.CREATE_NO_WINDOW
        startup = subprocess.STARTUPINFO()
        startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
        startup.wShowWindow = subprocess.SW_HIDE
        kwargs["startupinfo"] = startup
    return subprocess.run(argv, **kwargs)


def _truth(value: str | None) -> bool:
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def _runtime_root() -> Path:
    configured = os.environ.get("DOCUMENT_RUNTIME_ROOT")
    if configured:
        return Path(configured).expanduser().resolve()
    return Path(__file__).resolve().parent / ".runtime"


def _inside(value: Path, root: Path) -> Path:
    resolved = value.expanduser().resolve(strict=False)
    allowed = root.expanduser().resolve(strict=False)
    if resolved != allowed and allowed not in resolved.parents:
        raise ValueError("path_outside_authorized_root")
    return resolved


def _existing(candidates: list[Path]) -> Path | None:
    return next((candidate for candidate in candidates if candidate.is_file()), None)


def _find_tool(name: str, root: Path, allow_system: bool) -> tuple[Path | None, str]:
    is_windows = os.name == "nt"
    suffix = ".exe" if is_windows else ""
    local = {
        "pdftotext": [root / "pdf-tools" / ("pdftotext" + suffix),
                       root / "pdf-tools" / "Library" / "bin" / ("pdftotext" + suffix)],
        "pdftoppm": [root / "pdf-tools" / ("pdftoppm" + suffix),
                      root / "pdf-tools" / "Library" / "bin" / ("pdftoppm" + suffix)],
        "pdfinfo": [root / "pdf-tools" / ("pdfinfo" + suffix),
                    root / "pdf-tools" / "Library" / "bin" / ("pdfinfo" + suffix)],
        "tesseract": [root / "tesseract" / ("tesseract" + suffix)],
    }[name]
    found = _existing(local)
    if found:
        return found, "project_local"
    env_name = "DOCUMENT_RUNTIME_" + name.upper()
    env_path = os.environ.get(env_name)
    if env_path and Path(env_path).is_file():
        return Path(env_path).resolve(), "explicit_environment"
    if allow_system:
        system = shutil.which(name + suffix) or shutil.which(name)
        if system:
            return Path(system).resolve(), "system_development_only"
    return None, "missing"


def _chinese_font_status(root: Path) -> dict[str, Any]:
    # ReportLab's TTFont cannot embed CFF/OpenType fonts.  A bundled .otf is
    # therefore not enough evidence that the native exporter can render CJK.
    packaged = list((root / "fonts").glob("*.ttf"))
    usable = [path for path in packaged if _validate_reportlab_font(path)]
    if usable:
        licenses = [p.name for p in (root / "fonts").glob("LICENSE*")] + [p.name for p in (root / "fonts").glob("OFL*")]
        return {"status": "ok" if licenses else "warning", "detail": "project-local embeddable TrueType fonts found", "files": [p.name for p in usable], "licenses": licenses}
    if packaged:
        return {"status": "missing", "detail": "project-local TTF could not be embedded by ReportLab", "files": [p.name for p in packaged]}
    if os.name == "nt":
        windows_fonts = Path(os.environ.get("WINDIR", r"C:\\Windows")) / "Fonts"
        found = [name for name in ("msyh.ttc", "simsun.ttc", "simhei.ttf") if (windows_fonts / name).is_file()]
        if found:
            return {"status": "development_only", "detail": "system Chinese fonts found but are not bundled", "files": found}
    return {"status": "missing", "detail": "add an appropriately licensed Chinese font under runtime/fonts"}


def _chinese_font_path(root: Path) -> Path | None:
    for packaged in (root / "fonts").glob("*.ttf"):
        if _validate_reportlab_font(packaged):
            return packaged
    if os.name == "nt":
        fonts = Path(os.environ.get("WINDIR", r"C:\\Windows")) / "Fonts"
        return next((fonts / name for name in ("msyh.ttc", "simsun.ttc", "simhei.ttf") if (fonts / name).is_file()), None)
    return None


def _validate_reportlab_font(font: Path) -> bool:
    try:
        from reportlab.pdfbase.ttfonts import TTFont
        TTFont("DocumentRuntimeProbe", str(font))
        return True
    except Exception:
        return False


def doctor(request: dict[str, Any]) -> dict[str, Any]:
    root = _runtime_root()
    allow_system = bool(request.get("allow_system", _truth(os.environ.get("DOCUMENT_RUNTIME_ALLOW_SYSTEM"))))
    tesseract, ocr_source = _find_tool("tesseract", root, allow_system)
    chi_sim = bool(tesseract and ((root / "tesseract" / "tessdata" / "chi_sim.traineddata").is_file() or _tesseract_has_chinese(tesseract)))
    font = _chinese_font_status(root)
    checks = [
        {"name": "docx_to_pdf", "status": "ok" if _native_pdf_available() and font["status"] != "missing" else "missing", "detail": "python native export" if _native_pdf_available() else "ReportLab unavailable", "source": "python_runtime"},
        {"name": "pdf_text_extract", "status": "ok" if _native_pdf_inspection_available() else "missing", "detail": "pypdf project runtime" if _native_pdf_inspection_available() else "pypdf unavailable", "source": "python_runtime"},
        {"name": "pdf_raster", "status": "unavailable", "detail": "native raster dependency is intentionally not loaded in the LPAC runtime", "source": "not_provisioned"},
        {"name": "pdf_page_count", "status": "ok" if _native_pdf_inspection_available() else "missing", "detail": "pypdf project runtime" if _native_pdf_inspection_available() else "pypdf unavailable", "source": "python_runtime"},
        {"name": "ocr_chinese", "status": "ok" if chi_sim else "missing", "detail": "chi_sim is available" if chi_sim else "install tesseract plus chi_sim traineddata", "source": ocr_source},
        {"name": "chinese_fonts", **font},
    ]
    available = bool(_native_pdf_available() and _native_pdf_inspection_available() and _chinese_font_path(root))
    paths = {"tesseract": str(tesseract) if tesseract else None}
    return {"schema_version": SCHEMA_VERSION, "available": available, "runtime_id": "document-workshop-runtime", "runtime_root": str(root), "capabilities": ["docx_to_pdf", "pdf_text_extract", "pdf_page_count"] + (["ocr_chinese"] if chi_sim else []), "checks": checks, "executable_paths": paths, "development_fallback": allow_system, "raster_available": False}


def _check(name: str, path: Path | None, source: str) -> dict[str, Any]:
    return {"name": name, "status": "ok" if path else "missing", "detail": str(path) if path else "not provisioned", "source": source}


def _native_pdf_available() -> bool:
    try:
        import reportlab  # noqa: F401
        return True
    except ImportError:
        return False


def _native_pdf_inspection_available() -> bool:
    try:
        import pypdf
        return bool(pypdf)
    except ImportError:
        return False


def _tesseract_has_chinese(executable: Path) -> bool:
    try:
        # Tesseract emits UTF-8 even on Windows installations whose process
        # locale is GBK. Decode explicitly so the health probe itself cannot
        # abort this JSON runtime before reporting availability.
        result = _background_run([str(executable), "--list-langs"], capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=10, check=False)
        return result.returncode == 0 and "chi_sim" in result.stdout.splitlines()
    except (OSError, subprocess.TimeoutExpired):
        return False


def convert(request: dict[str, Any]) -> dict[str, Any]:
    root = _runtime_root()
    input_root = Path(str(request["input_root"]))
    output_root = Path(str(request["output_root"]))
    source = _inside(Path(str(request["input_docx"])), input_root)
    output_dir = _inside(Path(str(request["output_dir"])), output_root)
    if source.suffix.lower() != ".docx" or not source.is_file():
        return {"ok": False, "code": "invalid_input_docx", "detail": "input_docx must be an existing .docx inside input_root"}
    output_dir.mkdir(parents=True, exist_ok=True)
    settings = doctor(request)
    font = _chinese_font_path(root)
    if not _native_pdf_available() or not font:
        return {"ok": False, "code": "document_converter_unavailable", "doctor": settings}
    pdf = output_dir / (Path(str(request.get("filename") or source.stem)).with_suffix(".pdf").name)
    try:
        result = export_docx(source, pdf, font)
    except NativePDFError as exc:
        return {"ok": False, "code": str(exc)}
    result.update({"bytes": pdf.stat().st_size, "sha256": _hash(pdf)})
    return result


def verify(request: dict[str, Any]) -> dict[str, Any]:
    root = _runtime_root()
    output_root = Path(str(request["output_root"]))
    pdf = _inside(Path(str(request["input_pdf"])), output_root)
    if pdf.suffix.lower() != ".pdf" or not pdf.is_file():
        return {"ok": False, "code": "invalid_input_pdf"}
    settings = doctor(request)
    if not _native_pdf_inspection_available():
        return {"ok": False, "code": "pdf_verifier_unavailable", "doctor": settings}
    from pypdf import PdfReader
    try:
        document = PdfReader(str(pdf), strict=False)
        text = "\n".join(page.extract_text() or "" for page in document.pages)
        # Text is evidence from the just-verified PDF, not a host pathname.
        # Bound it before it crosses the MCP frame; callers needing a longer
        # document use normal page-scoped inspection instead of a silent cut.
        return {"ok": bool(document.pages), "pdf": str(pdf), "bytes": pdf.stat().st_size, "sha256": _hash(pdf), "text": text[:65536], "text_truncated": len(text) > 65536, "text_characters": len(text), "raster_pages": [], "page_count": len(document.pages), "raster_available": False}
    except Exception as exc:
        return {"ok": False, "code": "pdf_verification_failed", "detail": str(exc)}


def inspect(request: dict[str, Any]) -> dict[str, Any]:
    """Inspect project-authorized PDF/image data without a system fallback."""
    root = _runtime_root()
    input_root = Path(str(request["input_root"]))
    output_root = Path(str(request["output_root"]))
    source = _inside(Path(str(request["input_path"])), input_root)
    output_dir = _inside(Path(str(request["output_dir"])), output_root)
    if not source.is_file():
        return {"ok": False, "code": "document_inspection_input_missing"}
    output_dir.mkdir(parents=True, exist_ok=True)
    mime = str(request.get("mime_type", "")).lower()
    if source.suffix.lower() == ".pdf" or mime == "application/pdf":
        return _inspect_pdf(root, source, output_dir, request)
    if source.suffix.lower() in {".png", ".jpg", ".jpeg", ".bmp", ".tif", ".tiff", ".webp"} or mime.startswith("image/"):
        return _inspect_image(root, source, output_dir, request)
    return {"ok": False, "code": "document_inspection_unsupported", "detail": "only PDF and image originals are inspected by this runtime"}


def _page_list(raw: Any, page_count: int) -> list[int]:
    if raw is None or raw == []:
        return list(range(1, page_count + 1))
    if not isinstance(raw, list) or not raw or any(type(value) is not int for value in raw):
        raise ValueError("invalid_document_pages")
    values = sorted(set(raw))
    if values[0] < 1 or values[-1] > page_count:
        raise ValueError("document_page_out_of_range")
    return values


def _inspect_pdf(root: Path, source: Path, output_dir: Path, request: dict[str, Any]) -> dict[str, Any]:
    if not _native_pdf_inspection_available():
        return {"ok": False, "code": "document_runtime_unavailable", "doctor": doctor({"allow_system": False})}
    try:
        from pypdf import PdfReader
        document = PdfReader(str(source), strict=False)
        page_count = len(document.pages)
        pages = _page_list(request.get("pages"), page_count)
    except (ValueError, RuntimeError):
        return {"ok": False, "code": "document_page_count_failed"}
    excerpts = []
    for page in pages:
        excerpt = document.pages[page - 1].extract_text() or ""
        excerpts.append(excerpt)
    text = "\n".join(excerpts).strip()
    remaining = [page for page in range(1, page_count + 1) if page not in pages]
    base = {"ok": True, "kind": "pdf", "page_count": page_count, "pages": pages, "text": text, "scanned_or_no_extractable_text": not bool(text), "render_pages": [], "raster_available": False, "quality_failures": [] if text else ["pdf_raster_unavailable", "document_ocr_unavailable"], "continuation": {"complete": not remaining, "remaining_pages": remaining[:200], "next_start_page": remaining[0] if remaining else None}}
    if not text:
        base["ocr"] = {"available": False, "attempted": False, "code": "pdf_raster_unavailable"}
        base["needs_clarification"] = True
    else:
        base["ocr"] = {"available": bool(_find_tool("tesseract", root, False)[0]), "attempted": False}
    return base


def _inspect_image(root: Path, source: Path, output_dir: Path, request: dict[str, Any]) -> dict[str, Any]:
    result = {"ok": True, "kind": "image", "page_count": 1, "pages": [1], "text": "", "scanned_or_no_extractable_text": True, "render_pages": [str(source)], "quality_failures": [], "continuation": {"complete": True, "remaining_pages": [], "next_start_page": None}}
    result["ocr"] = _host_ocr(root, source, request.get("host_ocr"), bool(request.get("include_layout")), output_dir)
    if result["ocr"] is None:
        result["ocr"] = _ocr_images(root, [source], [1], bool(request.get("include_layout")), output_dir)
    result["source_ocr"] = result["ocr"].get("text", "")
    if result["ocr"].get("text"):
        result["text"] = result["ocr"]["text"]
        result["scanned_or_no_extractable_text"] = False
    if _ocr_needs_clarification(result["ocr"]):
        result["quality_failures"].append("ocr_low_confidence")
        result["needs_clarification"] = True
    if result["ocr"].get("code"):
        result["quality_failures"].append(str(result["ocr"]["code"]))
        result["needs_clarification"] = True
    return result


def _host_ocr(root: Path, source: Path, observation: Any, include_layout: bool, output_dir: Path) -> dict[str, Any] | None:
    """Consume only a hash-bound TSV minted by the initial LPAC OCR broker."""
    if observation is None:
        return None
    if not isinstance(observation, dict):
        return {"available": False, "attempted": True, "code": "ocr_observation_invalid"}
    tsv = observation.get("tsv")
    manifest = root / "runtime-manifest.json"
    if (not isinstance(tsv, str) or len(tsv.encode("utf-8")) > 2 * 1024 * 1024 or
            observation.get("input_sha256") != _hash(source) or
            observation.get("runtime_manifest_sha256") != _hash(manifest) or
            observation.get("language") != "chi_sim+eng" or observation.get("psm") != 6):
        return {"available": False, "attempted": True, "code": "ocr_observation_invalid"}
    page = _parse_tsv(tsv.encode("utf-8"), 1)
    page["psm"] = 6
    page["source"] = "host_initial_lpac_tesseract"
    if include_layout:
        page["tables"] = _grid_table(root, source, page, output_dir)
        page["visual_observation"] = {"available": False, "detail": "no vision observer configured; image regions are not inferred from OCR", "table_grid_available": bool(page["tables"]), "table_grid_detail": "grid detection needs the optional pure-Python image adapter when Pillow is unavailable"}
    words = [word for block in page["blocks"] for line in block["lines"] for word in line["words"]]
    return {"available": True, "attempted": True, "engine": "host_initial_lpac_tesseract", "pages": [page], "text": page["text"], "words": words}


def _ocr_needs_clarification(ocr: dict[str, Any]) -> bool:
    words = ocr.get("words", [])
    scores = [word.get("confidence") for word in words if isinstance(word, dict) and isinstance(word.get("confidence"), (int, float))]
    return bool(words) and (sum(scores) / len(scores) < 70 or any(score < 40 for score in scores))


def _bbox(words: list[dict[str, Any]]) -> dict[str, int]:
    return {
        "left": min(word["bbox"]["left"] for word in words),
        "top": min(word["bbox"]["top"] for word in words),
        "right": max(word["bbox"]["right"] for word in words),
        "bottom": max(word["bbox"]["bottom"] for word in words),
    }


def _confidence(words: list[dict[str, Any]]) -> float | None:
    scores = [word["confidence"] for word in words if isinstance(word.get("confidence"), (int, float)) and word["confidence"] >= 0]
    return round(sum(scores) / len(scores), 2) if scores else None


def _parse_tsv(raw: bytes, source_page: int) -> dict[str, Any]:
    """Parse Tesseract's real TSV evidence; never infer text from a fixture."""
    rows = _decode_ocr_text(raw).splitlines()
    groups: dict[tuple[int, int, int, int], list[dict[str, Any]]] = defaultdict(list)
    for row in rows[1:]:
        fields = row.split("\t", 11)
        if len(fields) != 12:
            continue
        try:
            level, block, paragraph, line, word = (int(fields[index]) for index in (0, 2, 3, 4, 5))
            left, top, width, height = (int(fields[index]) for index in (6, 7, 8, 9))
            confidence = float(fields[10])
        except ValueError:
            continue
        text = fields[11].strip()
        if level != 5 or not text or width <= 0 or height <= 0:
            continue
        groups[(block, paragraph, line, word)].append({
            "text": text,
            "confidence": round(confidence, 2),
            "bbox": {"left": left, "top": top, "right": left + width, "bottom": top + height},
        })
    line_groups: dict[tuple[int, int, int], list[dict[str, Any]]] = defaultdict(list)
    for (block, paragraph, line, _), words in groups.items():
        line_groups[(block, paragraph, line)].extend(words)
    blocks: dict[int, list[dict[str, Any]]] = defaultdict(list)
    for (block, paragraph, line), words in line_groups.items():
        words.sort(key=lambda item: (item["bbox"]["left"], item["bbox"]["top"]))
        blocks[block].append({
            "paragraph": paragraph,
            "line": line,
            "text": " ".join(item["text"] for item in words),
            "confidence": _confidence(words),
            "bbox": _bbox(words),
            "words": words,
        })
    normalized_blocks = []
    for block, lines in sorted(blocks.items()):
        lines.sort(key=lambda item: (item["bbox"]["top"], item["bbox"]["left"], item["line"]))
        words = [word for line in lines for word in line["words"]]
        normalized_blocks.append({"block": block, "text": "\n".join(line["text"] for line in lines), "confidence": _confidence(words), "bbox": _bbox(words), "lines": lines})
    return {"source_page": source_page, "text": "\n".join(block["text"] for block in normalized_blocks).strip(), "blocks": normalized_blocks}


def _collapse_positions(values: list[int], distance: int = 3) -> list[int]:
    result: list[list[int]] = []
    for value in values:
        if not result or value - result[-1][-1] > distance:
            result.append([value])
        else:
            result[-1].append(value)
    return [round(sum(group) / len(group)) for group in result]


def _crop_cell_ocr(root: Path, picture: Any, box: tuple[int, int, int, int], source_page: int, output_dir: Path, token: str) -> list[dict[str, Any]]:
    """Read an empty ruled cell independently and return only measured text."""
    executable, _ = _find_tool("tesseract", root, False)
    model = root / "tesseract" / "tessdata" / "chi_sim.traineddata"
    if not executable or not model.is_file():
        return []
    left, top, right, bottom = box
    # Keep rules out of the crop: they are frequently read as I, | or Chinese
    # strokes. The source bbox remains the full cell, while word bboxes below
    # are translated back into original-image coordinates.
    inset = 5
    crop = picture.crop((left + inset, top + inset, max(left + inset + 1, right - inset), max(top + inset + 1, bottom - inset)))
    path = output_dir / f".ocr-cell-{token}.png"
    try:
        crop.save(path)
        candidates = []
        for psm in (7, 6, 11):
            result = _background_run([str(executable), str(path), "stdout", "-l", "chi_sim+eng", "--oem", "1", "--psm", str(psm), "tsv"], stdin=subprocess.PIPE, capture_output=True, text=False, timeout=30, check=False, env={**os.environ, "TESSDATA_PREFIX": str(model.parent)}, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
            if result.returncode == 0:
                candidates.append(_parse_tsv(result.stdout or b"", source_page))
        if not candidates:
            return []
        selected = max(candidates, key=lambda page: sum(len(line["words"]) for block in page["blocks"] for line in block["lines"]))
        words = [word for block in selected["blocks"] for line in block["lines"] for word in line["words"]]
        for word in words:
            word["bbox"] = {key: word["bbox"][key] + (left + inset if key in ("left", "right") else top + inset) for key in ("left", "top", "right", "bottom")}
        return words
    finally:
        path.unlink(missing_ok=True)


def _grid_table(root: Path, image: Path, page: dict[str, Any], output_dir: Path) -> list[dict[str, Any]]:
    """Offer rows only when dark horizontal/vertical grid evidence is present."""
    try:
        from PIL import Image
        picture = Image.open(image).convert("L")
    except Exception:
        return []
    width, height = picture.size
    if width < 32 or height < 32:
        return []
    pixels = picture.load()
    # A scanned/anti-aliased form can use dark blue or grey rules rather than
    # pure black. The threshold deliberately remains well below white paper.
    horizontal = _collapse_positions([y for y in range(height) if sum(pixels[x, y] < 180 for x in range(width)) >= width * 0.55])
    if len(horizontal) < 3:
        return []
    # A table often occupies only a region of the page.  Its vertical borders
    # must be measured inside the horizontal rule span, not against the full
    # source image height (which would miss a lower-page table).
    top, bottom = horizontal[0], horizontal[-1]
    table_height = max(bottom - top, 1)
    vertical = _collapse_positions([x for x in range(width) if sum(pixels[x, y] < 180 for y in range(top, bottom + 1)) >= table_height * 0.55])
    if len(vertical) < 3:
        return []
    bounds = {"left": vertical[0], "top": horizontal[0], "right": vertical[-1], "bottom": horizontal[-1]}
    words = [word for block in page["blocks"] for line in block["lines"] for word in line["words"]]
    rows, evidence_rows = [], []
    for row_index, (top, bottom) in enumerate(zip(horizontal, horizontal[1:])):
        values, evidence = [], []
        for column_index, (left, right) in enumerate(zip(vertical, vertical[1:])):
            selected = [word for word in words if left <= (word["bbox"]["left"] + word["bbox"]["right"]) / 2 < right and top <= (word["bbox"]["top"] + word["bbox"]["bottom"]) / 2 < bottom]
            source = "whole_page_tsv"
            if not selected:
                selected = _crop_cell_ocr(root, picture, (left, top, right, bottom), page["source_page"], output_dir, f"{page['source_page']}-{row_index}-{column_index}")
                source = "cell_crop_tsv"
            values.append(" ".join(word["text"] for word in selected))
            evidence.append({"source": source, "bbox": {"left": left, "top": top, "right": right, "bottom": bottom}, "text": values[-1], "confidence": _confidence(selected)})
        if any(values):
            rows.append({"row": row_index, "cells": values})
            evidence_rows.append({"row": row_index, "cells": evidence})
    return [{"basis": "detected_grid_and_word_positions", "bbox": bounds, "columns": len(vertical) - 1, "rows": rows, "cell_evidence": evidence_rows, "cells_unreadable": not bool(rows)}]


def _ocr_images(root: Path, images: list[Path], source_pages: list[int], include_layout: bool, output_dir: Path) -> dict[str, Any]:
    executable, _ = _find_tool("tesseract", root, False)
    model = root / "tesseract" / "tessdata" / "chi_sim.traineddata"
    if not executable or not model.is_file():
        return {"available": False, "attempted": False, "code": "ocr_chinese_unavailable"}
    page_results = []
    for index, image in enumerate(images):
        # A single rendered document page or a chat attachment commonly holds
        # several short text blocks; sparse-page auto layout drops these more
        # often than a uniform block pass.  Callers retain the original image
        # and must still treat OCR as an observation, not authoritative data.
        # Tesseract on Windows can inherit a GBK console code page even when
        # the invoking managed Python process is UTF-8.  Capture bytes and
        # select a lossless Chinese decode rather than turning a healthy OCR
        # result into replacement characters in the host-visible evidence.
        candidates = []
        for psm in (6, 11):
            try:
                result = _background_run([str(executable), str(image), "stdout", "-l", "chi_sim+eng", "--oem", "1", "--psm", str(psm), "tsv"], stdin=subprocess.PIPE, capture_output=True, text=False, timeout=60, check=False, env={**os.environ, "TESSDATA_PREFIX": str(model.parent)}, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
            except OSError as exc:
                # A sandbox admission failure is not malformed input or a
                # silent empty OCR result. The host can surface this exact
                # dependency boundary and decide whether its narrow process
                # broker allows project-local Tesseract.
                return {"available": False, "attempted": True, "code": "ocr_process_unavailable", "detail": str(exc)}
            if result.returncode == 0:
                candidate = _parse_tsv(result.stdout or b"", source_pages[index] if index < len(source_pages) else index + 1)
                candidate["psm"] = psm
                candidates.append(candidate)
        if candidates:
            # Prefer the pass that found more actual words. This gives boxed
            # tables a sparse-text fallback without substituting invented text.
            page = max(candidates, key=lambda item: sum(len(line["words"]) for block in item["blocks"] for line in block["lines"]))
            if include_layout:
                page["tables"] = _grid_table(root, image, page, output_dir)
                page["visual_observation"] = {
                    "available": False,
                    "detail": "no vision observer configured; image regions are not inferred from OCR",
                    "table_grid_available": bool(page["tables"]),
                    "table_grid_detail": "grid detection needs the optional pure-Python image adapter when Pillow is unavailable",
                }
            page_results.append(page)
    text = "\n".join(page["text"] for page in page_results).strip()
    return {"available": True, "attempted": True, "engine": "tesseract_chi_sim", "pages": page_results, "text": text, "words": [word for page in page_results for block in page["blocks"] for line in block["lines"] for word in line["words"]]}


def _decode_ocr_text(raw: bytes) -> str:
    """Decode Tesseract output without depending on the Windows console ACP."""
    # Valid UTF-8 is unambiguous.  Do not score a valid UTF-8 stream against
    # GB18030: most UTF-8 Chinese byte sequences are also decodable as GB18030
    # but produce convincing-looking nonsense.
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        return raw.decode("gb18030", errors="replace")


def _hash(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def dispatch(request: dict) -> dict:
    try:
        action = request.get("action")
        if action == "doctor":
            result = doctor(request)
        elif action == "convert":
            result = convert(request)
        elif action == "verify":
            result = verify(request)
        elif action == "inspect":
            result = inspect(request)
        else:
            result = {"ok": False, "code": "unsupported_document_runtime_action"}
    except (KeyError, TypeError, ValueError) as exc:
        result = {"ok": False, "code": "invalid_document_runtime_request", "detail": str(exc)}
    except subprocess.TimeoutExpired:
        result = {"ok": False, "code": "document_runtime_timeout"}
    except Exception as exc:  # Never hide a runtime failure from the host.
        result = {"ok": False, "code": "document_runtime_internal_error", "detail": str(exc)}
    return result


def main() -> int:
    result = dispatch(json.load(sys.stdin))
    sys.stdout.write(json.dumps(result, ensure_ascii=False) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
