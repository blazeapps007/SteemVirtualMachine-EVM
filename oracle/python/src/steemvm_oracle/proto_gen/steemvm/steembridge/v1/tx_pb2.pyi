from amino import amino_pb2 as _amino_pb2
from cosmos.msg.v1 import msg_pb2 as _msg_pb2
from cosmos_proto import cosmos_pb2 as _cosmos_pb2
from gogoproto import gogo_pb2 as _gogo_pb2
from steemvm.steembridge.v1 import asset_pb2 as _asset_pb2
from steemvm.steembridge.v1 import params_pb2 as _params_pb2
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Mapping as _Mapping, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class MsgUpdateParams(_message.Message):
    __slots__ = ("authority", "params")
    AUTHORITY_FIELD_NUMBER: _ClassVar[int]
    PARAMS_FIELD_NUMBER: _ClassVar[int]
    authority: str
    params: _params_pb2.Params
    def __init__(self, authority: _Optional[str] = ..., params: _Optional[_Union[_params_pb2.Params, _Mapping]] = ...) -> None: ...

class MsgUpdateParamsResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgAttestDeposit(_message.Message):
    __slots__ = ("validator", "txid", "op_index", "steem_block", "steem_timestamp", "steem_sender", "gateway_account", "amount_millisteem", "memo", "asset")
    VALIDATOR_FIELD_NUMBER: _ClassVar[int]
    TXID_FIELD_NUMBER: _ClassVar[int]
    OP_INDEX_FIELD_NUMBER: _ClassVar[int]
    STEEM_BLOCK_FIELD_NUMBER: _ClassVar[int]
    STEEM_TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    STEEM_SENDER_FIELD_NUMBER: _ClassVar[int]
    GATEWAY_ACCOUNT_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_MILLISTEEM_FIELD_NUMBER: _ClassVar[int]
    MEMO_FIELD_NUMBER: _ClassVar[int]
    ASSET_FIELD_NUMBER: _ClassVar[int]
    validator: str
    txid: str
    op_index: int
    steem_block: int
    steem_timestamp: str
    steem_sender: str
    gateway_account: str
    amount_millisteem: int
    memo: str
    asset: _asset_pb2.BridgeAsset
    def __init__(self, validator: _Optional[str] = ..., txid: _Optional[str] = ..., op_index: _Optional[int] = ..., steem_block: _Optional[int] = ..., steem_timestamp: _Optional[str] = ..., steem_sender: _Optional[str] = ..., gateway_account: _Optional[str] = ..., amount_millisteem: _Optional[int] = ..., memo: _Optional[str] = ..., asset: _Optional[_Union[_asset_pb2.BridgeAsset, str]] = ...) -> None: ...

class MsgAttestDepositResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgBridgeOut(_message.Message):
    __slots__ = ("sender", "destination_steem_account", "amount_asteem", "memo", "asset")
    SENDER_FIELD_NUMBER: _ClassVar[int]
    DESTINATION_STEEM_ACCOUNT_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_ASTEEM_FIELD_NUMBER: _ClassVar[int]
    MEMO_FIELD_NUMBER: _ClassVar[int]
    ASSET_FIELD_NUMBER: _ClassVar[int]
    sender: str
    destination_steem_account: str
    amount_asteem: str
    memo: str
    asset: _asset_pb2.BridgeAsset
    def __init__(self, sender: _Optional[str] = ..., destination_steem_account: _Optional[str] = ..., amount_asteem: _Optional[str] = ..., memo: _Optional[str] = ..., asset: _Optional[_Union[_asset_pb2.BridgeAsset, str]] = ...) -> None: ...

class MsgBridgeOutResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgAttestWithdrawalPayout(_message.Message):
    __slots__ = ("validator", "withdrawal_id", "steem_txid", "op_index", "steem_block", "steem_timestamp")
    VALIDATOR_FIELD_NUMBER: _ClassVar[int]
    WITHDRAWAL_ID_FIELD_NUMBER: _ClassVar[int]
    STEEM_TXID_FIELD_NUMBER: _ClassVar[int]
    OP_INDEX_FIELD_NUMBER: _ClassVar[int]
    STEEM_BLOCK_FIELD_NUMBER: _ClassVar[int]
    STEEM_TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    validator: str
    withdrawal_id: int
    steem_txid: str
    op_index: int
    steem_block: int
    steem_timestamp: str
    def __init__(self, validator: _Optional[str] = ..., withdrawal_id: _Optional[int] = ..., steem_txid: _Optional[str] = ..., op_index: _Optional[int] = ..., steem_block: _Optional[int] = ..., steem_timestamp: _Optional[str] = ...) -> None: ...

class MsgAttestWithdrawalPayoutResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgSubmitNameRegistration(_message.Message):
    __slots__ = ("validator", "txid", "op_index", "steem_block", "steem_timestamp", "steem_account", "gateway_account", "amount_millisteem", "memo")
    VALIDATOR_FIELD_NUMBER: _ClassVar[int]
    TXID_FIELD_NUMBER: _ClassVar[int]
    OP_INDEX_FIELD_NUMBER: _ClassVar[int]
    STEEM_BLOCK_FIELD_NUMBER: _ClassVar[int]
    STEEM_TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    STEEM_ACCOUNT_FIELD_NUMBER: _ClassVar[int]
    GATEWAY_ACCOUNT_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_MILLISTEEM_FIELD_NUMBER: _ClassVar[int]
    MEMO_FIELD_NUMBER: _ClassVar[int]
    validator: str
    txid: str
    op_index: int
    steem_block: int
    steem_timestamp: str
    steem_account: str
    gateway_account: str
    amount_millisteem: int
    memo: str
    def __init__(self, validator: _Optional[str] = ..., txid: _Optional[str] = ..., op_index: _Optional[int] = ..., steem_block: _Optional[int] = ..., steem_timestamp: _Optional[str] = ..., steem_account: _Optional[str] = ..., gateway_account: _Optional[str] = ..., amount_millisteem: _Optional[int] = ..., memo: _Optional[str] = ...) -> None: ...

class MsgSubmitNameRegistrationResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgConfirmName(_message.Message):
    __slots__ = ("confirmer", "registration_id")
    CONFIRMER_FIELD_NUMBER: _ClassVar[int]
    REGISTRATION_ID_FIELD_NUMBER: _ClassVar[int]
    confirmer: str
    registration_id: int
    def __init__(self, confirmer: _Optional[str] = ..., registration_id: _Optional[int] = ...) -> None: ...

class MsgConfirmNameResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...
