# Legacy share-reader Go SDK

This directory retains the former `internal/channels` resolver and its Go
tests solely as a regression fixture for the share-reader migration. It is not
a production package: command and admin code must use `internal/sharespec` for
declarations and bounded response parsing, and `internal/mediadecode` for the
local Channels prefix transform.

Do not add a production import of this directory. The resolver's transport
logic exists only to preserve historical regression coverage while the
installed `share-reader` plugin owns live resolution.
