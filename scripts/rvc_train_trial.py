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


def training_layout(project):
    root = Path(project)
    if (root/'train/train.py').is_file():
        return {'preprocess': 'train/preprocess.py', 'pitch': 'train/dataset/extract_f0.py',
                'features': 'train/dataset/extract_hubert_feature.py', 'train': 'train/train.py', 'official': True}
    if (root/'infer/modules/train/train.py').is_file():
        return {'preprocess': 'infer/modules/train/preprocess.py', 'pitch': 'infer/modules/train/extract/extract_f0_rmvpe.py',
                'features': 'infer/modules/train/extract_feature_print.py', 'train': 'infer/modules/train/train.py', 'official': False}
    raise ValueError('unsupported_rvc_training_layout')


def create_shadow(source, destination):
    """Keep all mutable training state private; reuse only read-only assets."""
    source, destination = Path(source).resolve(strict=True), Path(destination).resolve()
    if destination.exists():
        raise ValueError('training_shadow_destination_must_be_new')
    destination.mkdir(parents=True)
    for name in ('infer', 'train', 'configs', 'i18n', 'tools'):
        if (source/name).is_dir():
            shutil.copytree(source/name, destination/name, ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
    (destination/'assets/weights').mkdir(parents=True)
    for name in ('pretrained_v2', 'rmvpe', 'hubert_base'):
        if (source/'assets'/name).exists():
            (destination/'assets'/name).symlink_to((source/'assets'/name).resolve(), target_is_directory=True)
    (destination/'logs').mkdir()
    (destination/'training-source.json').write_text(json.dumps({'source_project': str(source), 'mutable_assets_shared': False}), encoding='utf-8')


def fix_shadow_preprocess(project):
    """The pinned upstream saved only the last short silence-sliced phrase.

    Patch this exact known layout in the private copy, never shared upstream.
    Every slicer's final tail must be written inside its outer loop.
    """
    root=Path(project)
    if not (root/'training-source.json').is_file():return
    file=root/'train/preprocess.py'
    if not file.is_file():return
    source=file.read_text(encoding='utf-8')
    old='            self.norm_write(tmp_audio, output_key, idx1)\n            if should_report'
    fixed='                self.norm_write(tmp_audio, output_key, idx1)\n            if should_report'
    if old in source:
        file.write_text(source.replace(old,fixed,1),encoding='utf-8')
        meta=json.loads((root/'training-source.json').read_text())
        meta['compatibility_patches']=['retain_each_silence_sliced_tail']
        (root/'training-source.json').write_text(json.dumps(meta,indent=2),encoding='utf-8')


def prepare_mute(project):
    import numpy as np
    from scipy.io import wavfile
    root = Path(project)/'logs/mute'
    if (root/'0_gt_wavs/mute32k.wav').is_file():
        return
    for name in ('0_gt_wavs', '3_feature768', '2a_f0', '2b-f0nsf'):
        (root/name).mkdir(parents=True, exist_ok=True)
    wavfile.write(root/'0_gt_wavs/mute32k.wav', 32000, np.zeros(64000, dtype=np.int16))
    np.save(root/'3_feature768/mute.npy', np.zeros((100,768), dtype=np.float32))
    np.save(root/'2a_f0/mute.wav.npy', np.ones(200, dtype=np.int64))
    np.save(root/'2b-f0nsf/mute.wav.npy', np.zeros(200, dtype=np.float32))


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
    p.add_argument('--source-project',default='',help='Create a NEW private shadow from this registered source project')
    p.add_argument('--dataset',required=True)
    p.add_argument('--experiment',default='bluewhale-C-trial')
    p.add_argument('--epochs',type=int,default=30)
    p.add_argument('--device',choices=['auto','cpu','cuda:0'],default='auto')
    p.add_argument('--batch-size',type=int,default=4)
    p.add_argument('--save-every',type=int,default=10)
    p.add_argument('--workers',type=int,default=2)
    p.add_argument('--extra-python-path',action='append',default=[],help='Explicit isolated training dependency directory; does not change the inference runtime')
    p.add_argument('--resume-from',default='')
    p.add_argument('--stage',choices=['prepare','train','index'],required=True)
    args=p.parse_args()
    if not args.experiment.replace('-','').replace('_','').isalnum():raise ValueError('invalid_experiment')
    if min(args.epochs,args.batch_size,args.save_every,args.workers)<1:raise ValueError('positive_training_parameters_required')
    # Resolving a venv interpreter symlink executes the base Python without
    # that venv's torch/CUDA packages. Preserve its invoked absolute path.
    project=Path(args.project).resolve();python=str(Path(args.python).absolute());dataset=Path(args.dataset).resolve()
    if args.source_project:
        if args.stage!='prepare':raise ValueError('new_shadow_requires_prepare')
        create_shadow(args.source_project, project)
    layout=training_layout(project)
    experiment=project/'logs'/args.experiment
    if args.resume_from:
        if args.stage!='train':raise ValueError('resume_requires_training_stage')
        experiment=resume_experiment(project,args.resume_from,args.experiment)
    else:experiment.mkdir(parents=True,exist_ok=True)
    env=dict(os.environ, PYTHONPATH=os.pathsep.join([str(project),*map(lambda x:str(Path(x).absolute()),args.extra_python_path)]), OMP_NUM_THREADS=str(args.workers), MKL_NUM_THREADS=str(args.workers),
             RVC_AUDIO_FORCE_CPU='1', HF_HUB_OFFLINE='1', TRANSFORMERS_OFFLINE='1')
    if args.device=='cpu':env['CUDA_VISIBLE_DEVICES']=''
    probe=subprocess.check_output([python,'-c',"import json,torch; ok=torch.cuda.is_available(); print(json.dumps({'cuda':ok,'build':torch.version.cuda,'name':torch.cuda.get_device_name(0) if ok else None})); x=torch.ones(8,device='cuda' if ok else 'cpu'); assert x.sum().item()==8"], env=env, text=True)
    hardware=json.loads(probe.splitlines()[0]);device='cuda:0' if hardware['cuda'] and args.device!='cpu' else 'cpu'
    if args.device=='cuda:0' and device=='cpu':raise RuntimeError('requested_cuda_unavailable: check gpu=true sandbox mount and registered CUDA runtime; no CPU fallback')
    state_path=experiment/'training-state.json'
    if state_path.is_file():
        previous=json.loads(state_path.read_text(encoding='utf-8'))
        if previous.get('stage') in ('prepare','train','index'):
            (experiment/(previous['stage']+'-state.json')).write_text(json.dumps(previous,indent=2),encoding='utf-8')
    state={'stage':args.stage,'state':'requested','device':device,'hardware':hardware,'epochs':args.epochs,'batch_size':args.batch_size,'optimizer_steps_verified':False}
    def save_state():
        encoded=json.dumps(state,indent=2)
        state_path.write_text(encoded,encoding='utf-8')
        (experiment/(args.stage+'-state.json')).write_text(encoded,encoding='utf-8')
    save_state();print(json.dumps(state),flush=True)
    def run(relative,*arguments):
        entry=['-m',relative.removesuffix('.py').replace('/','.')] if layout['official'] else [str(project/relative)]
        command=[python,*entry,*map(str,arguments)]
        child=subprocess.Popen(command,cwd=project,env=env)
        state.update(state='running',pid=child.pid,entry=relative);save_state()
        code=child.wait()
        if code:
            state.update(state='failed',exit_code=code);save_state()
            raise subprocess.CalledProcessError(code,command)
    try:
        if args.stage=='prepare':
            fix_shadow_preprocess(project)
            run(layout['preprocess'],dataset,32000,args.workers,experiment,'False',3.7)
            if layout['official']:
                pitch_args=('cuda',1,0,0,experiment,'True') if device!='cpu' else ('cpu',experiment,args.workers,'rmvpe')
                run(layout['pitch'],*pitch_args)
                run(layout['features'],device,1,0,0,experiment,'v2','True')
            else:
                if device=='cpu':raise ValueError('legacy_prepare_requires_cuda_rmvpe')
                run(layout['pitch'],1,0,0,experiment,'True')
                run(layout['features'],device,1,0,0,experiment,'v2')
            prepare_mute(project)
            rows=training_filelist(experiment,project/'logs/mute')
            if len(rows)-2 != len(list((experiment/'0_gt_wavs').glob('*.wav'))):
                state.update(state='failed',reason='training_feature_coverage_incomplete');save_state()
                raise ValueError('training_feature_coverage_incomplete')
            (experiment/'filelist.txt').write_text('\n'.join(rows),encoding='utf-8')
            config=json.loads((project/'configs/v2/32k.json').read_text())
            config['train']['log_interval']=20
            (experiment/'config.json').write_text(json.dumps(config,indent=2),encoding='utf-8')
            state.update(state='completed',complete_samples=len(rows)-2);save_state()
            print('Prepared complete samples:',len(rows)-2,flush=True)
        elif args.stage=='train':
            # Inference-only installs can expose CUDA yet lack the trainer's
            # logging dependencies. Check them before loading large weights.
            subprocess.run([python,'-c',"from torch.utils.tensorboard import SummaryWriter; import matplotlib; matplotlib.use('Agg'); import matplotlib.pyplot as plt; f=plt.figure(); f.canvas.draw(); assert hasattr(f.canvas,'tostring_rgb'), 'trainer requires Matplotlib <3.10'; plt.close(f)"],env=env,check=True)
            run(layout['train'],'-e',args.experiment,'-sr','32k','-f0',1,'-bs',args.batch_size,'-g',0,'-te',args.epochs,'-se',args.save_every,'-pg',project/'assets/pretrained_v2/f0G32k.pth','-pd',project/'assets/pretrained_v2/f0D32k.pth','-l',1,'-c',0,'-sw',1,'-v','v2')
            final=project/'assets/weights'/(args.experiment+'.pth')
            if not final.is_file():raise ValueError('training_did_not_export_final_model')
            # The upstream parent can return zero even when its spawned trainer
            # crashes. Intermediate checkpoints alone are not a completed trial.
            subprocess.run([python,'-c',"import sys,torch; c=torch.load(sys.argv[1],map_location='cpu'); assert c.get('info')==sys.argv[2] and c.get('version')=='v2' and c.get('f0')==1, 'training export did not reach requested epoch'",str(final),str(args.epochs)+'epoch'],check=True)
            state.update(state='completed',optimizer_steps_verified=True,model_sha256=checkpoint_digest(final));save_state()
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
            state.update(state='completed',index_vectors=int(index.ntotal));save_state()
    except BaseException as error:
        state.update(state="failed",reason=type(error).__name__)
        save_state()
        raise



if __name__=='__main__':main()
