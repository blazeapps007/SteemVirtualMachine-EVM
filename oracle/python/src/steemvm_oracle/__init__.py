"""SteemVM validator oracle client (Python).

Functionally equivalent to ``oracle/go``: bridge attestation relay
(deposits, withdrawal-payout attestations, name registrations) plus the
commit-reveal price feed for ``x/oracle/data``. See ``oracle/PROTOCOL.md``
for the cross-language wire protocol every implementation must match
byte-for-byte, and ``oracle/go/relayer/`` for the reference implementation
this package mirrors file-for-file.

Importing this package first patches ``sys.path`` (see ``_protopath.py``)
so the buf-generated protobuf modules under ``proto_gen/`` -- which use
absolute imports like ``from cosmos.tx.v1beta1 import tx_pb2`` -- resolve
correctly.
"""

from . import _protopath  # noqa: F401  (side effect: patches sys.path)

__all__: list[str] = []
