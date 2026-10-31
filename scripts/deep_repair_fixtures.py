"""Versioned synthetic behavior catalogue. No personal or native account data."""
import json
from pathlib import Path
GROUPS = {
 "people": ["father-addressed", "father-ambiguous", "same-name", "renamed", "third-party-report", "unsupported-emotion", "assistant-not-source", "quoted-other-person"],
 "media": ["meal-then-goodnight", "same-caption-two-uploads", "photo-not-ownership", "late-observation", "quoted-old-image", "failed-observation", "video-range", "voice-uploader"],
 "conversation": ["other-speaker-source", "same-speaker-source", "explicit-quote", "unrelated-mention", "cross-thread-source", "cross-account-source", "assistant-source", "missing-anchor"],
 "restart": ["sender-stable", "quoted-source-stable", "incoming-media-stable", "reply-target-stable", "occurred-time-stable", "event-id-stable", "duplicate-idempotent", "name-not-identity"],
 "memory": ["correct", "forget", "other-subject-rejected", "missing-source-rejected", "wrong-quote-rejected", "scope-isolated", "revision-conflict", "pinned-forget"],
 "identity": ["namespace-distinct", "server-collision", "time-collision", "sender-collision", "body-collision", "explicit-revision", "late-independent", "duplicate-idempotent"],
}
def generate():
 rows=[]
 for group,behaviors in GROUPS.items():
  for variant,behavior in enumerate(behaviors):
   for permutation in range(5):
    a,b=("member-alpha","member-beta") if permutation%2==0 else ("member-beta","member-alpha")
    rows.append({"id":f"{group}-{variant:02d}-{permutation}","group":group,"behavior":behavior,"permutation":permutation,"actor":a,"other":b,"display_name":"小林" if permutation==2 else "同学","request":"你爸刚视频，刚挂掉" if group=="people" else "晚安咯" if group=="media" else "请结合来源回答当前问题","forbidden_transfer":["盒饭","汤洒键盘","心里有点空","站那儿"],"must_respond":behavior in ("same-speaker-source","explicit-quote","father-addressed")})
 assert len(rows)==240
 return {"version":"deep-repair-v1","purpose":"behavior regression; not evidence of real-model quality", "cases":rows}
if __name__=="__main__":
 p=Path(__file__).resolve().parents[1]/"internal/companion/testdata/deep_repair/scenarios.json"
 p.parent.mkdir(parents=True,exist_ok=True)
 p.write_text(json.dumps(generate(),ensure_ascii=False,indent=2)+"\n",encoding="utf8")
 print(p,240)
