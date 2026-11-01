# Legacy mobile SDK regression source

This directory preserves the former `internal/mobile` implementation and its
tests, including the old Control queue, observation/action loop, recording
collection and repository adapter. It is developer regression source, not the
installed plugin runtime and not an implementation for production imports.

Production uses the neutral `internal/devicespec` contracts and the installed
`device.workflow`, planner and native capabilities. Only the legacy admin
fixtures and metadata compatibility tests import this SDK. Moving this source
does not alter existing device tasks, private settings, models or media.

Run the synthetic SDK regression suite from the repository:

```powershell
go test ./plugins/mobile-control/dev/legacy-sdk
go vet ./plugins/mobile-control/dev/legacy-sdk
```

These tests use temporary synthetic files and mock bridges/planners; they do not
authorize real device, model or WeChat calls. Passing them is compatibility
evidence for the preserved source, not physical-device acceptance of the installed
plugin. Production must not import this directory or use it as a fallback when
the installed plugin is unavailable.
