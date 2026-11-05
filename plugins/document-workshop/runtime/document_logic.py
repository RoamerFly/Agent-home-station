"""Safe, dependency-free document primitives for the document-workshop plugin.

This module deliberately receives paths only after the MCP bridge has checked a
host-bound artifact context.  It never interprets a model supplied path, URL or
command.  The DOCX writer uses a small OOXML subset so the offline fixture does
not depend on a global Python environment.
"""
from __future__ import annotations

import hashlib
import html
import mimetypes
from pathlib import Path
import re
import shutil
import struct
import zipfile
from xml.etree import ElementTree as ET

MAX_FILE_BYTES = 128 * 1024 * 1024
MAX_TEXT = 250_000
MAX_DOCX_UNCOMPRESSED_BYTES = 64 * 1024 * 1024
MAX_DOCX_MEMBERS = 2_000
WORD_NS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"


class DocumentError(ValueError):
    pass


def file_digest(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for part in iter(lambda: f.read(1024 * 1024), b""):
            h.update(part)
    return h.hexdigest()


def safe_filename(value: str, suffix: str) -> str:
    if not isinstance(value, str):
        value = "document"
    value = re.sub(r"[^0-9A-Za-z._\-\u3400-\u9fff]+", "_", value).strip("._")[:96] or "document"
    return value + suffix if not value.lower().endswith(suffix) else value


def text_content(path: Path) -> str:
    """Extract editable text only; raises a stable code for unknown formats."""
    suffix = path.suffix.lower()
    if suffix == ".docx":
        try:
            with zipfile.ZipFile(path) as zf:
                _verify_docx_container(zf)
                raw = zf.read("word/document.xml")
        except (OSError, KeyError, zipfile.BadZipFile):
            raise DocumentError("document_docx_invalid")
        try:
            root = ET.fromstring(raw)
        except ET.ParseError:
            raise DocumentError("document_docx_invalid")
        paragraphs = []
        for p in root.findall(".//{%s}p" % WORD_NS):
            value = "".join(t.text or "" for t in p.findall(".//{%s}t" % WORD_NS))
            if value:
                paragraphs.append(value)
        return "\n".join(paragraphs)[:MAX_TEXT]
    if suffix == ".pdf":
        raw = path.read_bytes()
        if not raw.startswith(b"%PDF-"):
            raise DocumentError("document_pdf_invalid")
        # PDF text must be extracted by the project-local Poppler runtime.
        # A byte-level fallback would misreport compressed/CJK text as facts.
        return ""
    raise DocumentError("document_text_extraction_unsupported")


def _verify_docx_container(zf: zipfile.ZipFile) -> None:
    entries = zf.infolist()
    if len(entries) > MAX_DOCX_MEMBERS or sum(entry.file_size for entry in entries) > MAX_DOCX_UNCOMPRESSED_BYTES:
        raise DocumentError("document_docx_expansion_limit")
    names = {entry.filename for entry in entries}
    if "word/vbaProject.bin" in names or any(name.lower().endswith("/vbaProject.bin".lower()) for name in names):
        raise DocumentError("document_macro_not_allowed")
    # Relationships with TargetMode=External would turn opening a document into
    # an external fetch. This plugin treats source docs as data, never as a
    # vehicle for external links.
    for name in names:
        if not name.endswith(".rels"):
            continue
        try:
            relationships = ET.fromstring(zf.read(name))
        except (KeyError, ET.ParseError):
            raise DocumentError("document_docx_invalid")
        if any(node.get("TargetMode") == "External" for node in relationships):
            raise DocumentError("document_external_link_not_allowed")


def png_size(path: Path):
    raw = path.read_bytes()[:32]
    if raw[:8] != b"\x89PNG\r\n\x1a\n" or raw[12:16] != b"IHDR":
        return None
    return struct.unpack(">II", raw[16:24])


def inspect(path: Path, mime_type: str = "", pages=None, include_layout=False) -> dict:
    suffix = path.suffix.lower()
    result = {
        "kind": {".docx": "docx", ".pdf": "pdf"}.get(suffix, "image" if mime_type.startswith("image/") else "unknown"),
        "sha256": file_digest(path), "bytes": path.stat().st_size,
        "evidence": [{"source": "artifact", "sha256": file_digest(path), "range": "whole_file"}],
        "untrusted_document_content": True,
    }
    if result["bytes"] > MAX_FILE_BYTES:
        raise DocumentError("document_too_large")
    if suffix == ".docx":
        text = text_content(path)
        result.update({"text": text, "text_characters": len(text), "needs_clarification": False})
        with zipfile.ZipFile(path) as zf:
            _verify_docx_container(zf)
            names = zf.namelist()
        result["embedded_images"] = sum(name.startswith("word/media/") for name in names)
        result["editable"] = True
        return result
    if suffix == ".pdf":
        raw = path.read_bytes()
        if not raw.startswith(b"%PDF-"):
            raise DocumentError("document_pdf_invalid")
        result.update({"text": "", "text_characters": 0, "page_count_estimate": len(re.findall(rb"/Type\s*/Page\b", raw)), "scanned_or_no_extractable_text": True, "needs_clarification": True, "quality_failures": ["document_runtime_inspection_required"]})
        return result
    if mime_type.startswith("image/") or suffix in (".png", ".jpg", ".jpeg", ".webp"):
        result["needs_clarification"] = True
        result["quality_failures"] = ["document_ocr_required"]
        size = png_size(path) if suffix == ".png" else None
        if size:
            result["pixel_width"], result["pixel_height"] = size
        if include_layout:
            result["layout_observation"] = "ocr_not_run_by_plugin_runtime"
        return result
    raise DocumentError("document_format_unsupported")


def _xml(value: str) -> str:
    return html.escape(value, quote=False)


def _paragraph(text: str, style: str = "", font_size_pt: float | None = None) -> str:
    ppr = "<w:pPr><w:pStyle w:val=\"%s\"/></w:pPr>" % style if style else ""
    chunks = text.split("\n") or [""]
    size = "<w:rPr><w:sz w:val=\"%d\"/></w:rPr>" % round(font_size_pt * 2) if font_size_pt is not None else ""
    runs = "".join("<w:r>%s<w:t xml:space=\"preserve\">%s</w:t></w:r>" % (size, _xml(chunk)) + ("<w:r><w:br/></w:r>" if i < len(chunks)-1 else "") for i, chunk in enumerate(chunks))
    return "<w:p>%s%s</w:p>" % (ppr, runs)


def _table(rows) -> str:
    parts = ["<w:tbl><w:tblPr><w:tblBorders><w:top w:val=\"single\" w:sz=\"4\"/><w:left w:val=\"single\" w:sz=\"4\"/><w:bottom w:val=\"single\" w:sz=\"4\"/><w:right w:val=\"single\" w:sz=\"4\"/><w:insideH w:val=\"single\" w:sz=\"4\"/><w:insideV w:val=\"single\" w:sz=\"4\"/></w:tblBorders></w:tblPr>"]
    for row in rows:
        if not isinstance(row, list):
            continue
        cells = "".join("<w:tc><w:tcPr/><w:p><w:r><w:t xml:space=\"preserve\">%s</w:t></w:r></w:p></w:tc>" % _xml(str(cell)) for cell in row)
        parts.append("<w:tr>%s</w:tr>" % cells)
    return "".join(parts) + "</w:tbl>"


def _picture(rel_id: str, index: int, name: str, width: int = 4572000, height: int = 2743200) -> str:
    """A native DrawingML inline picture. It remains a real DOCX image part."""
    return ("<w:p><w:r><w:drawing><wp:inline xmlns:wp=\"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing\" "
            "xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\" xmlns:pic=\"http://schemas.openxmlformats.org/drawingml/2006/picture\">"
            "<wp:extent cx=\"%d\" cy=\"%d\"/><wp:docPr id=\"%d\" name=\"%s\"/><a:graphic><a:graphicData uri=\"http://schemas.openxmlformats.org/drawingml/2006/picture\">"
            "<pic:pic><pic:nvPicPr><pic:cNvPr id=\"%d\" name=\"%s\"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed=\"%s\" xmlns:r=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships\"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>"
            "<pic:spPr><a:xfrm><a:off x=\"0\" y=\"0\"/><a:ext cx=\"%d\" cy=\"%d\"/></a:xfrm><a:prstGeom prst=\"rect\"><a:avLst/></a:prstGeom></pic:spPr>"
            "</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>") % (width, height, index, _xml(name), index, _xml(name), rel_id, width, height)


def reconstruct(layout: dict, output_dir: Path, filename: str = "document.docx", title: str = "", images=None, title_font_size_pt: float | None = None) -> dict:
    if not isinstance(layout, dict):
        raise DocumentError("document_layout_invalid")
    blocks = layout.get("blocks", [])
    if not isinstance(blocks, list) or len(blocks) > 500:
        raise DocumentError("document_layout_invalid")
    body = []
    if title:
        if title_font_size_pt is not None and (type(title_font_size_pt) not in (int, float) or not 8 <= title_font_size_pt <= 48):
            raise DocumentError("document_font_size_invalid")
        body.append(_paragraph(title[:256], "Title", title_font_size_pt))
    for block in blocks:
        if not isinstance(block, dict) or set(block) - {"kind", "text", "rows", "level", "image_index", "caption", "font_size_pt"}:
            raise DocumentError("document_layout_invalid")
        kind = block.get("kind")
        if kind in ("paragraph", "heading"):
            text = block.get("text")
            if not isinstance(text, str) or len(text) > 20_000:
                raise DocumentError("document_layout_invalid")
            level = block.get("level", 1)
            style = "Heading%d" % level if kind == "heading" and type(level) is int and 1 <= level <= 9 else ""
            size = block.get("font_size_pt")
            if size is not None and (type(size) not in (int, float) or not 8 <= size <= 48):
                raise DocumentError("document_font_size_invalid")
            body.append(_paragraph(text, style, size))
        elif kind == "table":
            rows = block.get("rows")
            if not isinstance(rows, list) or len(rows) > 200:
                raise DocumentError("document_layout_invalid")
            body.append(_table(rows))
        elif kind == "image":
            image_index = block.get("image_index")
            if type(image_index) is not int or not isinstance(images, list) or not 0 <= image_index < len(images):
                raise DocumentError("document_image_not_authorized")
            body.append(_picture("rId%d" % (image_index + 1), image_index + 1, images[image_index].name))
            if isinstance(block.get("caption"), str) and block["caption"]:
                body.append(_paragraph(block["caption"][:500]))
        else:
            raise DocumentError("document_layout_invalid")
    if not body:
        raise DocumentError("document_layout_empty")
    output_dir.mkdir(parents=True, exist_ok=True)
    target = output_dir / safe_filename(filename, ".docx")
    document = """<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?><w:document xmlns:w=\"%s\"><w:body>%s<w:sectPr><w:pgSz w:w=\"11906\" w:h=\"16838\"/><w:pgMar w:top=\"1440\" w:right=\"1440\" w:bottom=\"1440\" w:left=\"1440\"/></w:sectPr></w:body></w:document>""" % (WORD_NS, "".join(body))
    types = """<?xml version=\"1.0\" encoding=\"UTF-8\"?><Types xmlns=\"http://schemas.openxmlformats.org/package/2006/content-types\"><Default Extension=\"rels\" ContentType=\"application/vnd.openxmlformats-package.relationships+xml\"/><Default Extension=\"xml\" ContentType=\"application/xml\"/><Default Extension=\"png\" ContentType=\"image/png\"/><Default Extension=\"jpg\" ContentType=\"image/jpeg\"/><Default Extension=\"jpeg\" ContentType=\"image/jpeg\"/><Override PartName=\"/word/document.xml\" ContentType=\"application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml\"/><Override PartName=\"/word/styles.xml\" ContentType=\"application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml\"/></Types>"""
    rels = """<?xml version=\"1.0\" encoding=\"UTF-8\"?><Relationships xmlns=\"%s\"><Relationship Id=\"rId1\" Type=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument\" Target=\"word/document.xml\"/></Relationships>""" % REL_NS
    styles = """<?xml version=\"1.0\" encoding=\"UTF-8\"?><w:styles xmlns:w=\"%s\"><w:style w:type=\"paragraph\" w:default=\"1\" w:styleId=\"Normal\"><w:name w:val=\"Normal\"/></w:style><w:style w:type=\"paragraph\" w:styleId=\"Title\"><w:name w:val=\"Title\"/><w:rPr><w:b/><w:sz w:val=\"36\"/></w:rPr></w:style>%s</w:styles>""" % (WORD_NS, "".join("<w:style w:type=\"paragraph\" w:styleId=\"Heading%d\"><w:name w:val=\"heading %d\"/><w:rPr><w:b/></w:rPr></w:style>" % (i, i) for i in range(1, 10)))
    doc_rels = "<?xml version=\"1.0\" encoding=\"UTF-8\"?><Relationships xmlns=\"%s\">%s</Relationships>" % (REL_NS, "".join("<Relationship Id=\"rId%d\" Type=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships/image\" Target=\"media/image%d%s\"/>" % (i+1, i+1, image.suffix.lower()) for i, image in enumerate(images or [])))
    with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("[Content_Types].xml", types)
        zf.writestr("_rels/.rels", rels)
        zf.writestr("word/document.xml", document)
        zf.writestr("word/styles.xml", styles)
        if images:
            zf.writestr("word/_rels/document.xml.rels", doc_rels)
            for i, image in enumerate(images):
                zf.write(image, "word/media/image%d%s" % (i+1, image.suffix.lower()))
    return {"path": str(target), "mime_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "sha256": file_digest(target), "text": text_content(target)}


def basic_verify(path: Path, expected_text=None) -> dict:
    suffix = path.suffix.lower()
    result = {"sha256": file_digest(path), "bytes": path.stat().st_size, "quality_failures": []}
    if suffix == ".docx":
        text = text_content(path)
        result.update({"format": "docx", "editable": True, "text_characters": len(text)})
    elif suffix == ".pdf":
        raw = path.read_bytes()
        if not raw.startswith(b"%PDF-"):
            raise DocumentError("document_pdf_invalid")
        result.update({"format": "pdf", "page_count_estimate": len(re.findall(rb"/Type\s*/Page\b", raw)), "text_characters": 0})
        if not result["page_count_estimate"]:
            result["quality_failures"].append("document_pdf_page_count_unknown")
    else:
        raise DocumentError("document_format_unsupported")
    for token in expected_text or []:
        if isinstance(token, str) and token and (suffix == ".pdf" or token not in text):
            result["quality_failures"].append("document_expected_text_missing")
            break
    result["ok"] = not result["quality_failures"]
    return result
