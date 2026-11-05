"""Frame-aligned RMVPE slicing; window sizes are adjustable execution defaults.

RVC's HuBERT segmentation does not segment its preceding RMVPE pass. Restrict
each pitch invocation to a contextual slice, retain only its core frame range,
and cover the complete original timeline without crossfading frequencies.
"""
import math


def pitch_parameters(chunk_seconds=10, overlap_seconds=1, sample_rate=16000, hop=160):
    for value in (chunk_seconds, overlap_seconds):
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
            raise ValueError('invalid_pitch_window')
    if isinstance(sample_rate, bool) or isinstance(hop, bool) or not isinstance(sample_rate, int) or not isinstance(hop, int) or hop <= 0 or sample_rate < hop:
        raise ValueError('invalid_pitch_frame_grid')
    if chunk_seconds and chunk_seconds < hop/sample_rate:
        raise ValueError('pitch_window_below_frame_step')
    chunk = round(chunk_seconds*sample_rate/hop) if chunk_seconds else 0
    overlap = round(overlap_seconds*sample_rate/hop)
    return {'chunk_frames': chunk, 'overlap_frames': overlap,
            'chunk_seconds': chunk*hop/sample_rate, 'overlap_seconds': overlap*hop/sample_rate,
            'frame_seconds': hop/sample_rate, 'sample_rate': sample_rate, 'hop': hop}


def pitch_windows(samples, parameters):
    if isinstance(samples, bool) or not isinstance(samples, int) or samples <= 0:
        raise ValueError('invalid_pitch_audio_length')
    hop = parameters['hop']
    total = samples//hop+1
    chunk = parameters['chunk_frames']
    if chunk == 0 or samples <= chunk*hop:
        yield {'sample_start': 0, 'sample_end': samples, 'frame_start': 0, 'frame_end': total, 'local_frame_start': 0}
        return
    overlap = parameters['overlap_frames']
    for sample in range(0, samples, chunk*hop):
        first = sample//hop
        last = total if sample+chunk*hop >= samples else first+chunk
        left = max(0, first-overlap)
        right = min(samples, (last+overlap)*hop)
        yield {'sample_start': left*hop, 'sample_end': right, 'frame_start': first,
               'frame_end': last, 'local_frame_start': first-left}


def infer_pitch_windows(audio, infer, *, chunk_seconds=10, overlap_seconds=1, progress=None):
    import numpy as np
    parameters = pitch_parameters(chunk_seconds, overlap_seconds)
    if getattr(audio, 'ndim', 1) != 1 or len(audio) == 0:
        raise ValueError('pitch_requires_mono_audio')
    windows = list(pitch_windows(len(audio), parameters))
    output = None
    for i, window in enumerate(windows):
        values = np.asarray(infer(audio[window['sample_start']:window['sample_end']]))
        count = window['frame_end']-window['frame_start']
        local = window['local_frame_start']
        if values.ndim != 1 or len(values) < local+count or not np.isfinite(values).all() or (values < 0).any():
            raise ValueError('pitch_window_result_invalid')
        if output is None:
            output = np.empty(len(audio)//parameters['hop']+1, dtype=values.dtype)
        output[window['frame_start']:window['frame_end']] = values[local:local+count]
        if progress:
            progress({'phase': 'pitch', 'completed_frames': window['frame_end'], 'total_frames': len(output),
                      'completed_windows': i+1, 'total_windows': len(windows),
                      'effective_parameters': parameters})
    return output


def install_windowed_rmvpe(cls, chunk_seconds=10, overlap_seconds=1, progress=None):
    parameters = pitch_parameters(chunk_seconds, overlap_seconds)
    original = getattr(cls, '_wechat_original_pitch_infer', cls.infer_from_audio)
    cls._wechat_original_pitch_infer = original

    def windowed(self, audio, thred=0.03):
        return infer_pitch_windows(audio, lambda part: original(self, part, thred=thred),
                                   chunk_seconds=chunk_seconds, overlap_seconds=overlap_seconds, progress=progress)

    cls.infer_from_audio = windowed
    return parameters
