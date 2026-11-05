# Legacy provider development SDK

This optional Go package preserves the former provider client, voice enrollment,
voice design, and read-only enrollment inspection implementations and their
synthetic HTTP tests. It declares no installed runtime capability. The bot core
must not import it. Production compatibility DTOs and observation errors belong
to `internal/mediaspec`, which contains no HTTP client.

Run the offline SDK tests from the repository root:

```powershell
go test ./plugins/speech/dev/legacy-sdk
```

The original clone/design audition source lives in `audition/*.go.in`. The
runner temporarily overlays those two test files into this repository's admin
test harness, preserving its installed speech/media plugin routes and existing
authority checks. This developer tool depends on that harness; it is not a
standalone plugin runtime or a newly implemented clone/design/training API.

No arguments print help. The following compiles both auditions with the two
provider-live flags removed, so no provider operation executes:

```powershell
python plugins/speech/dev/legacy-sdk/audition/run_audition.py --prepare
```

An actual audition requires both `--run-live --kind clone|design` and the
corresponding original `WECHAT_VOICE_CLONE_LIVE=1` or
`WECHAT_VOICE_DESIGN_LIVE=1` environment flag. All original runtime, private
workspace, reference, and specification guards remain in the preserved Go
source. Provider operations may cost money; uncertain creation is recorded and
never automatically submitted again. No audition changes the selected voice
or sends a WeChat message. Keys, voice registries, samples and results stay in
the operator's private paths and must never be committed.

Only the network implementations still present at this cutover are retained
here. TTS, ASR and music runtime execution already belongs to its installed
plugins; this directory does not restore a removed production fallback.
