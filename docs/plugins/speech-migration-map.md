# Speech plugin: executable cloud-TTS migration map

This map covers the **cloud character-TTS** path only.  It is deliberately
separate from local GPT-SoVITS, RVC singing, ASR, Fun-Music, and the Windows
YuE service.  Those use different runtimes, resource models, or task
semantics and must not be enabled merely because cloud TTS has migrated.

Status at the time of this map: `plugins/speech` is an unbound,
`migration-pending` descriptor.  The existing production route remains in
the core.  Nothing in this document authorizes enabling the package or
removing the old path.

## 1. Behaviour to preserve

The current default-cloud synthesis flow is:

```text
speech_synthesize(text, instruction, no voice_id)
  -> permittedTools.generateSpeech (internal/admin/speech.go)
  -> bailian.Client.Synthesize (internal/bailian/client.go)
  -> provider POST /api/v1/services/audio/tts/SpeechSynthesizer
  -> returned, validated audio URL
  -> host download with GetPublicMedia
  -> preparePublicAudioPreview / stageOutputWithAsset
  -> core-owned WeChat voice upload and receipt
```

The plugin must return a verified generated artifact to the host; it must
never upload or send a WeChat message itself.  A successful provider response
is not a delivery receipt.

The following rules are non-negotiable when moving that flow:

- `text` is 1--2,000 Unicode characters; a per-call instruction is at most
  1,500 characters.
- Empty instruction uses the configured instruction.  A non-empty instruction
  is appended as a *delivery adjustment*, not a replacement for the configured
  character instruction.  This is the exact composition currently performed
  by `bailian.Client.Synthesize`.
- The exact configured `tts_model`, `default_voice`, WAV format and 24 kHz
  sample rate are host-bound.  Tool arguments cannot select a provider model,
  endpoint, credential, or substitute voice.
- The provider audio URL is untrusted intermediate data.  The host must check
  its scheme/host, download it with the existing bounded media reader, validate
  the actual bytes, make the voice-note preview, register lineage, and only
  then expose `media_ids` to the reply path.
- An uncertain POST must become `needs_review` with its provider task/response
  identity retained; do not retry it automatically.  A successful synchronous
  response can complete only after host artifact validation.
- Named library voices remain a distinct route.  A `voice_id` selects the
  existing GPT-SoVITS path and is never allowed to fall back to the configured
  Alibaba voice.  RVC profiles are singing-only and must not be used for TTS.

## 2. Exact current ownership and migration destination

| Current responsibility | Current code / test evidence | Destination | Cutover condition |
| --- | --- | --- | --- |
| Tool discovery, group/owner authority and `SendVoice` gate | `internal/admin/speech.go`: `speechTools`, `callSpeech`; `internal/admin/voice_synthesis_test.go` | Core | Keep in core; discover the plugin tool only when installed, enabled and configured. |
| Named-voice selection and no-default-substitution rule | `speech.go`: `generateSpeech`; `speech_voice_constraints.go`; `voice_synthesis.go`; `voice_synthesis_test.go` | Core identity policy | Keep `voice_library_*` and task bindings in core.  Invoke cloud TTS only when `voice_id` is absent. |
| Alibaba TTS request construction and provider response parsing | `internal/bailian/client.go`: `Synthesize`, `audioURL`; `internal/bailian/client_test.go` | `plugins/speech/runtime/cloud_tts.py` plus a host route binder | Plugin prepares the constrained request and parses its declared result.  Host performs the authenticated POST and binds every field. |
| Endpoint/key/model/default voice/default instruction | `speech-config.json`, `bailian.Config`, `/api/speech` in `internal/admin/speech.go` | Plugin configuration, host secret store | Migrate with optimistic config revision.  Do not copy the whole legacy speech config. |
| Provider result URL fetch, audio byte bounds, preview conversion and asset registration | `mcptools.GetPublicMedia`, `preparePublicAudioPreview`, `stageOutputWithAsset` in `internal/admin` | Core result validator | This cannot move to a plugin because the core owns media IDs, conversion limits, asset lineage, upload idempotency and WeChat delivery. |
| Local GPT-SoVITS execution | `internal/admin/voice_synthesis.go`; `voice_synthesis_test.go` | Later local-runtime speech migration | Exclude from the cloud-TTS package cutover.  It has GPU/sandbox/weight/reference constraints. |
| Voice catalogue/privacy and selected-voice task constraints | `voice_agent.go`, `task_constraints.go`, `media_assets.go`, `cross_media.go`, `media_voice_identity_test.go` | Core | Preserve account-scoped visibility and result identity checks. |
| Voice clone/design | `internal/bailian/voice_clone.go`, `voice_design.go` and their unit tests | Later `speech.voices` operation | These currently have provider contracts and live-test helpers, not an ordinary Agent/admin capability path.  Do not expose them as migrated functionality without a durable operation record. |
| ASR, music generation, YuE/RVC | `local_asr.go`, `music_*`, `yue.go` | Other plugins | Explicitly out of this cloud-TTS change. |

## 3. Plugin contract to implement before enabling

The first production capability should be **only** `speech.synthesize`.
`speech.voices` and `speech.train` remain `migration-pending`; they need their
own operation/recovery design.

### Capability input

```json
{
  "text": "要朗读的文本",
  "instruction": "可选的本次语气调整",
  "timeout_seconds": 180
}
```

`timeout_seconds` is an execution deadline, not an authorization override.
The host applies a bounded maximum but reports the effective value rather than
silently pretending that a different value was used.

No `voice_id`, endpoint, API key, model, output URL, filename, target thread,
or send flag belongs in plugin input.  A named `voice_id` remains routed to
the core's current local-GPT-SoVITS path until that route has its own complete
plugin migration.

### Configuration mapping

Create a new revisioned plugin configuration; read `speech-config.json` only
during one-way import.  Suggested fields are:

| Plugin field | Legacy source | Secret / host-only | Notes |
| --- | --- | --- | --- |
| `tts_endpoint` | `Endpoint` plus fixed `/api/v1/services/audio/tts/SpeechSynthesizer` | no | Store the complete HTTPS route.  Validate the approved DashScope regional/business-space origin and exact path. |
| `tts_key` | `Key` | yes / yes | Never enter plugin initialization, arguments, logs, task public results, or UI responses. |
| `tts_model` | `TTSModel` | no | 1--128 chars; bound by the host route binder. |
| `default_voice` | `Voice` | no | Bound by the host route binder; cannot be overwritten per invocation. |
| `default_instruction` | `Instruction` | no | Subject to the existing 1,500-character limit. |
| plugin enabled state | `TTSEnabled` | n/a | Import `true` only after configuration validation and administrator confirmation.  A disabled legacy flag must not enable the package. |

Do **not** copy `ASR*`, `Music*`, `YuE*`, `YuEToken`, or local model fields.
Keep the old `speech-config.json` as compatibility input until an explicit
per-account migration record says it was imported.  Do not dual-write it
after cutover.  An endpoint or secret change must invalidate pending work from
the earlier configuration revision.

### Host services and result envelope

The plugin calls one fixed `wechat/http_route` route, for example
`cloud_tts`, under `network` permission.  The package declaration must bind
that route to `tts_endpoint`, `tts_key`, `speech.synthesize`, and the exact
authorization header.  A trusted core policy must verify:

1. account, conversation, requester, request purpose, source versions and
   idempotency key;
2. package digest/version, configuration revision, current `SendVoice`
   permission and plugin grant, before and after the POST;
3. model, configured default voice, 24 kHz WAV, text and composed instruction
   before passing body bytes to the provider; and
4. that the response's `output.audio.url` is a syntactically safe URL before
   the host attempts the bounded download.

The plugin may return only a small, untrusted candidate envelope such as:

```json
{
  "ok": true,
  "provider_audio_url": "https://...",
  "provider_model": "...",
  "voice": "...",
  "coverage": "provider_tts_response"
}
```

The host validator must compare `provider_model` and `voice` with the
provider response it actually received, then download and stage the audio.
Its verified result should contain core asset/media identifiers and source
lineage, not raw audio bytes or the provider URL.  The final WeChat send stays
on the existing core upload/queue/receipt path.

## 4. Tasks, identities, recovery and costs

Cloud TTS is normally synchronous, but the invocation still needs a durable
plugin job before the POST.  The job must retain account, conversation,
requester, request key, source versions, package digest, configuration
revision, exact text hash, default voice/model identity, and the selected
delivery mode.  It must not retain the API key or a raw private reference
audio file.

- Before provider acceptance: failures may be retried only by an explicitly
  new user/administrator action with a new idempotency key.
- After an uncertain POST or an accepted response whose audio cannot be
  retrieved/validated: state is `needs_review`; retain the safe provider
  response identity and do not blindly post again.
- After a verified artifact: completion records the asset ID and expected
  cloud voice identity.  The regular core send path still obtains the final
  WeChat receipt and prevents duplicate upload/send.
- Cancellation before the POST cancels locally.  Cancellation after a POST
  cannot claim that provider work was cancelled unless the provider has an
  actual verified cancel/query protocol.
- Provider usage is recorded by the host from actual response data; unknown
  usage remains unknown.  The plugin never supplies billing totals.

The existing selected-voice mechanism in `task_constraints.go` and the
asset/cross-send identity checks must remain authoritative.  For the default
cloud voice, add a stable provider voice identity to the staged asset before
enabling cross-conversation reuse; do not relabel old audio after generation.

## 5. Ordered implementation steps

1. Add the complete `speech.synthesize` schema/config/HTTP route and rehash
   the official package only once the runtime and host validator are present.
   Keep package and the other two capabilities pending until their contracts
   are real.
2. Implement `cloud_tts.py` as a stdio MCP operation with strict argument
   parsing.  It may request the fixed route and parse a response, but cannot
   download files, access secrets, select a route, or send media.
3. Add a core `speechPluginPolicy` analogous to the existing service-plugin
   policies.  It constructs the host invocation, enforces the legacy text and
   instruction composition, validates source/config/package changes, and
   performs result download/staging through the existing asset pipeline.
4. Add installed-state dispatch: absent plugin keeps the current Bailian path;
   installed-but-disabled/unconfigured plugin fails explicitly and does not
   fall back to old credentials.  Only after the migration is accepted should
   `speech_synthesize` be removed from the `bailian-speech` adapter for the
   no-`voice_id` case.
5. Import each account's cloud fields transactionally from `speech-config.json`
   in an isolated copy.  Compare source endpoint, model, voice and secret
   presence; require an administrator confirmation before enabling.  Retain a
   read-only mapping record and do not migrate ASR/music/YuE fields.
6. Run drain/backup/update only after plugin jobs and legacy in-flight speech
   work are reconciled.  Existing staged media remains core-owned and must be
   viewable/re-sendable with no plugin installed.
7. Delete the legacy provider path and old cloud-only UI fields only after
   all supported accounts have an explicit migration status.  Do not delete
   local GPT-SoVITS, catalogue, delivery or asset code as part of this step.

## 6. Reusable tests and required new synthetic tests

Reuse or preserve these current tests:

- `internal/bailian/client_test.go` for request/response behavior;
- `internal/admin/speech_test.go` for secret redaction, blank-secret
  preservation and endpoint-change rejection;
- `internal/admin/voice_synthesis_test.go` for named-voice isolation and
  sandboxed GPT-SoVITS output;
- `internal/admin/voice_agent_test.go`, `media_voice_identity_test.go` and
  cross-media tests for catalogue visibility, hash verification and identity
  preservation;
- `internal/bailian/voice_clone_test.go` and `voice_design_test.go` as future
  protocol tests only, not evidence that `speech.voices` is ready.

Add offline/synthetic contract coverage before changing the descriptor to
ready:

1. exact body composition for default instruction and per-call adjustment;
2. invalid text/instruction/model/voice/format rejected before any route call;
3. host secret never appears in plugin config, stdin, job public result,
   activity record or error;
4. request source/config/package/permission changes before and after route
   call reject the result and do not stage it;
5. provider errors, timeout, malformed JSON, missing/unsafe URL, oversized or
   invalid audio, and download failure all preserve non-delivery and the
   correct `needs_review`/failed state;
6. same idempotency key never repeats a POST or uploads/sends a second voice;
7. completed verified audio retains default provider voice identity and cannot
   be relabelled or satisfy a different named-voice task;
8. absent plugin uses legacy cloud TTS; installed disabled/unconfigured plugin
   does not fall back; named GPT-SoVITS voice remains usable without cloud TTS;
9. plugin crash, restart, cancellation and configuration upgrade preserve the
   original request and never send a default voice in place of a named voice.

Only the above synthetic tests, package verification, focused Go tests and
the plugin runtime's isolated contract test are suitable for this migration
stage.  No paid model call, real voice creation, or real WeChat message is
needed to prove the contract.
