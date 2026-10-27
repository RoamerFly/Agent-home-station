"""Project workers must not allocate a console when spawning CLI children."""
import subprocess


def no_console_options():
    return {'creationflags': subprocess.CREATE_NO_WINDOW} if hasattr(subprocess, 'CREATE_NO_WINDOW') else {}
