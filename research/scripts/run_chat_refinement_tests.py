"""Run only the isolated live social/music regression, with no WeChat sends."""
import os,pathlib,subprocess,sys
workspace=pathlib.Path(__file__).resolve().parents[2]
env=dict(os.environ)
env.update(WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_CHAT_REFINEMENT='1')
pattern=os.environ.get('WECHAT_REFINEMENT_PATTERN','^(TestLiveRestrainedSocialAndFreshNamedSong|TestPublicVideoAudioExtractionProducesReusableAudioOnlyFile)$')
if '--file-intent' in sys.argv:
    pattern='^(TestLiveQuotedModelChoosesOriginalFileAndEmitsCancellation|TestLiveModelReusesPriorVoiceAsFileWithoutSearch|TestMusicPreviewRetainsCompleteOriginalForFileReuse|TestPublicVideoAudioExtractionProducesReusableAudioOnlyFile)$'
result=subprocess.run([str(workspace/'temp/chat-refinement-tests'),'-test.run='+pattern,'-test.v'],env=env,cwd=workspace,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
filename='named-song-audio-live.log'if pattern=='TestLiveNamedSongAudioFromVerifiedPublicPage'else'chat-refinement-live.log'
if '--file-intent' in sys.argv: filename='quoted-file-preview-live.log'
(workspace/'research/evidence'/filename).write_text(result.stdout,encoding='utf8')
print(result.stdout)
raise SystemExit(result.returncode)
