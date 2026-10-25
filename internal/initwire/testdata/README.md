# Offline fixture provenance

The two hex fixtures were serialized offline with Python `google.protobuf`
7.36.2 from a minimal proto2 descriptor containing only the recovered
`mm.NewInitRequest`, `mm.NewInitResponse`, `BaseRequest`, `BaseResponse`,
`SKBuiltinBuffer_t`, and `CmdItem` field numbers.  They are static test data;
the Go package neither imports Python nor a protobuf runtime.
