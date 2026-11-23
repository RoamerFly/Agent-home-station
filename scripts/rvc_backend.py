"""Compatibility for the legacy 231006 and current official RVC APIs."""
from pathlib import Path


def layout(project):
    root = Path(project)
    if (root / 'infer/vc/modules.py').is_file():
        return 'official-2026'
    if (root / 'infer/modules/vc/modules.py').is_file():
        return 'legacy-231006'
    raise ValueError('rvc_project_layout_not_supported')


def convert(vc, source, semitones, index, index_rate, protect, backend, rms_mix_rate=.5, filter_radius=None):
    if filter_radius is None:
        filter_radius = 0 if backend == 'official-2026' else 3
    if backend == 'official-2026':
        return vc.vc_single(0, source, semitones, 'rmvpe', index, index_rate, filter_radius, rms_mix_rate, protect)
    if backend == 'legacy-231006':
        return vc.vc_single(0, source, semitones, None, 'rmvpe', index, None, index_rate, filter_radius, 0, rms_mix_rate, protect)
    raise ValueError('rvc_project_layout_not_supported')
