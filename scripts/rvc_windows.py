"""Convert fixed-size contextual vocal windows while retaining exact timing."""
import math


def conversion_parameters(chunk_seconds=8, context_seconds=1, sample_rate=16000):
    for value in (chunk_seconds, context_seconds):
        if isinstance(value,bool) or not isinstance(value,(int,float)) or not math.isfinite(value) or value<0:
            raise ValueError('invalid_conversion_window')
    if isinstance(sample_rate,bool) or not isinstance(sample_rate,int) or sample_rate<=0:
        raise ValueError('invalid_conversion_rate')
    core=round(chunk_seconds*sample_rate) if chunk_seconds else 0
    if chunk_seconds and core<1:raise ValueError('conversion_window_below_sample_step')
    context=round(context_seconds*sample_rate)
    return {'chunk_samples':core,'context_samples':context,'sample_rate':sample_rate,
            'chunk_seconds':core/sample_rate,'context_seconds':context/sample_rate}


def convert_vocal_windows(audio, convert, *, chunk_seconds=8, context_seconds=1, progress=None):
    import numpy as np
    options=conversion_parameters(chunk_seconds,context_seconds)
    if getattr(audio,'ndim',1)!=1 or len(audio)==0 or not np.isfinite(audio).all():raise ValueError('conversion_requires_finite_mono_audio')
    rate=options['sample_rate'];core=options['chunk_samples'];context=options['context_samples']
    if core==0:
        return convert(audio)
    results=[];target_rate=None;total=math.ceil(len(audio)/core)
    for i,start in enumerate(range(0,len(audio),core)):
        end=min(len(audio),start+core)
        left=max(0,start-context);right=min(len(audio),end+context)
        pad_left=max(0,context-start)
        # The last window uses the SAME inference tensor size, not a new
        # large convolution workspace for every music-dependent split shape.
        pad_right=core+2*context-(right-left)-pad_left
        part=np.pad(audio[left:right],(pad_left,pad_right),mode='reflect' if right-left>1 else 'edge')
        output_rate,converted=convert(part)
        converted=np.asarray(converted)
        if isinstance(output_rate,bool) or not isinstance(output_rate,(int,np.integer)) or output_rate<=0 or converted.ndim!=1 or not np.isfinite(converted).all():
            raise ValueError('conversion_window_result_invalid')
        if target_rate is None:target_rate=int(output_rate)
        if target_rate!=output_rate:raise ValueError('conversion_window_sample_rate_changed')
        offset=round(context*target_rate/rate)
        count=round(end*target_rate/rate)-round(start*target_rate/rate)
        if len(converted)<offset+count:raise ValueError('conversion_window_output_incomplete')
        # Copy only real core samples, never the contextual/padded tail. No
        # repeated chorus, tempo resampling or synthetic final voice segment.
        results.append(converted[offset:offset+count].copy())
        if progress:progress({'phase':'conversion','completed_samples':end,'total_samples':len(audio),
                              'completed_windows':i+1,'total_windows':total,'effective_parameters':options})
    return target_rate,np.concatenate(results)
