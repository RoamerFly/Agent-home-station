"""Explicit offline RVC trial in a private shadow project, never live deployment.

Requires the owner's prepared RVC runtime, synthetic/authorized dataset and
pretrained weights. No provider calls, downloads or WeChat sends.
"""
import argparse
import json
import os
from pathlib import Path
import random
import shutil
import hashlib


def checkpoint_digest(path):
    digest=hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda:stream.read(1<<20),b''):digest.update(block)
    return digest.hexdigest()
import subprocess


def training_filelist(experiment, mute, sample_rate='32k'):
    experiment, mute = Path(experiment), Path(mute)
    names=sorted(p.stem for p in (experiment/'0_gt_wavs').glob('*.wav') if (experiment/'3_feature768'/(p.stem+'.npy')).is_file() and (experiment/'2a_f0'/(p.name+'.npy')).is_file() and (experiment/'2b-f0nsf'/(p.name+'.npy')).is_file())
    if not names:raise ValueError('no_complete_training_samples')
    def line(root,name,wav):
        return '|'.join([str((root/'0_gt_wavs'/wav).resolve()).replace('\\','/'),str((root/'3_feature768'/(name+'.npy')).resolve()).replace('\\','/'),str((root/'2a_f0'/(name+'.wav.npy')).resolve()).replace('\\','/'),str((root/'2b-f0nsf'/(name+'.wav.npy')).resolve()).replace('\\','/'),'0'])
    rows=[line(experiment,n,n+'.wav')for n in names]
    rows.extend([line(mute,'mute','mute'+sample_rate+'.wav')]*2)
    random.Random(20261025).shuffle(rows)
    return rows


def resume_experiment(project, previous, target):
    """Copy mutable optimizer checkpoints; never hardlink to the selected model."""
    for name in (previous, target):
        if not name or not name.replace('-', '').replace('_', '').isalnum():
            raise ValueError('invalid_experiment')
    project = Path(project).resolve()
    source, destination = project/'logs'/previous, project/'logs'/target
    if source == destination or destination.exists():
        raise ValueError('resume_target_must_be_new')
    names = ['config.json', 'filelist.txt', 'G_2333333.pth', 'D_2333333.pth']
    if not all((source/name).is_file() and not (source/name).is_symlink() for name in names):
        raise ValueError('resume_checkpoint_missing')
    staging = destination.with_name(destination.name+'.preparing')
    staging.mkdir()
    hashes = {}
    for name in names:
        original = source/name
        hashes[name] = checkpoint_digest(original)
        shutil.copy2(original, staging/name)
        if checkpoint_digest(staging/name) != hashes[name] or checkpoint_digest(original) != hashes[name]:
            raise ValueError('resume_checkpoint_changed')
    (staging/'resume-source.json').write_text(json.dumps({'source_experiment': previous, 'copied_files_sha256': hashes, 'shared_mutable_files': False}, indent=2), encoding='utf-8')
    staging.rename(destination)
    return destination


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--python',required=True)
    p.add_argument('--project',required=True)
    p.add_argument('--dataset',required=True)
    p.add_argument('--experiment',default='bluewhale-C-trial')
    p.add_argument('--epochs',type=int,default=30)
    p.add_argument('--resume-from',default='')
    p.add_argument('--stage',choices=['prepare','train','index'],required=True)
    args=p.parse_args()
    if not args.experiment.replace('-','').replace('_','').isalnum():raise ValueError('invalid_experiment')
    if not 1<=args.epochs<=200:raise ValueError('invalid_epoch_budget')
    project=Path(args.project).resolve();python=str(Path(args.python).resolve());dataset=Path(args.dataset).resolve()
    experiment=project/'logs'/args.experiment
    if args.resume_from:
        if args.stage!='train':raise ValueError('resume_requires_training_stage')
        experiment=resume_experiment(project,args.resume_from,args.experiment)
    else:experiment.mkdir(parents=True,exist_ok=True)
    def run(relative,*arguments):
        subprocess.run([python,str(project/relative),*map(str,arguments)],cwd=project,check=True)
    if args.stage=='prepare':
        run('infer/modules/train/preprocess.py',dataset,32000,2,experiment,'False',3.7)
        run('infer/modules/train/extract/extract_f0_rmvpe.py',1,0,0,experiment,'True')
        run('infer/modules/train/extract_feature_print.py','cuda:0',1,0,0,experiment,'v2')
        rows=training_filelist(experiment,project/'logs/mute')
        (experiment/'filelist.txt').write_text('\n'.join(rows),encoding='utf-8')
        config=json.loads((project/'configs/v2/32k.json').read_text())
        config['train']['log_interval']=20
        (experiment/'config.json').write_text(json.dumps(config,indent=2),encoding='utf-8')
        print('Prepared complete samples:',len(rows)-2,flush=True)
    elif args.stage=='train':
        run('infer/modules/train/train.py','-e',args.experiment,'-sr','32k','-f0',1,'-bs',4,'-g',0,'-te',args.epochs,'-se',10,'-pg',project/'assets/pretrained_v2/f0G32k.pth','-pd',project/'assets/pretrained_v2/f0D32k.pth','-l',1,'-c',0,'-sw',1,'-v','v2')
        final=project/'assets/weights'/(args.experiment+'.pth')
        if not final.is_file():raise ValueError('training_did_not_export_final_model')
        # The upstream parent can return zero even when its spawned trainer
        # crashes. Intermediate checkpoints alone are not a completed trial.
        subprocess.run([python,'-c',"import sys,torch; c=torch.load(sys.argv[1],map_location='cpu'); assert c.get('info')==sys.argv[2] and c.get('version')=='v2' and c.get('f0')==1, 'training export did not reach requested epoch'",str(final),str(args.epochs)+'epoch'],check=True)
    else:
        import numpy as np
        import faiss
        arrays=[np.load(p,allow_pickle=False)for p in sorted((experiment/'3_feature768').glob('*.npy'))]
        features=np.concatenate(arrays).astype('float32')
        if features.ndim!=2 or features.shape[1]!=768 or not np.isfinite(features).all():raise ValueError('invalid_features')
        # Exact L2 index is deterministic and sufficient for a small trial.
        index=faiss.IndexFlatL2(768);index.add(features)
        target=experiment/(args.experiment+'-flat.index')
        faiss.write_index(index,str(target));print('Index vectors:',index.ntotal,flush=True)


if __name__=='__main__':main()
