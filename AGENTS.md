# Project development and repository workflow

Repository: https://github.com/RoamerFly/Agent-home-station (private).

- Current owner override: prioritize Windows implementation and verification; defer Linux/macOS work and temporarily do not apply `github-project-dev-skill`. Keep the other repository, privacy and verification rules below.
- Complete each authorized project update, run appropriate verification, then commit and push the task-related changes to this repository. Do not treat a local commit as a successful push. Report the commit and any unfinished validation or push failure.
- Use Chinese commit messages for subsequent updates. Push every verified task update and report the destination branch and verified remote SHA; distinguish development-branch content from the default branch.
- Inspect the current branch, remote history and user changes before editing. Never force-push, overwrite remote history, discard unrelated edits or bypass branch protection. For collaborative or multi-author work, use a feature branch and PR, and ensure checks pass before landing.
- Preserve the active native WeChat login, private configuration, stored messages, models and generated media. Do not send real test messages without the user's authorization for that recipient and purpose.
- Do not build/export Docker images or publish releases until the user requests that stage. Source-only checks and local executable builds are allowed.
- Never commit API keys, tokens, login/session material, real conversation exports, browser authentication state, raw research evidence, downloaded third-party binaries/models or local runtimes. The private repository does not relax this rule.
- Run `python scripts/check_repository.py` before commits. For Go changes run the relevant tests and vet; use `go test ./...` and `go vet ./...` for a complete baseline. Static front-end JavaScript is checked with `node --check`.
- Keep generated artifacts out of Git except intentional project design assets. Retain source fixtures needed for tests only when synthetic or otherwise safe for repository storage.
- Document actual capability and evidence boundaries; distinguish simulation, real model decisions, tool execution, WeChat server acceptance and phone playback.
- Do not use another user-visible Codex/ChatGPT thread to delegate project work. Keep any authorized subagent work inside the current task.

## Tool interface principle

- Defaults are starting values, not capability ceilings. Expose meaningful parameters so the Agent can adjust precision, range, page size, batching and execution time to the conversation's needs.
- Do not silently clamp an explicit Agent parameter to a project preference. Process the requested range in batches when necessary; return effective parameters, verified progress and a continuation position on partial completion.
- Distinguish configurable defaults from actual provider/protocol/resource requirements. Report a real boundary and an available continuation or alternative instead of claiming the Agent is generally unable to perform the task.
- Preserve authorization, cancellation, data integrity and verified delivery semantics. These are independent of task granularity and defaults.
