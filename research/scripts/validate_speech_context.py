import argparse,os,subprocess,json
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
env=dict(os.environ,WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_SPEECH_CONTEXT_LIVE='1')
p=argparse.ArgumentParser();p.add_argument('--only',default='');args=p.parse_args()
if args.only in ('Followup','Completion'):env['WECHAT_MUSIC_FOLLOWUP_LIVE']='1'
if args.only=='Duration':env['WECHAT_MUSIC_DURATION_LIVE']='1'
failed=[]
for name,pattern in [('bailian-roundtrip.log','TestLiveBailianSpeechRoundTrip'),('native-functions-reference.log','TestLiveNativeFunctionsResolveRecentOriginalVideo'),('live-model-tts-voice.log','TestLiveModelSynthesizesAndPreparesWechatVoice'),('live-context-compression.log','TestLiveStructuredContextCompression'),('live-yue-file-staging.log','TestLiveCompletedYuESongStagesOriginalFile'),('music-followup-live.log','TestLiveStyleConfirmationStartsYuEAndReturnsCompletedSong'),('music-followup-completion.log','TestLiveCompletedMusicContinuationSelectsStagedMedia'),('music-duration-live.log','TestLiveMusicOneMinuteControlsDurationAndDefaultsToCompleteVoice')]:
    if args.only and (args.only not in pattern and not(args.only=='Followup' and 'StyleConfirmation' in pattern) and not(args.only=='Completion' and 'ContinuationSelects' in pattern)):continue
    r=subprocess.run([str(workspace/'temp/admin-speech-context-tests'),'-test.v','-test.run='+pattern],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=240)
    (workspace/'research/evidence'/name).write_bytes(r.stdout)
    print(name,'PASS' if r.returncode==0 else 'FAIL',flush=True)
    if r.returncode:print(r.stdout.decode(errors='replace')[-5000:]);failed.append(name)
raise SystemExit(bool(failed))
