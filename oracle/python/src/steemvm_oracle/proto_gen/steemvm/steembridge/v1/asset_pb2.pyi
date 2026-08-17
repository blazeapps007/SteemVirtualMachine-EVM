from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from typing import ClassVar as _ClassVar

DESCRIPTOR: _descriptor.FileDescriptor

class BridgeAsset(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    BRIDGE_ASSET_STEEM: _ClassVar[BridgeAsset]
    BRIDGE_ASSET_SBD: _ClassVar[BridgeAsset]
BRIDGE_ASSET_STEEM: BridgeAsset
BRIDGE_ASSET_SBD: BridgeAsset
