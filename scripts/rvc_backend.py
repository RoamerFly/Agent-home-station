"""Compatibility for the legacy 231006 and current official RVC APIs."""
from pathlib import Path


def layout(project):
    root = Path(project)
    if (root / 'infer/vc/modules.py').is_file():
        return 'official-2026'
    if (root / 'infer/modules/vc/modules.py').is_file():
        return 'legacy-231006'
    raise ValueError('rvc_project_layout_not_supported')


def convert(vc, source, semitones, index, index_rate, protect, backend):
    if backend == 'official-2026':
        return vc.vc_single(0, source, semitones, 'rmvpe', index, index_rate, 0, .5, protect)
    if backend == 'legacy-231006':
        return vc.vc_single(0, source, semitones, None, 'rmvpe', index, None, index_rate, 3, 0, .5, protect)
    raise ValueError('rvc_project_layout_not_supported')
