"""Convert an explicit isolated vocal track with an explicit trained RVC model.

This does not create a character model from a short reference recording. It
does not change the robot's selected engine or send messages.
"""
import argparse
import json
import os
from pathlib import Path
import sys
import subprocess
from types import SimpleNamespace


def decode_audio_pcm(file, rate):
    """Use the managed FFmpeg ABI instead of RVC's obsolete PyAV API."""
    source=Path(file)
    if not source.is_file() or isinstance(rate,bool) or not isinstance(rate,int) or rate<=0:
        raise ValueError('invalid_audio_source_or_rate')
    from process_window import no_console_options
    result=subprocess.run(['ffmpeg','-nostdin','-v','error','-i',str(source),'-f','f32le','-ac','1','-ar',str(rate),'pipe:1'],capture_output=True,**no_console_options())
    if result.returncode:
        raise RuntimeError('audio_decode_failed: '+(result.stderr or b'').decode('utf8',errors='replace')[:500])
    if not result.stdout or len(result.stdout)%4:raise ValueError('invalid_decoded_pcm')
    return result.stdout


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
    p.add_argument('--pitch-chunk-seconds',type=float,default=10,help='RMVPE core slice in seconds; 0 uses the whole recording')
    p.add_argument('--pitch-overlap-seconds',type=float,default=1,help='Pitch context on each side, aligned to the 10 ms model grid')
    p.add_argument('--conversion-chunk-seconds',type=float,default=8,help='Core vocal conversion slice; 0 keeps upstream whole-recording behavior')
    p.add_argument('--conversion-context-seconds',type=float,default=1,help='Vocal context around each slice, removed before joining')
    args=p.parse_args()
    from rvc_pitch import pitch_parameters
    pitch_parameters(args.pitch_chunk_seconds,args.pitch_overlap_seconds)
    from rvc_windows import conversion_parameters
    conversion_options=conversion_parameters(args.conversion_chunk_seconds,args.conversion_context_seconds)
    if not -12<=args.semitones<=12 or not 0<=args.index_rate<=1 or not 0<=args.protect<=.5:raise ValueError('invalid_conversion_parameters')
    project=Path(args.project).resolve();model=Path(args.model).resolve();source=Path(args.source).resolve();output=Path(args.output).resolve()
    index=str(Path(args.index).resolve()) if args.index else ''
    if not model.is_file() or not source.is_file() or index and not Path(index).is_file():raise ValueError('conversion_resource_missing')
    os.environ.update(weight_root=str(model.parent),index_root=str(Path(index).parent) if index else str(project/'logs'),rmvpe_root=str(project/'assets/rmvpe'))
    os.chdir(project);sys.path.insert(0,str(project))
    import torch
    # HuBERT's Fairseq checkpoint stores its vocabulary object. Keep Torch's
    # weights-only loader and allow this concrete class; never disable safe
    # loading globally just to support the legacy checkpoint.
    from fairseq.data.dictionary import Dictionary
    if hasattr(torch.serialization,'add_safe_globals'):
        torch.serialization.add_safe_globals([Dictionary])
    from scipy.io import wavfile
    from infer.lib import audio as rvc_audio
    rvc_audio.load_audio=lambda file,sr: rvc_audio.np.frombuffer(decode_audio_pcm(file,sr),rvc_audio.np.float32).copy()
    from infer.modules.vc.modules import VC
    from infer.lib.rmvpe import RMVPE
    from rvc_pitch import install_windowed_rmvpe
    def pitch_progress(state):
        print(json.dumps(state),flush=True)
    pitch_options=install_windowed_rmvpe(RMVPE,args.pitch_chunk_seconds,args.pitch_overlap_seconds,pitch_progress)
    device=('cuda:0' if torch.cuda.is_available() else 'cpu') if args.device=='auto' else args.device
    if device=='cpu':torch.set_num_threads(2)
    # Bound CPU attention windows instead of materializing a full 30-second
    # float32 sequence. Training and inference still need separate memory.
    config=SimpleNamespace(device=device,is_half=device!='cpu' and args.precision=='auto',x_pad=1,x_query=3 if device=='cpu' else 6,x_center=8 if device=='cpu' else 38,x_max=10 if device=='cpu' else 41)
    vc=VC(config);vc.get_vc(model.name)
    from infer.modules.vc import modules as rvc_modules
    from rvc_windows import convert_vocal_windows
    original_loader=rvc_modules.load_audio
    waveform=rvc_audio.np.frombuffer(decode_audio_pcm(source,16000),rvc_audio.np.float32).copy()
    peak=rvc_audio.np.abs(waveform).max()/.95
    if peak>1:waveform/=peak
    def convert_part(part):
        # VC's public API reads a file. Supply only this verified local PCM
        # window for the selected source, then restore its loader immediately.
        rvc_modules.load_audio=lambda file,sr: part.copy() if str(file)==str(source) and sr==16000 else original_loader(file,sr)
        previous_max=vc.pipeline.t_max
        if conversion_options['chunk_samples']:
            vc.pipeline.t_max=len(part)+vc.pipeline.window
        try:
            status,(rate,audio)=vc.vc_single(0,str(source),args.semitones,None,'rmvpe',index,None,args.index_rate,3,0,.5,args.protect)
            if rate is None or audio is None or not status.startswith('Success'):raise RuntimeError('RVC did not produce valid audio: '+status)
            return rate,audio
        finally:
            rvc_modules.load_audio=original_loader
            vc.pipeline.t_max=previous_max
    rate,audio=convert_vocal_windows(waveform,convert_part,chunk_seconds=args.conversion_chunk_seconds,context_seconds=args.conversion_context_seconds,progress=pitch_progress)
    output.parent.mkdir(parents=True,exist_ok=True);wavfile.write(str(output),rate,audio)
    output.with_suffix('.rvc.json').write_text(json.dumps({'engine':'RVC','model':model.name,'semitones':args.semitones,'index_rate':args.index_rate,'protect':args.protect,'device':device,'precision':'float16' if config.is_half else 'float32','sample_rate':rate,'seconds':len(audio)/rate,'pitch':pitch_options,'conversion':conversion_options,'wechat_sends':0},indent=2),encoding='utf-8')
    print('RVC conversion complete; seconds',len(audio)/rate,'no messages sent',flush=True)


if __name__=='__main__':main()
