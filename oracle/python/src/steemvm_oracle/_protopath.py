"""Adds the bundled ``proto_gen/`` directory to ``sys.path``.

The buf Python plugin (protoc-gen-python) emits imports relative to the
proto *root*, not relative to wherever the generated ``.py`` files end up on
disk: e.g. ``cosmos/tx/v1beta1/tx_pb2.py`` contains
``from cosmos.tx.signing.v1beta1 import signing_pb2``. For that bare
``cosmos.*`` import to resolve, the directory that directly contains
``cosmos/``, ``steemvm/``, ``amino/``, ``gogoproto/`` and ``cosmos_proto/``
-- i.e. this package's ``proto_gen/`` -- must itself be on ``sys.path``.

``google.protobuf.*`` imports are deliberately NOT bundled under
``proto_gen/`` (see ``oracle/python/buf.gen.yaml``'s header comment): they
resolve to the real ``protobuf`` pip package instead, avoiding a
duplicate-file-registration risk in protobuf's global descriptor pool (the
well-known types are already shipped by that package).

Every module in this package that touches generated protobuf code should
import this module first (``from . import _protopath``) rather than
manipulating ``sys.path`` itself, so the patch only ever happens once and
in one place.
"""

from __future__ import annotations

import os
import sys

_PROTO_GEN_DIR = os.path.join(os.path.dirname(__file__), "proto_gen")

if _PROTO_GEN_DIR not in sys.path:
    sys.path.insert(0, _PROTO_GEN_DIR)
