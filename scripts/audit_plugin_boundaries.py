"""Read-only architecture audit; --require-light is the final migration gate."""
import argparse,json,subprocess
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
OPTIONAL={
 "wechat-bot/internal/bailian":"speech",
 "wechat-bot/internal/mobile":"mobile-control",
 "wechat-bot/internal/agenthost":"project-development",
 "wechat-bot/internal/channels":"share-reader",
}
def main():
 parser=argparse.ArgumentParser();parser.add_argument("--require-light",action="store_true");args=parser.parse_args()
 result=subprocess.run(["go","list","-deps","./cmd/wechat-admin"],cwd=ROOT,text=True,capture_output=True,check=True)
 deps=set(result.stdout.splitlines());remaining=[{"package":name,"target_plugin":plugin} for name,plugin in OPTIONAL.items() if name in deps]
 inventory=[]
 for p in sorted((ROOT/"plugins").glob("*/plugin.json")):
  m=json.loads(p.read_text(encoding="utf-8"));inventory.append({"id":m["id"],"status":m["status"]})
 # The named package check is necessary but not sufficient: embedded service
 # code in admin must also be migrated, so every official descriptor must ready.
 ready=not remaining and all(p["status"]=="ready" for p in inventory) and len(inventory)==9
 print(json.dumps({"light_core_ready":ready,"remaining_direct_dependencies":remaining,"plugins":inventory,"audit_only":True},ensure_ascii=False,indent=2))
 return 1 if args.require_light and not ready else 0
if __name__=="__main__":raise SystemExit(main())
