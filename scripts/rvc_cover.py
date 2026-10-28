"""Direct original-recording conversion, independent of YuE/Comfy generation."""
import base64
import hashlib
import math
from pathlib import Path
import subprocess
from process_window import no_console_options


def recipe(body):
    if not isinstance(body.get('title'),str) or not 0<len(body['title'])<=100:
        raise ValueError('rvc_invalid_title')
    try:
        data=base64.b64decode(body['reference_base64'],validate=True)
    except Exception:
        raise ValueError('rvc_invalid_reference') from None
    if not 0<len(data)<=25*1024*1024:
        raise ValueError('rvc_reference_size_limit')
    start,seconds=body.get('reference_start_seconds',0),body.get('reference_seconds')
    if any(isinstance(v,bool) or not isinstance(v,(int,float)) or not math.isfinite(v) for v in (start,seconds)) or start<0 or not 0<seconds<=600:
        raise ValueError('rvc_invalid_window')
    if body.get('reference_pretrimmed') is not True:
        raise ValueError('rvc_prepared_reference_required')
    return {'engine':'rvc','mode':'cover','title':body['title'],'duration_seconds':math.ceil(seconds),'reference_sha256':hashlib.sha256(data).hexdigest(),
            'reference_source_sha256':body.get('reference_source_sha256'),'reference_start_seconds':start,'reference_seconds':seconds,
            'singing_voice':'character','lyrics_source':'original_audio','melody_source':'original_audio','accompaniment_source':'original_audio'}


def convert(body,state,jid,config,profile,convert_voice):
    if profile.get('engine')!='rvc':
        raise ValueError('rvc_profile_required')
    details=recipe(body)
    state=Path(state)
    source=state/(jid+'-source.audio')
    source.write_bytes(base64.b64decode(body['reference_base64'],validate=True))
    output=state/(jid+'-rvc.wav')
    # The caller-supplied converter verifies the pinned profile hash again.
    convert_voice(config,profile,source,output)
    actual=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','default=nw=1:nk=1',str(output)],timeout=30,**no_console_options()))
    if not math.isfinite(actual) or actual<=0 or abs(actual-details['reference_seconds'])>.25:
        raise ValueError('rvc_duration_mismatch')
    return {'file':output.name,'bytes':output.stat().st_size,'duration_seconds':actual,'requested_seconds':details['reference_seconds'],
            'quality':{'duration_passed':True,'engine':'rvc','vocal_gender_verified':False,'timbre_listening_verified':False,'preserves_original_timing':True}}
