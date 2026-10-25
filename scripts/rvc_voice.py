"""Convert an explicit isolated vocal track with an explicit trained RVC model.

This does not create a character model from a short reference recording. It
does not change the robot's selected engine or send messages.
"""
import argparse
import json
import os
from pathlib import Path
import sys
from types import SimpleNamespace


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--project',required=True)
    p.add_argument('--model',required=True)
    p.add_argument('--index',default='')
    p.add_argument('--source',required=True)
    p.add_argument('--output',required=True)
    p.add_argument('--semitones',type=int,default=0)
    p.add_argument('--index-rate',type=float,default=.65)
    p.add_argument('--protect',type=float,default=.33)
    p.add_argument('--device',choices=['auto','cpu','cuda:0'],default='auto')
    p.add_argument('--precision',choices=['auto','float32'],default='auto')
    args=p.parse_args()
    if not -12<=args.semitones<=12 or not 0<=args.index_rate<=1 or not 0<=args.protect<=.5:raise ValueError('invalid_conversion_parameters')
    project=Path(args.project).resolve();model=Path(args.model).resolve();source=Path(args.source).resolve();output=Path(args.output).resolve()
    index=str(Path(args.index).resolve()) if args.index else ''
    if not model.is_file() or not source.is_file() or index and not Path(index).is_file():raise ValueError('conversion_resource_missing')
    os.environ.update(weight_root=str(model.parent),index_root=str(Path(index).parent) if index else str(project/'logs'),rmvpe_root=str(project/'assets/rmvpe'))
    os.chdir(project);sys.path.insert(0,str(project))
    import torch
    from scipy.io import wavfile
    from infer.modules.vc.modules import VC
    device=('cuda:0' if torch.cuda.is_available() else 'cpu') if args.device=='auto' else args.device
    if device=='cpu':torch.set_num_threads(2)
    # Bound CPU attention windows instead of materializing a full 30-second
    # float32 sequence. Training and inference still need separate memory.
    config=SimpleNamespace(device=device,is_half=device!='cpu' and args.precision=='auto',x_pad=1,x_query=3 if device=='cpu' else 6,x_center=8 if device=='cpu' else 38,x_max=10 if device=='cpu' else 41)
    vc=VC(config);vc.get_vc(model.name)
    status,(rate,audio)=vc.vc_single(0,str(source),args.semitones,None,'rmvpe',index,None,args.index_rate,3,0,.5,args.protect)
    if rate is None or audio is None or not status.startswith('Success'):raise RuntimeError('RVC did not produce valid audio: '+status)
    output.parent.mkdir(parents=True,exist_ok=True);wavfile.write(str(output),rate,audio)
    output.with_suffix('.rvc.json').write_text(json.dumps({'engine':'RVC','model':model.name,'semitones':args.semitones,'index_rate':args.index_rate,'protect':args.protect,'device':device,'precision':'float16' if config.is_half else 'float32','sample_rate':rate,'seconds':len(audio)/rate,'wechat_sends':0},indent=2),encoding='utf-8')
    print('RVC conversion complete; seconds',len(audio)/rate,'no messages sent',flush=True)


if __name__=='__main__':main()
