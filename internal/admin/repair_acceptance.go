package admin

// Controlled acceptance requests originate from the authenticated management
// endpoint, not an invented WeChat packet. Real tasks/tools and normal private
// WeChat delivery are used; fixture source and verification stay isolated.
import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

const repairSource = `from decimal import Decimal, ROUND_HALF_UP

def quote(subtotal, discount_percent=0):
    net = (Decimal(str(subtotal)) * (1 - Decimal(str(discount_percent)) / 100)).quantize(Decimal('0.01'), rounding=ROUND_HALF_UP)
    shipping = Decimal('0') if net > Decimal('100') else Decimal('6')
    return {'goods': format(net, '.2f'), 'shipping': format(shipping, '.2f'), 'total': format(net + shipping, '.2f')}
`
const repairReceipt = `from decimal import Decimal

def describe(quote):
    return '实付' + str(int(Decimal(quote['total']) * 100)) + '元'
`
const repairReadme = `# 独立报价测试项目

只用于管理端发起的自主修复验收，与机器人业务和真实订单无关。
shipping.py 的 quote(subtotal, discount_percent=0) 返回 goods/shipping/total 三个两位小数字符串。
折扣参数表示优惠百分比，例如 10 表示九折。优惠后的商品金额使用 Decimal 四舍五入到分。
优惠后的商品金额满 100 元（含 100）免运费，不满 100 元收 6 元运费。
验证：在此目录运行 python3 -m unittest discover -s tests -v。
只修复本目录业务源码，不能修改 tests。测试目录与业务源码都是合成素材；不要触及其他项目、密钥或服务。
`
const repairChecks1 = `import unittest
from shipping import quote
class QuoteTests(unittest.TestCase):
    def test_exact_threshold(self): self.assertEqual(quote('100')['shipping'], '0.00')
    def test_discount_threshold(self): self.assertEqual(quote('125',20)['total'], '100.00')
    def test_below_threshold(self): self.assertEqual(quote('99.99')['total'], '105.99')
    def test_rounding(self): self.assertEqual(quote('10.05',10)['goods'], '9.05')
`
const repairChecks2 = `import unittest
from shipping import quote
from receipt import describe
class ReceiptTests(unittest.TestCase):
    def test_yuan_not_fen(self): self.assertEqual(describe(quote('99.99')), '实付105.99元')
    def test_integer(self): self.assertEqual(describe(quote('100')), '实付100.00元')
`
const repairGrader = `import sys,json
sys.path.insert(0,sys.argv[1])
from shipping import quote
from decimal import Decimal,ROUND_HALF_UP
n=0
for amount,discount in [('0',0),('99.99',0),('100',0),('100.01',0),('125',20),('200',50),('10.05',10),('100.005',0),('99.995',0),('150',33.33),('98.05',10),('1000',100)]:
    goods=(Decimal(amount)*(1-Decimal(str(discount))/100)).quantize(Decimal('.01'),rounding=ROUND_HALF_UP)
    shipping=Decimal(0) if goods>=100 else Decimal(6)
    expected=dict(goods=format(goods,'.2f'),shipping=format(shipping,'.2f'),total=format(goods+shipping,'.2f'))
    assert quote(amount,discount)==expected,(amount,discount)
    n+=1
if int(sys.argv[2])>=2:
    from receipt import describe
    for amount in ['0','99.99','100','200.05']:
        q=quote(amount)
        assert describe(q)=='实付'+q['total']+'元'
        n+=1
print(json.dumps({'passed':n}))
`

type repairTestRun struct {
	ProgramFile    string            `json:"program_file,omitempty"`
	ProgramGuide   string            `json:"program_guide,omitempty"`
	ID             string            `json:"id"`
	Project        string            `json:"project"`
	Recipient      string            `json:"recipient"`
	Stage          int               `json:"stage"`
	TaskIDs        []string          `json:"task_ids"`
	TestHashes     map[string]string `json:"test_hashes"`
	InitialHashes  map[string]string `json:"initial_hashes"`
	BaselineFailed bool              `json:"baseline_failed"`
	FollowupFailed bool              `json:"followup_baseline_failed"`
}

func (m *Manager) repairRunPath(id string) string {
	return filepath.Join(m.dir, "repair-test-runs", id+".json")
}
func validRepairRun(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil
}
func (m *Manager) loadRepairRun(id string) (repairTestRun, error) {
	var v repairTestRun
	if !validRepairRun(id) {
		return v, errors.New("测试编号无效")
	}
	b, e := os.ReadFile(m.repairRunPath(id))
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	if e == nil && (v.ID != id || !validRepairRun(v.ID) || v.Recipient == "") {
		e = errors.New("测试记录身份无效，原文件保留")
	}
	return v, e
}
func (m *Manager) saveRepairRun(v repairTestRun) error {
	if e := os.MkdirAll(filepath.Dir(m.repairRunPath(v.ID)), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return writePrivate(m.repairRunPath(v.ID), b)
}
func repairPython() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}
func (m *Manager) repairTask(v repairTestRun) agentTask {
	id := agentID("admin-repair-test", v.ID, string(rune('0'+v.Stage)))
	request := "报价计算有问题：满100元却仍加了运费，优惠后的门槛也不对。请自行查看项目说明和源码，复现、修复并运行测试验证。"
	if v.Stage == 2 {
		request = "刚刚的计算修复后，我看报价文字还是不对：99.99元的商品加运费应显示实付105.99元，却变成10599元。请结合上一轮继续排查、修复并重新验证，不只回答建议。"
	}
	if v.Stage == 3 {
		return agentTask{ID: id, Account: m.native.Account(), Thread: v.Recipient, Sender: v.Recipient, OwnerRequested: true, Title: "本机文件修复与Windows进程启停验收", State: "pending", Next: time.Now(), OriginTrace: "admin-repair-test-" + v.ID, Checkpoint: "这是主人通过管理端授权的本机实操验收，不是真实微信入站。请真实修复项目文件并启动、关闭本机Windows程序，不能只说明步骤。先读取指南 " + v.ProgramGuide + " 和源码 " + v.ProgramFile + "，按指南复现故障，自行修改源码并验证，随后启动本次诊断程序，确认真实PID、Windows平台和健康端口。启动后请将当前任务设为waiting等待30秒再继续关闭，便于独立观察器采样；等待时可私聊反馈启动事实。下一轮必须实际关闭本次程序，确认PID退出和端口关闭，保存本会话REPAIR-TEST-" + v.ID + ".md，最后agent_task标记completed并私聊反馈。只改本次脚本，不修改其他项目、权限、测试或登录配置，不启停机器人、YuE等其他程序。当前仓库由主维护者统一验收提交，不在本次子操作中commit/push。所有微信反馈仅发当前主人私聊，带【自修复测试】。"}
	}
	return agentTask{ID: id, Account: m.native.Account(), Thread: v.Recipient, Sender: v.Recipient, OwnerRequested: true, Title: "代码自主修复验收", State: "pending", Next: time.Now(), OriginTrace: "admin-repair-test-" + v.ID, Checkpoint: "这是主人通过已认证管理端代发的合成测试请求，不是真实微信入站。测试项目：" + v.Project + "。\n" + request + "\n只操作这个独立测试项目，先读取README。不要改tests或其他项目，不调用微信跨会话工具。请自主选择实际工具执行；如遇命令错误自行修正。通过后把本轮问题、改动和验证事实保存为本会话REPAIR-TEST-" + v.ID + ".md，已有笔记先读revision并保留上轮内容。用agent_task将当前任务标记completed，最后私聊反馈真实结果，并明确带上【自修复测试】。"}
}
func (m *Manager) startRepairRun(ctx context.Context, recipient string) (repairTestRun, error) {
	var v repairTestRun
	cfg := m.agent.config()
	access := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: recipient, Sender: recipient}}
	if !cfg.TasksEnabled || cfg.Host.Mode != "full" || !access.privileged() || !m.Config().Companion.Enabled || !m.Config().Companion.Permissions.ReplyText {
		return v, errors.New("需要已配置的主人私聊、运行中的Agent及服务器执行权限；测试不会提升权限")
	}
	if _, e := access.scopedHostConfig(json.RawMessage(`{}`)); e != nil {
		return v, e
	}
	conversation := access.executionContext().Conversation
	base := filepath.Join(conversation, "scratch", "repair-tests")
	anchor, e := os.OpenRoot(conversation)
	if e != nil {
		return v, e
	}
	defer anchor.Close()
	if e = anchor.MkdirAll(filepath.Join("scratch", "repair-tests"), 0700); e != nil {
		return v, e
	}
	v = repairTestRun{ID: randomToken(), Recipient: recipient, Stage: 1, TestHashes: map[string]string{}, InitialHashes: map[string]string{}}
	v.Project = filepath.Join(base, v.ID)
	parent, e := anchor.OpenRoot(filepath.Join("scratch", "repair-tests"))
	if e != nil {
		return v, e
	}
	defer parent.Close()
	if e = parent.Mkdir(v.ID, 0700); e != nil {
		return v, e
	}
	root, e := parent.OpenRoot(v.ID)
	if e != nil {
		return v, e
	}
	defer root.Close()
	if e = root.Mkdir("tests", 0700); e != nil {
		return v, e
	}
	for name, body := range map[string]string{"README.md": repairReadme, "shipping.py": repairSource, "receipt.py": repairReceipt, "tests/test_quote.py": repairChecks1} {
		if e := createRepairFixture(root, name, body); e != nil {
			return v, e
		}
		v.InitialHashes[name] = repairFileHash(root, name)
	}
	v.TestHashes["tests/test_quote.py"] = v.InitialHashes["tests/test_quote.py"]
	code, _, e := m.runRepairProgram(ctx, v, "tests")
	if e != nil {
		return v, e
	}
	v.BaselineFailed = code != 0
	if !v.BaselineFailed {
		return v, errors.New("测试故障未复现，未安排修复")
	}
	task := m.repairTask(v)
	v.TaskIDs = append(v.TaskIDs, task.ID)
	if e := m.saveRepairRun(v); e != nil {
		return v, e
	}
	return v, m.agent.task(task)
}
func (s *Server) repairTestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/repair-tests/{id}/lifecycle", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ProgramFile  string `json:"program_file"`
			ProgramGuide string `json:"program_guide"`
			Restart      bool   `json:"restart"`
		}
		if !decode(w, r, &in) {
			return
		}
		m := s.manager
		v, e := m.loadRepairRun(r.PathValue("id"))
		if e != nil {
			fail(w, 404, "测试不存在")
			return
		}
		if v.Stage == 3 && !in.Restart {
			respond(w, 200, v)
			return
		}
		if v.Stage != 2 && v.Stage != 3 {
			fail(w, 409, "请先完成追问阶段")
			return
		}
		for _, t := range m.agent.list(m.native.Account(), v.Recipient) {
			for _, id := range v.TaskIDs {
				if t.ID == id && t.State == "running" {
					fail(w, 409, "上一阶段仍在运行")
					return
				}
			}
		}
		access := &permittedTools{manager: m, trace: companion.Input{Account: m.native.Account(), Thread: v.Recipient, Sender: v.Recipient}}
		if !access.privileged() || m.agent.config().Host.Mode != "full" {
			fail(w, 403, "沿用现有主人执行权限，未提升权限")
			return
		}
		for _, path := range []string{in.ProgramFile, in.ProgramGuide} {
			body, _ := json.Marshal(map[string]string{"path": path})
			if _, e := access.Call(r.Context(), "server_read", body); e != nil {
				fail(w, 400, "需要已授权根目录中的真实脚本和指南")
				return
			}
		}
		v.Stage = 3
		v.ProgramFile = in.ProgramFile
		v.ProgramGuide = in.ProgramGuide
		task := m.repairTask(v)
		if in.Restart {
			for _, old := range m.agent.list(m.native.Account(), v.Recipient) {
				for _, id := range v.TaskIDs {
					if old.ID == id && old.Title == "本机文件修复与Windows进程启停验收" && old.State != "completed" {
						old.State = "cancelled"
						if e := m.agent.task(old); e != nil {
							fail(w, 500, "旧验收任务取消失败")
							return
						}
					}
				}
			}
			task.ID = agentID(task.ID, randomToken())
		}
		v.TaskIDs = append(v.TaskIDs, task.ID)
		if e = m.saveRepairRun(v); e == nil {
			e = m.agent.task(task)
		}
		if e != nil {
			fail(w, 500, "任务保存失败")
			return
		}
		respond(w, 202, v)
	}))
	mux.HandleFunc("POST /api/agent/repair-tests", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Recipient string `json:"recipient"`
		}
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		v, e := s.manager.startRepairRun(ctx, in.Recipient)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		s.manager.record(bot.Record{Event: "admin_repair_test_created", Category: "decision", ThreadID: in.Recipient, Status: "pending", Reason: "synthetic_admin_request_not_wechat_inbound", Origin: "admin_test"})
		respond(w, 202, v)
	}))
	mux.HandleFunc("POST /api/agent/repair-tests/{id}/followup", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		v, e := m.loadRepairRun(r.PathValue("id"))
		if e != nil {
			fail(w, 404, "测试不存在")
			return
		}
		if v.Stage != 1 {
			respond(w, 200, v)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		root, e := m.openRepairProject(v)
		if e != nil {
			fail(w, 400, "测试工作区不可执行；旧记录保留，请使用受管会话创建验收")
			return
		}
		defer root.Close()
		for name, hash := range v.TestHashes {
			if repairFileHash(root, name) != hash {
				fail(w, 409, "测试被修改，验收拒绝")
				return
			}
		}
		code, _, e := m.runRepairProgram(ctx, v, "tests")
		if e != nil {
			fail(w, 503, "隔离验收执行器未完成，未安排下一阶段")
			return
		}
		if code != 0 {
			fail(w, 409, "第一轮验证尚未通过")
			return
		}
		for _, t := range m.agent.list(m.native.Account(), v.Recipient) {
			if t.ID == v.TaskIDs[0] && t.State == "running" {
				fail(w, 409, "第一轮仍在执行")
				return
			}
		}
		if e = createRepairFixture(root, "tests/test_receipt.py", repairChecks2); e != nil {
			fail(w, 500, "测试文件写入失败")
			return
		}
		v.TestHashes["tests/test_receipt.py"] = repairFileHash(root, "tests/test_receipt.py")
		f, e := root.OpenFile("README.md", os.O_APPEND|os.O_WRONLY|repairNonblockingFlag, 0600)
		if e != nil {
			fail(w, 500, "测试说明写入失败")
			return
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			f.Close()
			fail(w, 409, "测试说明不是普通文件，原文件保留")
			return
		}
		_, e = f.WriteString("\n追问：receipt.describe(quote) 应返回 实付{total}元，保留两位小数，单位为元，不能把分值当成元。\n")
		f.Close()
		if e != nil {
			fail(w, 500, "测试说明写入失败")
			return
		}
		code, _, e = m.runRepairProgram(ctx, v, "tests")
		if e != nil {
			fail(w, 503, "隔离验收执行器未完成，原阶段保留")
			return
		}
		v.FollowupFailed = code != 0
		v.Stage = 2
		task := m.repairTask(v)
		v.TaskIDs = append(v.TaskIDs, task.ID)
		if e = m.saveRepairRun(v); e == nil {
			e = m.agent.task(task)
		}
		if e != nil {
			fail(w, 500, "测试任务保存失败")
			return
		}
		respond(w, 202, v)
	}))
	mux.HandleFunc("GET /api/agent/repair-tests/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		m := s.manager
		v, e := m.loadRepairRun(r.PathValue("id"))
		if e != nil {
			fail(w, 404, "测试不存在")
			return
		}
		root, e := m.openRepairProject(v)
		if e != nil {
			fail(w, 400, "测试工作区不可执行；旧记录保留，请使用受管会话创建验收")
			return
		}
		defer root.Close()
		unchanged := true
		for name, hash := range v.TestHashes {
			unchanged = unchanged && repairFileHash(root, name) == hash
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		code, output, runErr := m.runRepairProgram(ctx, v, "tests")
		if runErr != nil {
			fail(w, 503, "隔离验收执行器未完成，未生成通过结论")
			return
		}
		gradeCode, graderOutput, gradeErr := m.runRepairProgram(ctx, v, string(rune('0'+v.Stage)))
		states := []map[string]any{}
		for _, t := range m.agent.list(m.native.Account(), v.Recipient) {
			for _, id := range v.TaskIDs {
				if t.ID == id {
					states = append(states, map[string]any{"id": t.ID, "state": t.State, "runs": t.Runs})
				}
			}
		}
		sourceChanged := false
		for _, name := range []string{"shipping.py", "receipt.py"} {
			sourceChanged = sourceChanged || repairFileHash(root, name) != v.InitialHashes[name]
		}
		if len(graderOutput) > 1000 {
			graderOutput = graderOutput[len(graderOutput)-1000:]
		}
		tools := []string{}
		for _, op := range m.agent.operations() {
			if strings.Contains(string(op.Arguments), filepath.ToSlash(v.Project)) || strings.Contains(string(op.Arguments), strings.ReplaceAll(v.Project, `\`, `\\`)) {
				tools = append(tools, op.Tool)
			}
		}
		note, _ := m.workspace.Read(companion.ScopeID("wechat", m.native.Account(), v.Recipient), "REPAIR-TEST-"+v.ID+".md")
		respond(w, 200, map[string]any{"run": v, "tests_passed": code == 0, "tests_unchanged": unchanged, "source_changed": sourceChanged, "independent_grader_passed": gradeErr == nil && gradeCode == 0, "grader_output": graderOutput, "test_output": output, "tasks": states, "observed_project_tools": tools, "memory_saved": note.Revision != "", "execution": "production_agent_task", "system_isolated": true, "inbound": "synthetic_authenticated_admin_request", "wechat_delivery": "inspect_message_receipts_separately"})
	}))
}
