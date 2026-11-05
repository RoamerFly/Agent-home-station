"""Verified offline rename of ONE account workspace and private voice archive.

Stop/drain the management and music workers first; leave native WeChat online.
Only structured path fields are rebased. Conversation text and original inbox,
asset and output file bytes are never rewritten. Default is a read-only plan.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import time

from migrate_managed_workspaces import copy_verified, digest
from migration_io import Directory, source_files


TEXT_FIELDS={'text','content','summary','notes','system_prompt','prompt','lyrics','transcript','current_focus','error','reason'}


def rebase(value, old, new, field=''):
    if field.lower() in TEXT_FIELDS:
        return value
    if isinstance(value,dict):
        return {k:rebase(v,old,new,k) for k,v in value.items()}
    if isinstance(value,list):
        return [rebase(v,old,new,field) for v in value]
    if isinstance(value,str):
        if value==old or value.startswith(old+'/') or value.startswith(old+'\\'):
            return new+value[len(old):]
        if field.lower() in {'command','command_line','execstart'}:
            return value.replace(old,new)
        if field.lower() in {'arguments','metadata','params','request'} and value.startswith(('{','[')):
            try:
                changed=rebase(json.loads(value),old,new)
                if changed != json.loads(value):
                    return json.dumps(changed,ensure_ascii=False,separators=(',',':'))
            except ValueError:
                pass
    return value


def atomic(path,data):
    with tempfile.NamedTemporaryFile(dir=path.parent,delete=False) as f:
        pending=Path(f.name)
        f.write(data)
        f.flush();os.fsync(f.fileno())
    try:
        os.chmod(pending,0o600)
        os.replace(pending,path)
    finally:
        pending.unlink(missing_ok=True)


def check_tree(path):
    for parent in [path,*path.parents]:
        if parent.is_symlink() or getattr(parent,'is_junction',lambda:False)():
            raise ValueError('migration_path_must_not_be_link')


def cleanup_legacy(admin,private):
    source,target=admin/'voice-library',private/'voice-library'
    if not source.exists():return 0
    files=list(source_files(source,set(),set()))
    for path,relative,_ in files:
        if digest(path)!=digest(target/relative):raise ValueError('legacy_voice_not_verified')
    size=0
    for path,relative,_ in files:
        # Check again immediately before deleting this verified duplicate.
        if digest(path)!=digest(target/relative):raise ValueError('legacy_voice_changed')
        size+=path.stat().st_size
        path.unlink()
    for directory in sorted((p for p in source.rglob('*') if p.is_dir()),key=lambda p:len(p.parts),reverse=True):
        check_tree(directory);directory.rmdir()
    source.rmdir()
    return size


def migrate(workspace,admin,state,account,name,backup,units=(),apply=False):
    if not name or len(name)>100 or name.strip()!=name or name in {'.','..'} or re.search(r'[\\/:<>"|?*\x00-\x1f]',name) or name.endswith('.'):
        raise ValueError('invalid_workspace_directory_name')
    for path in [workspace,admin,state,backup.parent,*units]:check_tree(path)
    workspace,admin,state=map(lambda p:p.resolve(strict=True),(workspace,admin,state))
    scope=hashlib.sha256(account.encode()).hexdigest()
    parent=workspace/'pri_workspace'
    old,new=parent/scope,parent/name
    if not old.is_dir() or new.exists():raise ValueError('migration_requires_original_and_unused_target')
    check_tree(old)
    source_voice=admin/'voice-library'
    voice_files=list(source_files(source_voice,set(),set())) if source_voice.exists() else []
    candidates=set()
    for base in [admin,state,workspace/'public_workspace/manifests',old/'music',old/'global/tasks']:
        if base.is_dir():
            candidates.update(base.rglob('*.json'))
    deployment=workspace/'secrets/local-deployment.json'
    if deployment.is_file():candidates.add(deployment)
    for tasks in (old/'conversations').glob('*/tasks') if (old/'conversations').exists() else []:
        candidates.update(tasks.rglob('*.json'))
    # Bind only this account's operational documents, never downloaded originals.
    changes=[]
    for path in sorted(candidates):
        check_tree(path)
        if path.is_relative_to(source_voice):continue
        before=path.read_bytes()
        try:value=json.loads(before.decode('utf-8-sig'))
        except (ValueError,UnicodeError):continue
        changed=rebase(value,str(old),str(new))
        # Windows-shaped metadata uses the same physical managed tree.
        if str(old).startswith('/mnt/'):
            winold=str(old)[5].upper()+':'+str(old)[6:].replace('/','\\')
            winnew=str(new)[5].upper()+':'+str(new)[6:].replace('/','\\')
            changed=rebase(changed,winold,winnew)
        if changed!=value:
            changes.append((path,before,(json.dumps(changed,ensure_ascii=False,indent=2)+'\n').encode()))
    # Frozen computation checkpoints must retain verified graph/content hashes.
    # Refuse a generic rewrite of one: migrate its paired journal explicitly.
    if any('-yue-request.json' in p.name or '-yue-queued.json' in p.name for p,_,_ in changes):
        raise ValueError('frozen_music_checkpoint_needs_paired_digest_migration')
    result={'old':str(old),'new':str(new),'account_scope_unchanged':scope,
        'json_files_to_rebase':len(changes),'voice_files':len(voice_files),
        'voice_bytes':sum(size for _,_,size in voice_files),'applied':apply}
    if not apply:return result
    backup.mkdir(mode=0o700,parents=True,exist_ok=False)
    marker=admin/'workspace-directory'
    extra=[(marker,marker.read_bytes() if marker.exists() else None)]
    for unit in units:extra.append((unit,unit.read_bytes()))
    journal=[]
    moved=False
    applied=[]
    applied_extra=[]
    try:
        for path,relative,_ in voice_files:
            copy_verified(path,old/'voice-library'/relative)
        for i,(path,before,after) in enumerate(changes):
            atomic(backup/(str(i)+'.json.before'),before)
            journal.append({'path':str(path),'sha256_before':hashlib.sha256(before).hexdigest(),'sha256_after':hashlib.sha256(after).hexdigest()})
            if path.read_bytes()!=before:raise ValueError('state_changed_while_migrating')
            atomic(path,after)
            applied.append((path,before,after))
        atomic(backup/'journal.json',json.dumps(journal,ensure_ascii=False,indent=2).encode())
        old.rename(new);moved=True
        identity={'version':1,'account_id':account,'scope':scope,'directory':name}
        atomic(new/'account.json',json.dumps(identity,ensure_ascii=False,indent=2).encode())
        atomic(marker,(name+'\n').encode())
        applied_extra.append((marker,extra[0][1],(name+'\n').encode()))
        for unit,before in extra[1:]:
            if unit.read_bytes()!=before:raise ValueError('unit_changed_while_migrating')
            after=before.replace(str(old).encode(),str(new).encode())
            atomic(unit,after)
            applied_extra.append((unit,before,after))
        atomic(new/'WORKSPACE.md',(
            '# 账号工作区\n\n当前目录：'+name+'\n\n账号：'+account+'\n\n'
            '- voice-library：本账号个人声线权重、参考录音、试听及索引。\n'
            '- global：管理员跨会话任务与笔记。\n'
            '- conversations：每个群聊或私聊的独立文件、笔记和任务。\n'
            '- assets：本账号素材原件与转换索引。\n'
            '- music：本账号歌曲任务、原音频与生成结果。\n\n'
            '目录名用于识别；account.json中的稳定账号身份用于隔离。旧名称 '+scope+' 已迁移，请使用工具返回的当前路径。\n'
        ).encode())
    except BaseException:
        if moved:new.rename(old)
        for path,before,after in applied:
            if path.read_bytes()==after:atomic(path,before)
        for path,before,after in applied_extra:
            if path.read_bytes()==after:
                if before is None:path.unlink(missing_ok=True)
                else:atomic(path,before)
        raise
    return result


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    for arg in ['workspace','admin-dir','state-dir','backup']:
        p.add_argument('--'+arg,required=True,type=Path)
    p.add_argument('--account-id',required=True)
    p.add_argument('--directory',required=True)
    p.add_argument('--unit',action='append',type=Path,default=[])
    p.add_argument('--apply',action='store_true')
    a=p.parse_args()
    print(json.dumps(migrate(a.workspace,a.admin_dir,a.state_dir,a.account_id,a.directory,a.backup,a.unit,a.apply),ensure_ascii=False))
