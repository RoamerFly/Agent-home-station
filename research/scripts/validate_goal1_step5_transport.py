"""Run subprocess/GET cancellation unit regressions under the real Linux runtime."""
import pathlib,subprocess
workspace=pathlib.Path(__file__).resolve().parents[2]
result=subprocess.run([str(workspace/'temp/mcp-goal1-step5-tests'),'-test.run','^Test(StdioCancellationClosesInheritedOutputPipe|PublicGETRecoveryIsBoundedAndReportsSafeStages|PublicGETRetryWaitCancellationAndCatalogWaitDoNotHang)$','-test.v','-test.timeout','30s'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=40)
(workspace/'research/evidence/goal1-step5-linux-transport.log').write_text(result.stdout,encoding='utf8')
print(result.stdout,flush=True)
raise SystemExit(result.returncode)
