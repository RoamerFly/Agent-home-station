"""No-UI compatibility shim for upstream dataset scripts.

GPT-SoVITS imports Gradio only to surface WebUI warnings from its file-path
helpers.  Enrollment executes no WebUI and handles all failures through the
signed result protocol, so loading a browser/UI framework in its training
worker is both unnecessary and unsafe.  This shim intentionally provides only
the two notification names used by that helper.
"""


def Warning(*_args, **_kwargs):
    return None


def Error(*_args, **_kwargs):
    return None
