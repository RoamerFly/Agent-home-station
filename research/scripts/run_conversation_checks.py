"""Isolated model check: no WeChat sender and no real conversation memory."""
import os,pathlib,subprocess,sys
workspace=pathlib.Path(__file__).resolve().parents[2]
env=dict(os.environ,WECHAT_LIVE_WORKSPACE=str(workspace),WECHAT_LIVE_MEDIA_ROOT='/home/fly/.local/share/wechat-bot-native-research-20261024',WECHAT_CONVERSATION_LIVE='1')
binary='conversation-admin-tests';pattern='TestLiveSemanticPlanAndNewContextReview';name='goal1-step6-live-plan.log'
if '--unit'in sys.argv:binary='conversation-bot-tests';pattern='^(TestSemanticPlan|TestPlanRestart|TestPlanPolls|TestNewContext|TestSleepingPlan|TestShortMessage|TestWake|TestNewInbox|TestProgress|TestAcknowledged)';name='goal1-step6-linux-queue.log'
if '--background' in sys.argv:binary='conversation-bot-tests';pattern='^(TestDurableInboxContinues|TestNewFileInstruction|TestSlowPlanReview|TestModelCanCancelAnInFlight|TestCompletedGeneration|TestMediaCaptionJoins|TestGenerationRestart|TestAsyncSurf|TestMediaUnderstandingDoesNot|TestBackgroundProgress|TestUpdateDrainWaitsForActive|TestAuthorizedImage|TestOldResultWaitsForStop)';name='goal1-step6-linux-background.log'
if '--final' in sys.argv:binary='conversation-bot-tests';pattern='^(TestAsyncWakeRuns|TestPendingPlanDelay|TestWakeIsIndependent|TestSemanticPlan|TestShortMessage|TestPlanRestart)';name='goal1-final-linux-conversation.log'
p=subprocess.run([str(workspace/'temp'/binary),'-test.run='+pattern,'-test.v'],env=env,cwd=workspace,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
(workspace/'research/evidence'/name).write_text(p.stdout,encoding='utf8');print(p.stdout);raise SystemExit(p.returncode)
