"""Resume verified local RVC computation under its original job identity.

This module never sends media, changes a recipe or starts YuE generation.
The bridge holds its job lock while claiming a resume; active/completed jobs
return their existing status rather than enqueuing another conversion.
"""
import base64
import hashlib
import os
from pathlib import Path
import re
import stat
import tempfile
import time

import rvc_cover


def source_path(state, jid):
    if not isinstance(jid, str) or not re.fullmatch(r'[a-f0-9]{32}', jid):
        raise ValueError('rvc_recovery_invalid_id')
    state = Path(state)
    if state.is_symlink() or state.resolve() != state.absolute():
        raise ValueError('rvc_recovery_state_link')
    return state / (jid + '-source.audio')


def read_reference(path, expected):
    if path.is_symlink():
        raise ValueError('rvc_recovery_source_link')
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0))
        with os.fdopen(fd, 'rb') as stream:
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                raise ValueError('rvc_recovery_source_not_regular')
            data = stream.read(25*1024*1024+1)
    except FileNotFoundError:
        raise ValueError('rvc_recovery_source_missing') from None
    if not 0 < len(data) <= 25*1024*1024 or hashlib.sha256(data).hexdigest() != expected:
        raise ValueError('rvc_recovery_source_changed')
    return data


def save_jobs(path, jobs):
    import json
    path = Path(path)
    fd, pending = tempfile.mkstemp(dir=path.parent, prefix='music-jobs-')
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(jobs, stream, ensure_ascii=False)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(pending, path)
    finally:
        Path(pending).unlink(missing_ok=True)


def lock_state(state):
    """One broker owns this account journal, including startup transitions."""
    path = Path(state)/'.bridge.lock'
    if path.is_symlink(): raise ValueError('music_state_lock_link')
    stream = path.open('a+b')
    try:
        if os.name == 'nt':
            import msvcrt
            stream.seek(0); stream.write(b'0'); stream.flush(); stream.seek(0)
            msvcrt.locking(stream.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BaseException:
        stream.close()
        raise
    return stream


def persist_reference(state, jid, body):
    details = rvc_cover.recipe(body)
    path = source_path(state, jid)
    if path.exists() or path.is_symlink():
        read_reference(path, details['reference_sha256'])
        return path
    data = base64.b64decode(body['reference_base64'], validate=True)
    fd, pending = tempfile.mkstemp(dir=path.parent, prefix='rvc-source-')
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        # Publish complete data without overwriting another source at this ID.
        try:
            os.link(pending, path)
        except FileExistsError:
            read_reference(path, details['reference_sha256'])
    finally:
        Path(pending).unlink(missing_ok=True)
    return path


def recover_body(state, jid, job):
    if job.get('engine') != 'rvc':
        raise ValueError('rvc_recovery_engine_required')
    details = job.get('recipe', {})
    data = read_reference(source_path(state, jid), details.get('reference_sha256'))
    body = {key: details.get(key) for key in ('title', 'reference_start_seconds',
            'reference_seconds', 'reference_source_sha256')}
    body.update(reference_base64=base64.b64encode(data).decode(), reference_pretrimmed=True)
    if details.get('voice_profile_id'):
        body['voice_profile_id']=details['voice_profile_id']
    # Compare the exact stored recipe, including duration/provenance. A valid
    # source alone does not authorize changing this job to a different take.
    if rvc_cover.recipe(body) != details:
        raise ValueError('rvc_recovery_recipe_changed')
    return body


def resume(jobs, jid, state, config, profile_reader, save, submit):
    """Caller holds bridge lock. submit schedules its existing RVC runner."""
    source_path(state, jid)
    job = jobs.get(jid)
    if job is None:
        raise ValueError('rvc_recovery_not_found')
    if job.get('engine') != 'rvc':
        raise ValueError('rvc_recovery_engine_required')
    if job.get('state') in ('queued', 'running', 'completed'):
        return dict(job)
    if job.get('state') not in ('failed', 'needs_review'):
        raise ValueError('rvc_recovery_state_not_resumable')
    body = recover_body(state, jid, job)
    current = profile_reader(config)
    pinned = job.get('singing_voice') or {}
    if not current or current.get('engine') != 'rvc' or not pinned.get('config_sha256') or any(
            current.get(key) != pinned.get(key) for key in ('config_sha256', 'profile', 'engine')):
        raise ValueError('rvc_recovery_profile_changed')
    before = dict(job)
    job.update(state='queued', phase='resuming_rvc', reason='', resumed_at=time.time(),
               resume_count=job.get('resume_count', 0)+1)
    try:
        save()
    except BaseException:
        job.clear(); job.update(before)
        raise
    try:
        submit(jid, body)
    except Exception:
        job.update(state='needs_review', reason='rvc_recovery_schedule_failed')
        save()
        raise
    return dict(job)
