# Developer repair acceptance contract

The former management repair-test routes, faulty quote/receipt examples,
independent grader and regression tests are retained here as developer source.
They are absent from the production admin build and are not a runtime capability.
No application setting enables them, and the normal mobile/project tools do not
call them. Existing user projects, tasks and repair-test records remain untouched.

Run from the repository:

```powershell
python -B plugins/project-development/dev/repair-acceptance/run_contract.py --test
python -B plugins/project-development/dev/repair-acceptance/run_contract.py --vet
```

No arguments display help. The command temporarily overlays these `.go.in` files
and the old route registration into the repository's synthetic admin test harness;
it does not install the routes in a running application or edit repository files.
The tests use temporary synthetic accounts, projects and a fake WeChat transport
that rejects sends. They never call a real model, recipient or device. The actual
Linux isolation/grading checks require Bubblewrap and skip elsewhere; a skipped
check is not evidence that the grader ran on that platform. Host workspace scope,
owner permission checks and isolated process primitives remain in their original
core packages and are reused without a host-execution fallback.
