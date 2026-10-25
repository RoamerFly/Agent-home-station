"""Dump the exact Go SaeInfo layout using the existing offline metadata reader."""
import contextlib, io, pathlib, runpy

ROOT=pathlib.Path(__file__).resolve().parents[2]
capture=io.StringIO()
with contextlib.redirect_stdout(capture):
    helpers=runpy.run_path(str(ROOT/'research/scripts/recover_device_v8_structs.py'))
capture=io.StringIO()
with contextlib.redirect_stdout(capture):helpers['dump'](0xf08400)
(ROOT/'research/evidence/sae08-struct-layout.txt').write_text(capture.getvalue(),encoding='utf8')
print(capture.getvalue(),end='')
