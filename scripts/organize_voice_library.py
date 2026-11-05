"""Organize registered voice assets by character without changing file IDs.

Use during a drained/offline library update. Checkpoints are physically moved,
not linked outside the managed library. Catalog publication is atomic; a failed
move rolls completed moves back. Public singing configs retain their bytes.
"""
import argparse
import copy
import json
from pathlib import Path
import re

from migrate_managed_workspaces import digest
from rename_account_workspace import atomic,check_tree


def label(name):
    name=re.sub(r'[\\/:<>"|?*\x00-\x1f]','_',str(name)).strip(' .')
    if not name or name in {'.','..'}:raise ValueError('invalid_role_name')
    return name


def role_name(item):
    if item.get('role'):return label(item['role'])
    name=item['name']
    if '大肥鱼' in name or item['id'].startswith('whale-'):return '蓝色大肥鱼'
    if item['id']=='manbo-tlme' or '曼波' in name:return '曼波'
    return label(name)


def organize(root,shared=False,exclude_ids=(),apply=False):
    root=root.absolute();check_tree(root)
    before=(root/'catalog.json').read_bytes()
    catalog=copy.deepcopy(json.loads(before))
    catalog['items']=[x for x in catalog['items'] if x['id'] not in exclude_ids]
    moves={};canonical={};roles={};directories={}
    for item in catalog['items']:
        role=role_name(item);folder=label(role)
        if folder.casefold() in directories and directories[folder.casefold()]!=role:
            raise ValueError('case_colliding_role_names')
        directories[folder.casefold()]=role
        item['role'],item['directory']=role,folder
        roles.setdefault(role,[]).append(item)
        for category in ['weights','references','previews']:
            for f in item.get(category,[]):
                identity=f.get('file') or f['sha256']+Path(f['filename']).suffix.lower()
                if not re.fullmatch(r'[a-f0-9]{64}\.[a-z0-9]+',identity):raise ValueError('invalid_voice_file_id')
                source=Path(f['path']) if shared else root/f.get('relative_path','files/'+identity)
                source=source.absolute();check_tree(source)
                if not source.is_relative_to(root):raise ValueError('voice_source_outside_library')
                if not source.is_file():raise ValueError('voice_source_missing')
                target=root/folder/category/label(f['filename'])
                # Keep one immutable object for shared references; entry IDs and
                # original filenames remain independent of physical storage.
                if f['sha256'] in canonical:target=canonical[f['sha256']]
                elif any(v[0]==target and v[1]!=f['sha256'] for v in moves.values()):
                    target=target.with_name(target.stem+'--'+f['sha256'][:8]+target.suffix)
                canonical[f['sha256']]=target
                if source!=target:moves[source]=(target,f['sha256'],f['bytes'])
                f['file']=identity
                f['relative_path']=target.relative_to(root).as_posix()
                if shared:f['path']=str(target)
    # The RVC config stays byte-identical and beside the relocated weights.
    # singing_config resolves missing old paths only after matching pinned SHA.
    if shared:
        parents={source.parent for source in moves}
        for parent in list(parents):
            targets=[v[0] for s,v in moves.items() if s.parent==parent]
            if not targets:continue
            weights=targets[0].parent
            config=parent/'singing-voice.config'
            if config.is_file():moves[config]=(weights/config.name,digest(config),config.stat().st_size)
            readme=parent/'README.md'
            if readme.is_file():moves[readme]=(weights.parent/'README.md',digest(readme),readme.stat().st_size)
    report={'roles':len(roles),'variants':len(catalog['items']),'moves':{str(s):str(v[0]) for s,v in moves.items()},'applied':apply}
    if not apply:return report
    completed=[]
    duplicate=[]
    metadata={}
    try:
        for source,(target,checksum,size) in moves.items():
            check_tree(target.parent)
            if source.stat().st_size!=size or digest(source)!=checksum:raise ValueError('voice_hash_mismatch')
            target.parent.mkdir(parents=True,exist_ok=True)
            if target.exists():
                if target.stat().st_size!=size or digest(target)!=checksum:raise ValueError('role_target_conflict')
                duplicate.append((source,target,checksum,size))
                continue
            source.rename(target);completed.append((source,target))
        if (root/'catalog.json').read_bytes()!=before:raise ValueError('catalog_changed_during_organization')
        for role,entries in roles.items():
            folder=root/label(role);folder.mkdir(exist_ok=True)
            path=folder/'role.json'
            metadata[path]=path.read_bytes() if path.exists() else None
            atomic(path,json.dumps({'role':role,'variants':[{'id':x['id'],'name':x['name'],'engine':x['engine']} for x in entries]},ensure_ascii=False,indent=2).encode())
        # Publish the authoritative index only after every role is ready.
        if (root/'catalog.json').read_bytes()!=before:raise ValueError('catalog_changed_during_organization')
        atomic(root/'catalog.json',(json.dumps(catalog,ensure_ascii=False,indent=2)+'\n').encode())
    except BaseException:
        for path,data in metadata.items():
            if data is None:path.unlink(missing_ok=True)
            else:atomic(path,data)
        for source,target in reversed(completed):
            source.parent.mkdir(parents=True,exist_ok=True)
            if target.exists() and not source.exists():target.rename(source)
        raise
    # Publication succeeded. Cleanup failure must not restore an index pointing
    # at already removed duplicates; report remaining cleanup for a later pass.
    pending=[]
    for source,target,checksum,size in duplicate:
        try:
            if any(p.stat().st_size!=size or digest(p)!=checksum for p in [source,target]):
                raise ValueError('voice_changed_before_duplicate_cleanup')
            source.unlink()
        except (OSError,ValueError):pending.append(str(source))
    for parent in sorted({s.parent for s in moves},key=lambda p:len(p.parts),reverse=True):
        try:
            if parent!=root and parent.exists() and not any(parent.iterdir()):parent.rmdir()
        except OSError:pending.append(str(parent))
    report['cleanup_pending']=pending
    return report


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--library',required=True,type=Path)
    p.add_argument('--public',action='store_true')
    p.add_argument('--exclude-id',action='append',default=[])
    p.add_argument('--apply',action='store_true')
    a=p.parse_args()
    print(json.dumps(organize(a.library,a.public,a.exclude_id,a.apply),ensure_ascii=False))
