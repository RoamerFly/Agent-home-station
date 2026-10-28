"""Recover one YuE computation without creating a second song or delivery.

An absent backend prompt is not proof that its submission failed. Read-only
reattachment is the default; recreating a lost computation is explicit.
"""
import hashlib
import json
import os
from pathlib import Path
import stat
import time

from rvc_recovery import save_jobs, source_path


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


def read_file(path, limit):
    if path.is_symlink():
        raise ValueError('yue_recovery_file_link')
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0))
        with os.fdopen(fd, 'rb') as stream:
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                raise ValueError('yue_recovery_file_not_regular')
            data = stream.read(limit + 1)
    except FileNotFoundError:
        raise ValueError('yue_recovery_checkpoint_missing') from None
    if not 0 < len(data) <= limit:
        raise ValueError('yue_recovery_file_size')
    return data


def checkpoint_path(state, jid):
    # Reuse the strict ID and canonical account-state validation.
    return source_path(state, jid).with_name(jid + '-yue-request.json')


def persist(state, jid, body, graph, input_dir, prepared=True):
    references = []
    if prepared and (body.get('mode') == 'cover' or body.get('reference_base64')):
        name = 'wechat-ref-' + jid + '.wav'
        data = read_file(Path(input_dir) / name, 256 * 1024 * 1024)
        references.append({'name': name, 'sha256': hashlib.sha256(data).hexdigest()})
    value = {'version': 1, 'body': body, 'graph': graph, 'references': references, 'prepared': prepared}
    path = checkpoint_path(state, jid)
    if not prepared:
        path = path.with_name(jid + '-yue-queued.json')
    save_jobs(path, value)
    return hashlib.sha256(read_file(path, 40 * 1024 * 1024)).hexdigest()


def load(state, jid, job, input_dir, recipe_reader):
    path = checkpoint_path(state, jid)
    prepared = bool(job.get('checkpoint_sha256'))
    if not prepared:
        if job.get('submission_attempts') or job.get('prompt_id'):
            raise ValueError('yue_recovery_submission_checkpoint_missing')
        path = path.with_name(jid + '-yue-queued.json')
    data = read_file(path, 40 * 1024 * 1024)
    if hashlib.sha256(data).hexdigest() != job.get('checkpoint_sha256' if prepared else 'queued_checkpoint_sha256'):
        raise ValueError('yue_recovery_checkpoint_changed')
    value = json.loads(data)
    if value.get('version') != 1 or recipe_reader(value['body']) != job.get('recipe'):
        raise ValueError('yue_recovery_recipe_changed')
    if value.get('prepared', True) != prepared or digest(value['graph']) != job.get('graph_sha256' if prepared else 'queued_graph_sha256'):
        raise ValueError('yue_recovery_graph_changed')
    for item in value.get('references', []):
        if item['name'] != 'wechat-ref-' + jid + '.wav':
            raise ValueError('yue_recovery_reference_path')
        data = read_file(Path(input_dir) / item['name'], 256 * 1024 * 1024)
        if hashlib.sha256(data).hexdigest() != item['sha256']:
            raise ValueError('yue_recovery_reference_changed')
    return value


def find_prompt(job, api):
    """History, queue, then history again closes the running-to-history race."""
    matches = set()
    client_id = 'wechat-yue-' + job['id']
    known = job.get('prompt_id')

    def examine(item):
        if not isinstance(item, (list, tuple)) or len(item) < 4 or not isinstance(item[3], dict):
            return
        extra = item[3]
        if extra.get('client_id') != client_id:
            if item[1] == known:
                raise ValueError('yue_recovery_prompt_identity_mismatch')
            return
        if extra.get('wechat_graph_sha256') != job.get('graph_sha256'):
            raise ValueError('yue_recovery_prompt_identity_mismatch')
        if known and item[1] != known:
            raise ValueError('yue_recovery_prompt_identity_mismatch')
        matches.add(item[1])

    for history in (api('/history'),):
        for entry in history.values():
            examine(entry.get('prompt'))
    queue = api('/queue')
    for key in ('queue_running', 'queue_pending'):
        if key not in queue or not isinstance(queue[key], list):
            raise ValueError('yue_recovery_queue_invalid')
        for item in queue[key]:
            examine(item)
    for entry in api('/history').values():
        examine(entry.get('prompt'))
    if len(matches) > 1:
        raise ValueError('yue_recovery_multiple_prompts')
    return next(iter(matches), None)


def resume(jobs, jid, state, input_dir, recipe_reader, profile_reader, api, save, submit, restart_missing=False):
    """Caller holds the journal lock; submit accepts the frozen checkpoint."""
    checkpoint_path(state, jid)
    job = jobs.get(jid)
    if job is None:
        raise ValueError('yue_recovery_not_found')
    if job.get('engine') == 'rvc':
        raise ValueError('yue_recovery_engine_required')
    if job.get('state') in ('queued', 'running', 'completed'):
        return dict(job)
    if job.get('state') not in ('needs_review', 'failed'):
        raise ValueError('yue_recovery_state_not_resumable')
    checkpoint = load(state, jid, job, input_dir, recipe_reader)
    pinned = job.get('singing_voice')
    if pinned:
        current = profile_reader()
        if not current or any(current.get(k) != pinned.get(k) for k in ('profile', 'engine', 'config_sha256')):
            raise ValueError('yue_recovery_voice_configuration_changed')
    prepared = checkpoint.get('prepared', True)
    prompt_id = find_prompt(job, api) if prepared else None
    if prepared and not prompt_id and not restart_missing:
        raise ValueError('yue_recovery_prompt_missing_explicit_restart_required')
    before = dict(job)
    job.update(state='queued', phase='reattaching_yue' if prompt_id else ('restarting_yue' if prepared else 'preparing_yue'), reason='',
               resumed_at=time.time(), resume_count=job.get('resume_count', 0) + 1)
    # A confirmed backend loss permits a new prompt, preserving the song ID,
    # graph, seed, reference and pinned voice. No media is sent here.
    if prompt_id:
        job['prompt_id'] = prompt_id
    elif prepared:
        job.pop('prompt_id', None)
        job['backend_absent_verified_at'] = time.time()
    try:
        save()
    except BaseException:
        job.clear(); job.update(before)
        raise
    try:
        submit(jid, checkpoint, prompt_id)
    except Exception:
        job.update(state='needs_review', reason='yue_recovery_schedule_failed')
        save()
        raise
    return dict(job)
