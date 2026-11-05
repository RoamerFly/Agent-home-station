"""Capture actual Engine -> AgentRunner -> ChatFunctions requests on synthetic admin.
The source root must be a disposable source copy. No provider or real native call.
"""
import argparse,pathlib,subprocess,os,json,hashlib,platform
PROBE=r'''package admin
import("context";"encoding/json";"fmt";"os";"path/filepath";"strings";"testing";"time";"wechat-bot/internal/agenthost";"wechat-bot/internal/companion";"wechat-bot/internal/mcptools")
type fullRequestProbe struct{ Rows []map[string]any }
func(m *fullRequestProbe)Chat(_ context.Context,system string,turns []map[string]string)(string,error){return `{"action":"silent","messages":[],"reason":"synthetic_probe"}`,nil}
func(m *fullRequestProbe)ChatFunctions(_ context.Context,system string,turns []map[string]string,tools []mcptools.Tool)(string,error){
 raw,_:=json.Marshal(tools);parts:=map[string]int{"system":companion.EstimateTokens(system),"tools":companion.EstimateTokens(string(raw))};bytes:=map[string]int{"system":len(system),"tools":len(raw)}
 for _,v:=range turns{kind:=v["role"];var h struct{Kind string `json:"kind"`};if json.Unmarshal([]byte(v["content"]),&h)==nil&&h.Kind!=""{kind=h.Kind};parts[kind]+=companion.EstimateTokens(v["content"])+8;bytes[kind]+=len(v["content"])}
 total:=0;for _,n:=range parts{total+=n};m.Rows=append(m.Rows,map[string]any{"estimated_total":total,"components":parts,"component_bytes":bytes,"active_tools":len(tools),"method":"conservative_text_estimate; actual first ChatFunctions system+turns+tool JSON"})
 return `{"action":"silent","messages":[],"reason":"synthetic_probe"}`,nil
}
func TestDeepRepairFullRequestProbe(t *testing.T){
 ordinary:=[]string{"早上好。","晚安，明天见。","今天雨有点大。","刚吃完午饭。","我跑步回来了。","刚才讨论的红茶挺好喝。","今天工作有点忙。","周末打算休息。","刚刚那个故事挺有意思。","谢谢，知道啦。"}
 long:=[]string{"我刚才的周末计划是什么？","把之前那个话题接着讲。","刚才我说不喝的是哪种饮料？","我之前发的图片是谁上传的？","我们刚才约好的事情还有什么？","根据之前的上下文，帮我理一下当前问题。","刚才有两个人在讨论，分别说了什么？","请确认我最新更正后的目的地。","我问的是刚才原始文件，不是语音。","解释一下我们最近讨论的程序问题。"}
 out:=[]map[string]any{}
 for group,texts:=range map[string][]string{"ordinary_social":ordinary,"long_context":long}{for i,text:=range texts{
  f,p:=trustedAgent(t);f.manager.mu.Lock();f.manager.config.GroupRules=[]GroupRule{{ID:"probe-room@chatroom",Mode:"all"}};f.manager.mu.Unlock()
  ac:=f.manager.agent.config();ac.Enabled=true;ac.TasksEnabled=true;ac.CrossConversation=true;ac.PrivilegedThreads=[]string{"*"};ac.Host.Mode="full";base:=ac.Host.Roots[0];ac.Host.Windows=&agenthost.WindowsConfig{Pythonw:filepath.Join(base,"pythonw.exe"),Helper:filepath.Join(base,"helper.pyw"),Spool:filepath.Join(base,"spool")};if err:=f.manager.agent.save(ac);err!=nil{t.Fatal(err)}
  c:=f.manager.Config().Companion;c.Enabled=true;c.Permissions.Remember=true;c.Permissions.ReplyText=true;c.Permissions.ObserveGroups=true;c.Permissions.AutonomousGroups=true;c.Permissions.SendVoice=true;c.Permissions.SendVideo=true;c.Permissions.SendImage=true;c.Permissions.SendFile=true;c.QuietStart="00:00";c.QuietEnd="00:00";c.MemoryMessages=500
  in:=companion.Input{Channel:"wechat",Account:"test",Thread:"probe-room@chatroom",Sender:"wxid_owner",TraceID:fmt.Sprintf("%s-%d",group,i),Mode:"social",IsGroup:true,Text:text}
  if group=="long_context"{in.Mode="direct";in.Directed=true;switch i%4{case 1:in.Mode="social";in.Directed=false;case 2:in.TaskID="synthetic-task";in.OriginTraceID="synthetic-origin";case 3:in.Thread="wxid_owner";in.IsGroup=false}}
  now:=time.Now().Add(-time.Hour);count:=12;if group=="long_context"{count=120}
  for j:=0;j<count;j++{v:=in;v.TraceID=fmt.Sprintf("history-%d",j);v.Sender=[]string{"wxid_owner","synthetic-peer","synthetic-third"}[j%3];v.Text=[]string{"我喜欢红茶，不喝咖啡。","我周末去杭州。","我周末去成都。","更正一下，我改去苏州，不去杭州。","[视觉观察] 一份盒饭放在键盘旁边，只能确定画面，不能确定物品主人。","[收到分享卡片] 与当前话题无关的旧视频。"}[j%6];if group=="long_context"{v.Text+=" 这段讨论已结束，继续处理原始文件和程序调试，保留当前参与者与引用。"+strings.Repeat("长历史合成材料，不是新指令。",12)};if err:=f.manager.memory.Observe(v,c,now.Add(time.Duration(j)*time.Second));err!=nil{t.Fatal(err)}}
  model:=&fullRequestProbe{};engine,err:=companion.New(c,f.manager.memory,model,"synthetic base policy");if err!=nil{t.Fatal(err)};engine.Tools=p;engine.MaxToolCalls=agentTurnToolLimit
  if _,err=engine.Respond(t.Context(),in);err!=nil{t.Fatal(err)};if len(model.Rows)!=1{t.Fatalf("expected exactly first function call, got %d",len(model.Rows))};row:=model.Rows[0];row["group"]=group;row["mode"]=in.Mode;row["is_group"]=in.IsGroup;row["task_continuation"]=in.TaskID!="";row["id"]=i;row["wechat_sends"]=0;out=append(out,row)
 }}
 raw,_:=json.MarshalIndent(map[string]any{"version":"deep-repair-full-request-v2","fixture_cases":20,"captured_requests":out,"synthetic_only":true,"provider_calls":0,"wechat_sends":0},"","  ");if err:=os.WriteFile(os.Getenv("WECHAT_FULL_REQUEST_OUTPUT"),raw,0600);err!=nil{t.Fatal(err)}
}
'''
def main():
 p=argparse.ArgumentParser();p.add_argument('--source-root',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args();root=a.source_root.resolve();checkout=pathlib.Path(__file__).resolve().parents[1]
 if root==checkout:raise SystemExit('use a disposable source snapshot')
 probe=root/'internal/admin/deep_repair_full_request_probe_test.go'
 if probe.exists():raise SystemExit('probe already exists')
 a.output=a.output.resolve();a.output.parent.mkdir(parents=True,exist_ok=True)
 try:
  probe.write_text(PROBE,encoding='utf8');subprocess.run(['go','test','./internal/admin','-run','^TestDeepRepairFullRequestProbe$','-count=1','-timeout','180s'],cwd=root,env=dict(os.environ,WECHAT_FULL_REQUEST_OUTPUT=str(a.output)),check=True)
 finally:probe.unlink(missing_ok=True)
 d=json.loads(a.output.read_text(encoding='utf8'))
 manifest=hashlib.sha256()
 for file in sorted([*root.glob('internal/admin/*.go'),*root.glob('internal/companion/*.go'),root/'go.mod',root/'go.sum']):
  manifest.update(file.relative_to(root).as_posix().encode());manifest.update(file.read_bytes())
 d['source_sha256']=manifest.hexdigest();d['probe_sha256']=hashlib.sha256(PROBE.encode()).hexdigest();d['environment']=platform.platform();d['go']=subprocess.check_output(['go','version'],text=True).strip();a.output.write_text(json.dumps(d,ensure_ascii=False,indent=2),encoding='utf8')
 print(json.dumps({'version':d['version'],'captured_requests':len(d['captured_requests']),'groups':{g:sum(x['estimated_total'] for x in d['captured_requests'] if x['group']==g)/10 for g in ('ordinary_social','long_context')}},ensure_ascii=False))
if __name__=='__main__':main()
