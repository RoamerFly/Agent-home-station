# rqtx

This package is a pure-Go, bounded interpreter for an opaque program recovered
from the pinned `rqtx.dat` sample. It does not load the original shared object,
does not use CGO, and creates fresh mutable state for every request.

`program.hex` is required recovered data. The interpreter's agreement with the
included offline oracle vectors establishes only the tested input/output
equivalence for that pinned data; it does not recover a mathematical protocol
algorithm or remove the data dependency.
