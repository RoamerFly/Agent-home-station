"""Convert contextual vocal windows with time-aligned seam blending."""
import math


def conversion_parameters(chunk_seconds=8, context_seconds=1, crossfade_seconds=.08, sample_rate=16000):
    for value in (chunk_seconds, context_seconds, crossfade_seconds):
        if isinstance(value,bool) or not isinstance(value,(int,float)) or not math.isfinite(value) or value<0:
            raise ValueError('invalid_conversion_window')
    if isinstance(sample_rate,bool) or not isinstance(sample_rate,int) or sample_rate<=0:
        raise ValueError('invalid_conversion_rate')
    core=round(chunk_seconds*sample_rate) if chunk_seconds else 0
    if chunk_seconds and core<1:raise ValueError('conversion_window_below_sample_step')
    context=round(context_seconds*sample_rate)
    fade=round(crossfade_seconds*sample_rate)
    if core and fade and (fade>context or fade*2>core):raise ValueError('conversion_crossfade_outside_context')
    return {'chunk_samples':core,'context_samples':context,'crossfade_samples':fade,'sample_rate':sample_rate,
            'chunk_seconds':core/sample_rate,'context_seconds':context/sample_rate,'crossfade_seconds':fade/sample_rate}


def convert_vocal_windows(audio, convert, *, chunk_seconds=8, context_seconds=1, crossfade_seconds=.08, progress=None):
    import numpy as np
    options=conversion_parameters(chunk_seconds,context_seconds,crossfade_seconds)
    if getattr(audio,'ndim',1)!=1 or len(audio)==0 or not np.isfinite(audio).all():raise ValueError('conversion_requires_finite_mono_audio')
    rate=options['sample_rate'];core=options['chunk_samples'];context=options['context_samples'];fade=options['crossfade_samples']
    if core==0:
        return convert(audio)
    results=[];target_rate=None;total=math.ceil(len(audio)/core)
    mixed=weights=original_dtype=None
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
        write_start=max(0,start-fade) if i else 0
        write_end=min(len(audio),end+fade) if i+1<total else len(audio)
        output_start=round(write_start*target_rate/rate)
        output_end=round(write_end*target_rate/rate)
        offset=round((write_start-start+context)*target_rate/rate)
        count=output_end-output_start
        if len(converted)<offset+count:raise ValueError('conversion_window_output_incomplete')
        if fade==0:
            results.append(converted[offset:offset+count].copy())
        else:
            if mixed is None:
                mixed=np.zeros(round(len(audio)*target_rate/rate),dtype=np.float64)
                weights=np.zeros(len(mixed),dtype=np.float64)
                original_dtype=converted.dtype
            segment=converted[offset:offset+count].astype(np.float64)
            blend=np.ones(count,dtype=np.float64)
            if i:
                fade_end=min(count,round((start+fade)*target_rate/rate)-output_start)
                blend[:fade_end]=np.linspace(0,1,fade_end,endpoint=False)
            if i+1<total:
                fade_start=max(0,round((end-fade)*target_rate/rate)-output_start)
                blend[fade_start:]=np.linspace(1,0,count-fade_start,endpoint=False)
            mixed[output_start:output_end]+=segment*blend
            weights[output_start:output_end]+=blend
        if progress:progress({'phase':'conversion','completed_samples':end,'total_samples':len(audio),
                              'completed_windows':i+1,'total_windows':total,'effective_parameters':options})
    if fade==0:return target_rate,np.concatenate(results)
    if np.any(weights<=0):raise ValueError('conversion_crossfade_gap')
    output=mixed/weights
    if np.issubdtype(original_dtype,np.integer):
        bounds=np.iinfo(original_dtype)
        output=np.clip(np.rint(output),bounds.min,bounds.max)
    return target_rate,output.astype(original_dtype)
