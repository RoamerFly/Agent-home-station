"""Prepare only pinned native source + reviewed patch as a Docker context.

No user profiles, local target/ files or binaries are copied. Supply an existing
upstream clone with --source, or clone the public upstream into ignored temp.
"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def prepare(source, output):
    manifest = json.loads((ROOT / 'components/native-client/source-manifest.json').read_text())
    patch = ROOT / manifest['patch']
    if hashlib.sha256(patch.read_bytes()).hexdigest() != manifest['patch_sha256']:
        raise ValueError('native_patch_integrity_failed')
    output = output.resolve()
    if not output.is_relative_to(ROOT / 'temp') or output.exists():
        raise ValueError('native_context_requires_new_ignored_temp_directory')
    archive = subprocess.check_output(['git','-C',str(source),'archive',manifest['base_commit'],'packages/native-client-rust'])
    output.mkdir(parents=True)
    with tarfile.open(fileobj=io.BytesIO(archive)) as stream:
        for item in stream.getmembers():
            if not (item.isdir() or item.isfile()):
                raise ValueError('native_source_link_not_allowed')
        stream.extractall(output, filter='data')
    # This context is below the application's Git worktree. Without its own
    # repository git apply can discover the parent and silently skip every
    # native path as outside the current subdirectory.
    subprocess.run(['git','init','-q',str(output)],check=True)
    subprocess.run(['git','apply','--check',str(patch)],cwd=output,check=True)
    subprocess.run(['git','apply',str(patch)],cwd=output,check=True)
    subprocess.run(['git','apply','--reverse','--check',str(patch)],cwd=output,check=True)
    (output / '.dockerignore').write_text('**/target/\n**/.git/\n',encoding='utf8')
    print(json.dumps({'context':str(output),'revision':manifest['base_commit'],'patch_sha256':manifest['patch_sha256'],'runtime_data_included':False}))


if __name__ == '__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--source',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    prepare(args.source,args.output)
