"""Native Python export for the constrained document-workshop DOCX subset.

It intentionally supports the editable DOCX produced by ``document_logic``:
paragraphs, headings, simple tables and embedded raster images.  Existing
complex DOCX layouts are rejected with a precise capability boundary instead
of being silently reflowed or sent to Office/LibreOffice.
"""
from __future__ import annotations

from pathlib import Path
import re
import zipfile
from xml.etree import ElementTree as ET

from document_logic import DocumentError, _docx_block_for_table, _verify_docx_container
from reportlab_image import EmbeddedImageError, intrinsic_size, reportlab_reader

WORD = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
DRAWING = "http://schemas.openxmlformats.org/drawingml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"


class NativePDFError(ValueError):
    pass


def _require_reportlab():
    try:
        from reportlab.lib import colors
        from reportlab.lib.enums import TA_CENTER, TA_LEFT
        from reportlab.lib.pagesizes import A4
        from reportlab.lib.styles import ParagraphStyle
        from reportlab.lib.units import mm
        from reportlab.platypus import Flowable, KeepTogether, Paragraph, SimpleDocTemplate, Spacer, Table, TableStyle, PageBreak
        from reportlab.pdfbase import pdfmetrics
        from reportlab.pdfbase.ttfonts import TTFont
        from reportlab.lib.utils import ImageReader
        return locals()
    except ModuleNotFoundError as exc:
        missing = str(exc.name or "").split(".", 1)[0]
        if missing == "reportlab":
            raise NativePDFError("native_pdf_reportlab_missing") from exc
        if missing == "PIL":
            raise NativePDFError("native_pdf_pillow_missing") from exc
        raise NativePDFError("native_pdf_dependency_import_error") from exc
    except ImportError as exc:
        detail = str(exc).lower()
        if "dll load failed" in detail:
            if "python313" in detail:
                raise NativePDFError("native_pdf_python_dll_unavailable") from exc
            if "vcruntime" in detail:
                raise NativePDFError("native_pdf_vcruntime_unavailable") from exc
            if "imaging" in detail or "pillow" in detail:
                raise NativePDFError("native_pdf_pillow_dll_unavailable") from exc
            raise NativePDFError("native_pdf_dependency_dll_load_failed") from exc
        if "no module named" in detail:
            raise NativePDFError("native_pdf_dependency_module_missing") from exc
        # Keep provider exceptions and filesystem details private, but retain
        # the actionable distinction between an unavailable packaged module
        # and a runtime import/load failure.
        raise NativePDFError("native_pdf_dependency_import_error") from exc
    except Exception as exc:
        raise NativePDFError("native_pdf_dependency_unavailable") from exc


def _font(api, font_path: Path):
    if not font_path.is_file():
        raise NativePDFError("native_pdf_chinese_font_unavailable")
    try:
        api["pdfmetrics"].registerFont(api["TTFont"]("DocumentChinese", str(font_path), subfontIndex=0))
    except Exception as exc:
        raise NativePDFError("native_pdf_chinese_font_unavailable") from exc
    return "DocumentChinese"


def _text(node):
    return "".join(value.text or "" for value in node.findall(".//{%s}t" % WORD))


def _markup(text):
    escaped = text.replace("&", "&amp;").replace("<", "&lt;")
    # Paragraph's HTML parser otherwise collapses the actual Word form blanks.
    escaped = re.sub(r" {2,}", lambda match: "&#160;" * len(match.group()), escaped)
    return escaped.replace("\n", "<br/>")


def _font_size(node):
    value = node.find(".//{%s}rPr/{%s}sz" % (WORD, WORD))
    try:
        points = int(value.get("{%s}val" % WORD)) / 2 if value is not None else None
        return points if points is not None and 8 <= points <= 48 else None
    except (TypeError, ValueError):
        return None


def _relations(archive):
    try:
        root = ET.fromstring(archive.read("word/_rels/document.xml.rels"))
    except KeyError:
        return {}
    except ET.ParseError as exc:
        raise NativePDFError("native_pdf_docx_invalid") from exc
    return {item.get("Id"): item.get("Target") for item in root if item.get("Id") and item.get("Target")}


def _paragraph_format(node):
    p = node.find('{%s}pPr' % WORD)
    result = {}
    face = node.find('.//{%s}rFonts'%WORD)
    if face is not None: result['font_family'] = face.get('{%s}eastAsia'%WORD) or face.get('{%s}ascii'%WORD)
    if p is None: return result
    align = p.find('{%s}jc' % WORD)
    if align is not None: result['alignment'] = {'left':0,'center':1,'right':2,'both':4}.get(align.get('{%s}val'%WORD),0)
    for tag, pairs in [('ind', [('firstLine','firstLineIndent'),('left','leftIndent'),('right','rightIndent')]), ('spacing',[('before','spaceBefore'),('after','spaceAfter'),('line','leading')])]:
        item = p.find('{%s}%s'%(WORD,tag))
        if item is not None:
            for attr,key in pairs:
                raw = item.get('{%s}%s'%(WORD,attr))
                if raw is not None:
                    if attr=='line' and item.get('{%s}lineRule'%WORD) not in ('exact','atLeast'):continue
                    result[key] = float(raw)/20
    if p.find('{%s}pageBreakBefore'%WORD) is not None:result['page_break']=True
    return result

def _page_geometry(docx):
    with zipfile.ZipFile(docx) as z: root=ET.fromstring(z.read('word/document.xml'))
    section=root.find('.//{%s}sectPr'%WORD)
    result={}
    if section is None:return result
    for tag,pairs in [('pgSz',[('w','width'),('h','height')]),('pgMar',[('top','topMargin'),('bottom','bottomMargin'),('left','leftMargin'),('right','rightMargin')])]:
        item=section.find('{%s}%s'%(WORD,tag))
        if item is not None:
            for attr,key in pairs:
                raw=item.get('{%s}%s'%(WORD,attr))
                if raw is not None:result[key]=float(raw)/20
    return result


def _blocks(docx: Path):
    try:
        with zipfile.ZipFile(docx) as archive:
            _verify_docx_container(archive)
            names = set(archive.namelist())
            if any(name.startswith(("word/header", "word/footer", "word/comments", "word/footnotes", "word/endnotes")) for name in names):
                raise NativePDFError("native_pdf_complex_docx_unsupported")
            root = ET.fromstring(archive.read("word/document.xml"))
            if root.find(".//{%s}txbxContent" % WORD) is not None:
                raise NativePDFError("native_pdf_complex_docx_unsupported")
            relations = _relations(archive)
            images = {name: archive.read(name) for name in archive.namelist() if name.startswith("word/media/")}
    except (OSError, KeyError, zipfile.BadZipFile, ET.ParseError) as exc:
        raise NativePDFError("native_pdf_docx_invalid") from exc
    body = root.find("{%s}body" % WORD)
    if body is None:
        raise NativePDFError("native_pdf_docx_invalid")
    items = []
    for child in body:
        tag = child.tag.rsplit("}", 1)[-1]
        if tag == "p":
            style = child.find("{%s}pPr/{%s}pStyle" % (WORD, WORD))
            drawing = child.find(".//{%s}blip" % DRAWING)
            if drawing is not None:
                target = relations.get(drawing.get("{%s}embed" % REL))
                if not target or not target.startswith("media/") or "word/" + target not in images:
                    raise NativePDFError("native_pdf_image_relation_unsupported")
                items.append(("image", target, images["word/" + target]))
            else:
                items.append(("heading" if style is not None and str(style.get("{%s}val" % WORD, "")).startswith("Heading") else "title" if style is not None and style.get("{%s}val" % WORD) == "Title" else "paragraph", _text(child), _font_size(child), _paragraph_format(child)))
        elif tag == "tbl":
            try:
                table = _docx_block_for_table(child)
            except DocumentError as exc:
                raise NativePDFError(str(exc)) from exc
            if not table or len(table["rows"]) > 200 or max(map(len, table["rows"])) > 16:
                raise NativePDFError("native_pdf_table_unsupported")
            items.append(("table", table["rows"], table.get("merges", []), table.get("borderless", False)))
        elif tag != "sectPr":
            raise NativePDFError("native_pdf_layout_unsupported")
    return items


def _image_flowable(api, raw: bytes, target: str):
    """Make a Platypus image without Pillow or any external image process."""
    try:
        reader = reportlab_reader(raw, target, api["ImageReader"])
        width, height = intrinsic_size(reader)
    except EmbeddedImageError as exc:
        raise NativePDFError(str(exc)) from exc
    max_width, max_height = 160 * api["mm"], 110 * api["mm"]
    scale = min(1.0, max_width / width, max_height / height)

    class EmbeddedImageFlowable(api["Flowable"]):
        def __init__(self):
            super().__init__()
            self.drawWidth, self.drawHeight = width * scale, height * scale
            self.hAlign = "LEFT"

        def wrap(self, available_width, available_height):
            return self.drawWidth, self.drawHeight

        def draw(self):
            self.canv.drawImage(reader, 0, 0, width=self.drawWidth, height=self.drawHeight, mask="auto")

    return EmbeddedImageFlowable()


def export_docx(docx: Path, pdf: Path, chinese_font: Path) -> dict:
    api = _require_reportlab()
    font = _font(api, chinese_font)
    blocks = _blocks(docx)
    pdf.parent.mkdir(parents=True, exist_ok=True)
    geometry = _page_geometry(docx)
    size = (geometry.pop('width',api['A4'][0]),geometry.pop('height',api['A4'][1]))
    doc = api["SimpleDocTemplate"](str(pdf), pagesize=size, **geometry)
    body = []
    styles = {
        "paragraph": api["ParagraphStyle"]("Body", fontName=font, fontSize=10.5, leading=17, alignment=api["TA_LEFT"], wordWrap="CJK"),
        "heading": api["ParagraphStyle"]("Heading", fontName=font, fontSize=15, leading=22, spaceBefore=8, spaceAfter=6, wordWrap="CJK"),
        "title": api["ParagraphStyle"]("Title", fontName=font, fontSize=18, leading=28, alignment=api["TA_LEFT"], spaceAfter=14, wordWrap="CJK"),
    }
    for kind, *values in blocks:
        if kind in styles:
            text, requested_size, formatting = values
            style = styles[kind]
            if requested_size is not None:
                style = api["ParagraphStyle"](kind + "Custom", parent=style, fontSize=requested_size, leading=max(requested_size * 1.45, style.leading))
            formatting = dict(formatting)
            family = formatting.pop('font_family', None)
            if family in ('宋体', 'SimSun'):
                local_font = chinese_font.parent / 'SimSun-local.ttf'
                if local_font.is_file():
                    api['pdfmetrics'].registerFont(api['TTFont']('DocumentSimSun', str(local_font), subfontIndex=0))
                    formatting['fontName'] = 'DocumentSimSun'
            if formatting.pop('page_break',False):body.append(api['PageBreak']())
            if formatting: style = api['ParagraphStyle'](kind+'Layout',parent=style,**formatting)
            body.extend([api["Paragraph"](_markup(text), style), api["Spacer"](1, 0 if formatting else 4)])
        elif kind == "table":
            rows, merges, borderless = values
            data = [[api["Paragraph"](_markup(str(cell)), styles["paragraph"]) for cell in row] for row in rows]
            repeat = 0 if any(m["row"] == 0 and m["row_span"] > 1 for m in merges) else 1
            table = api["Table"](data, repeatRows=repeat, hAlign="LEFT")
            spans = [("SPAN", (m["column"], m["row"]),
                      (m["column"] + m["col_span"] - 1, m["row"] + m["row_span"] - 1)) for m in merges]
            decoration = [] if borderless else [
                ("GRID", (0,0), (-1,-1), 0.5, api["colors"].HexColor("#B8B8B8")),
                ("BACKGROUND", (0,0), (-1,0), api["colors"].HexColor("#EAF0FA")),
            ]
            table.setStyle(api["TableStyle"](decoration + [
                ("VALIGN", (0,0), (-1,-1), "MIDDLE"),
                ("LEFTPADDING", (0,0), (-1,-1), 7), ("RIGHTPADDING", (0,0), (-1,-1), 7),
                ("TOPPADDING", (0,0), (-1,-1), 6), ("BOTTOMPADDING", (0,0), (-1,-1), 6),
            ] + spans))
            body.extend([api["KeepTogether"](table), api["Spacer"](1, 8)])
        elif kind == "image":
            target, raw = values
            image = _image_flowable(api, raw, target)
            body.extend([image, api["Spacer"](1, 6)])
        else:
            raise NativePDFError("native_pdf_layout_unsupported")
    if not body:
        raise NativePDFError("native_pdf_layout_empty")
    try:
        doc.build(body)
    except Exception as exc:
        raise NativePDFError("native_pdf_export_failed") from exc
    if not pdf.is_file() or pdf.stat().st_size < 128:
        raise NativePDFError("native_pdf_export_failed")
    return {"ok": True, "output_pdf": str(pdf), "backend": "python-reportlab", "supported_layout": "paragraphs_headings_tables_images"}
