"""Compare frozen/current source copies using identical production prompt fixtures.
Source roots must be disposable copies outside the checkout. Does not contact a model.
"""
import argparse,json,pathlib,subprocess,os
PROBE = r'''package companion
import("context";"encoding/json";"fmt";"os";"testing";"time";"wechat-bot/internal/mcptools")
type repairProbeModel struct{}
func(repairProbeModel) Chat(context.Context,string,[]map[string]string)(string,error){return `{"action":"silent","messages":[]}`,nil}
func(m repairProbeModel) ChatFunctions(c context.Context,s string,v []map[string]string,_ []mcptools.Tool)(string,error){return m.Chat(c,s,v)}
func TestDeepRepairComparableTokenProbe(t *testing.T){
 c:=Defaults();c.Enabled=true;c.Permissions.Remember=true;c.Permissions.ObserveGroups=true;c.Permissions.AutonomousGroups=true;c.MemoryMessages=500
 s,err:=NewStore(t.TempDir());if err!=nil{t.Fatal(err)};defer s.Close();now:=time.Now().Add(-time.Hour)
 for i:=0;i<60;i++{who:=fmt.Sprintf("member-%d",i%3);text:="今天聊工作和天气，明天再继续。";if i%3==1{text="[视觉观察] 甲的盒饭放在键盘旁边。"};in:=Input{Channel:"wechat",Account:"synthetic",Thread:"synthetic-room@chatroom",Sender:who,TraceID:fmt.Sprint(i),Text:text};if err=s.Observe(in,c,now.Add(time.Duration(i)*time.Second));err!=nil{t.Fatal(err)}}
 e,err:=New(c,s,repairProbeModel{},"synthetic base policy");if err!=nil{t.Fatal(err)};totals:=[]int{};for _,who:=range []string{"member-0","member-1","member-2"}{in:=Input{Channel:"wechat",Account:"synthetic",Thread:"synthetic-room@chatroom",Sender:who,TraceID:"current",Text:"晚安，明天见。",Mode:"social",IsGroup:true};system:=e.policy(in);turns:=e.contextTurns(s.Context(in,c,time.Now()),in);n:=EstimateTokens(system);for _,v:=range turns{n+=EstimateTokens(v["content"])+8};totals=append(totals,n)}
 raw,_:=json.Marshal(map[string]any{"version":"deep-repair-token-probe-v1","tokens":totals,"method":"production policy + selected transcript; excludes provider tokenizer and tools","wechat_sends":0});if err=os.WriteFile(os.Getenv("WECHAT_DEEP_TOKEN_OUTPUT"),raw,0600);err!=nil{t.Fatal(err)}
}
'''
def main():
 p=argparse.ArgumentParser();p.add_argument('--source-root',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
 root=a.source_root.resolve();checkout=pathlib.Path(__file__).resolve().parents[1]
 if root==checkout:raise SystemExit('use a disposable source copy, never the active checkout')
 probe=root/'internal/companion/deep_repair_comparable_probe_test.go'
 if probe.exists():raise SystemExit('probe already exists')
 a.output=a.output.resolve();a.output.parent.mkdir(parents=True,exist_ok=True)
 env=dict(os.environ,WECHAT_DEEP_TOKEN_OUTPUT=str(a.output))
 try:
  probe.write_text(PROBE,encoding='utf8')
  r=subprocess.run(['go','test','./internal/companion','-run','^TestDeepRepairComparableTokenProbe$','-count=1','-timeout','120s'],cwd=root,env=env,check=True)
 finally:probe.unlink(missing_ok=True)
 print(a.output.read_text(encoding='utf8'))
if __name__=='__main__':main()
