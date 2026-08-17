from amino import amino_pb2 as _amino_pb2
from gogoproto import gogo_pb2 as _gogo_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Iterable as _Iterable, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class Params(_message.Message):
    __slots__ = ("vote_period", "vote_threshold", "reward_band", "miss_band", "whitelist")
    VOTE_PERIOD_FIELD_NUMBER: _ClassVar[int]
    VOTE_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    REWARD_BAND_FIELD_NUMBER: _ClassVar[int]
    MISS_BAND_FIELD_NUMBER: _ClassVar[int]
    WHITELIST_FIELD_NUMBER: _ClassVar[int]
    vote_period: int
    vote_threshold: str
    reward_band: str
    miss_band: str
    whitelist: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, vote_period: _Optional[int] = ..., vote_threshold: _Optional[str] = ..., reward_band: _Optional[str] = ..., miss_band: _Optional[str] = ..., whitelist: _Optional[_Iterable[str]] = ...) -> None: ...
