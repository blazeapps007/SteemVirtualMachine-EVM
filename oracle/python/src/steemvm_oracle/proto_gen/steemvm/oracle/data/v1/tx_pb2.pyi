from amino import amino_pb2 as _amino_pb2
from cosmos.msg.v1 import msg_pb2 as _msg_pb2
from cosmos_proto import cosmos_pb2 as _cosmos_pb2
from gogoproto import gogo_pb2 as _gogo_pb2
from steemvm.oracle.data.v1 import params_pb2 as _params_pb2
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Mapping as _Mapping, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class MsgAggregateExchangeRatePrevote(_message.Message):
    __slots__ = ("validator", "hash")
    VALIDATOR_FIELD_NUMBER: _ClassVar[int]
    HASH_FIELD_NUMBER: _ClassVar[int]
    validator: str
    hash: str
    def __init__(self, validator: _Optional[str] = ..., hash: _Optional[str] = ...) -> None: ...

class MsgAggregateExchangeRatePrevoteResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MsgAggregateExchangeRateVote(_message.Message):
    __slots__ = ("validator", "salt", "exchange_rates")
    VALIDATOR_FIELD_NUMBER: _ClassVar[int]
    SALT_FIELD_NUMBER: _ClassVar[int]
    EXCHANGE_RATES_FIELD_NUMBER: _ClassVar[int]
    validator: str
    salt: str
    exchange_rates: str
    def __init__(self, validator: _Optional[str] = ..., salt: _Optional[str] = ..., exchange_rates: _Optional[str] = ...) -> None: ...

class MsgAggregateExchangeRateVoteResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

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
