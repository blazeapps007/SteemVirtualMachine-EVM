from amino import amino_pb2 as _amino_pb2
from gogoproto import gogo_pb2 as _gogo_pb2
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class Params(_message.Message):
    __slots__ = ("bridge_enabled", "bridge_out_enabled", "gateway_account", "bridge_confirmation_threshold", "minimum_bridge_amount", "maximum_bridge_amount", "deposit_timeout_blocks", "name_service_enabled", "name_registration_min_millisteem", "name_pending_timeout_blocks", "relayer_start_block", "bridge_fee_bps", "withdrawal_timeout_blocks")
    BRIDGE_ENABLED_FIELD_NUMBER: _ClassVar[int]
    BRIDGE_OUT_ENABLED_FIELD_NUMBER: _ClassVar[int]
    GATEWAY_ACCOUNT_FIELD_NUMBER: _ClassVar[int]
    BRIDGE_CONFIRMATION_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    MINIMUM_BRIDGE_AMOUNT_FIELD_NUMBER: _ClassVar[int]
    MAXIMUM_BRIDGE_AMOUNT_FIELD_NUMBER: _ClassVar[int]
    DEPOSIT_TIMEOUT_BLOCKS_FIELD_NUMBER: _ClassVar[int]
    NAME_SERVICE_ENABLED_FIELD_NUMBER: _ClassVar[int]
    NAME_REGISTRATION_MIN_MILLISTEEM_FIELD_NUMBER: _ClassVar[int]
    NAME_PENDING_TIMEOUT_BLOCKS_FIELD_NUMBER: _ClassVar[int]
    RELAYER_START_BLOCK_FIELD_NUMBER: _ClassVar[int]
    BRIDGE_FEE_BPS_FIELD_NUMBER: _ClassVar[int]
    WITHDRAWAL_TIMEOUT_BLOCKS_FIELD_NUMBER: _ClassVar[int]
    bridge_enabled: bool
    bridge_out_enabled: bool
    gateway_account: str
    bridge_confirmation_threshold: str
    minimum_bridge_amount: int
    maximum_bridge_amount: int
    deposit_timeout_blocks: int
    name_service_enabled: bool
    name_registration_min_millisteem: int
    name_pending_timeout_blocks: int
    relayer_start_block: int
    bridge_fee_bps: int
    withdrawal_timeout_blocks: int
    def __init__(self, bridge_enabled: bool = ..., bridge_out_enabled: bool = ..., gateway_account: _Optional[str] = ..., bridge_confirmation_threshold: _Optional[str] = ..., minimum_bridge_amount: _Optional[int] = ..., maximum_bridge_amount: _Optional[int] = ..., deposit_timeout_blocks: _Optional[int] = ..., name_service_enabled: bool = ..., name_registration_min_millisteem: _Optional[int] = ..., name_pending_timeout_blocks: _Optional[int] = ..., relayer_start_block: _Optional[int] = ..., bridge_fee_bps: _Optional[int] = ..., withdrawal_timeout_blocks: _Optional[int] = ...) -> None: ...
