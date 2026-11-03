"""Offline smoke test for the project-local document runtime.

Creates a synthetic Chinese DOCX with typography and a table, then invokes the
plugin runtime's doctor/convert/verify JSON interface.  It neither needs nor
touches a WeChat account, user file, model, or network service.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import zipfile


def write_docx(path: Path) -> None:
    entries = {
        "[Content_Types].xml": """<?xml version='1.0' encoding='UTF-8'?><Types xmlns='http://schemas.openxmlformats.org/package/2006/content-types'><Default Extension='rels' ContentType='application/vnd.openxmlformats-package.relationships+xml'/><Default Extension='xml' ContentType='application/xml'/><Override PartName='/word/document.xml' ContentType='application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml'/></Types>""",
        "_rels/.rels": """<?xml version='1.0' encoding='UTF-8'?><Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'><Relationship Id='rId1' Type='http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument' Target='word/document.xml'/></Relationships>""",
        "word/document.xml": """<?xml version='1.0' encoding='UTF-8'?><w:document xmlns:w='http://schemas.openxmlformats.org/wordprocessingml/2006/main'><w:body><w:p><w:pPr><w:jc w:val='center'/></w:pPr><w:r><w:rPr><w:rFonts w:eastAsia='Microsoft YaHei'/><w:b/><w:sz w:val='32'/></w:rPr><w:t>中文文档运行时验收</w:t></w:r></w:p><w:p><w:r><w:rPr><w:rFonts w:eastAsia='Microsoft YaHei'/><w:sz w:val='22'/></w:rPr><w:t>本页用于验证中文排版、段落和表格在 DOCX 转 PDF 后保持可读。</w:t></w:r></w:p><w:tbl><w:tblPr><w:tblBorders><w:top w:val='single' w:sz='8'/><w:left w:val='single' w:sz='8'/><w:bottom w:val='single' w:sz='8'/><w:right w:val='single' w:sz='8'/><w:insideH w:val='single' w:sz='4'/><w:insideV w:val='single' w:sz='4'/></w:tblBorders></w:tblPr><w:tblGrid><w:gridCol w:w='2600'/><w:gridCol w:w='6400'/></w:tblGrid><w:tr><w:tc><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>项目</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>验收结论</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>中文字体</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>标题、正文与表格均可提取</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>PDF 页面</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>首页应成功栅格化为 PNG</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:sectPr><w:pgSz w:w='11906' w:h='16838'/><w:pgMar w:top='1440' w:right='1440' w:bottom='1440' w:left='1440'/></w:sectPr></w:body></w:document>""",
    }
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, value in entries.items():
            archive.writestr(name, value)


def call(python: Path, runtime: Path, payload: dict, environment: dict[str, str]) -> dict:
    # The managed runtime emits UTF-8 JSON irrespective of the Windows console
    # code page. Without an explicit decoder, a successful Chinese result can
    # make this smoke harness fail while reading stdout.
    result = subprocess.run([str(python), str(runtime)], input=json.dumps(payload, ensure_ascii=False), text=True, encoding="utf-8", errors="replace", capture_output=True, env=environment, timeout=150, check=False, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
    if result.returncode != 0:
        raise RuntimeError(result.stderr)
    return json.loads(result.stdout)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--python", required=True, type=Path)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--runtime-root", required=True, type=Path)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()
    output = args.output_dir.resolve() if args.output_dir else Path(tempfile.mkdtemp(prefix="document-runtime-smoke-"))
    input_root, output_root = output / "input", output / "output"
    input_root.mkdir(parents=True, exist_ok=True)
    output_root.mkdir()
    docx = input_root / "synthetic-layout.docx"
    write_docx(docx)
    env = dict(**__import__("os").environ, DOCUMENT_RUNTIME_ROOT=str(args.runtime_root.resolve()))
    proof = call(args.python, args.runtime, {"action": "doctor"}, env)
    converted = call(args.python, args.runtime, {"action": "convert", "input_root": str(input_root), "output_root": str(output_root), "input_docx": str(docx), "output_dir": str(output_root)}, env)
    if not converted.get("ok"):
        raise RuntimeError(json.dumps(converted, ensure_ascii=False))
    verified = call(args.python, args.runtime, {"action": "verify", "output_root": str(output_root), "input_pdf": converted["output_pdf"]}, env)
    text = str(verified.get("text", ""))
    required = ("中文文档运行时验收", "中文字体", "验收结论")
    if not verified.get("ok") or not all(value in text for value in required):
        raise RuntimeError("PDF verification did not preserve the required Chinese text")
    print(json.dumps({"ok": True, "output": str(output), "doctor": proof, "convert": converted, "verify": verified}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
