"""Tests for worker token strategies and the client's 401 refresh."""

from __future__ import annotations

import json
from pathlib import Path
from urllib.error import HTTPError

import pytest

from scimesh.worker.auth import (
    StaticTokenProvider,
    TokenExchangeError,
    WorkerKeyTokenProvider,
    provider_from_config,
)
from scimesh.worker.config import WorkerConfig
from scimesh.worker.coordinator import HttpCoordinatorClient


class FakeResponse:
    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, *exc) -> bool:
        return False


class SeqOpener:
    """Returns/raises a scripted sequence of responses, recording each request."""

    def __init__(self, actions: list) -> None:
        self.actions = list(actions)
        self.requests: list = []

    def open(self, request, timeout=None):
        self.requests.append(request)
        action = self.actions.pop(0)
        if isinstance(action, Exception):
            raise action
        return action


def _exchange_response(token: str, expires_in: int) -> FakeResponse:
    return FakeResponse(200, json.dumps({"token": token, "expires_in": expires_in}).encode())


def test_static_provider_returns_fixed_token_and_never_refreshes():
    provider = StaticTokenProvider("tok")
    assert provider.token() == "tok"
    provider.refresh()
    assert provider.token() == "tok"


def test_static_provider_none_means_no_auth():
    assert StaticTokenProvider(None).token() is None


def test_worker_key_provider_exchanges_once_then_caches():
    clock = {"t": 1000.0}
    provider = WorkerKeyTokenProvider(
        "http://users", "scimesh_wk_live_x", timeout=5, now=lambda: clock["t"]
    )
    provider._opener = SeqOpener([_exchange_response("jwt-1", 100)])

    # First call exchanges; a second call well within the TTL reuses the cache.
    assert provider.token() == "jwt-1"
    clock["t"] = 1050.0  # 50s later, TTL 100s with 0.2 leeway → refresh at +80s
    assert provider.token() == "jwt-1"
    assert len(provider._opener.requests) == 1


def test_worker_key_provider_refreshes_after_leeway():
    clock = {"t": 0.0}
    provider = WorkerKeyTokenProvider(
        "http://users", "k", timeout=5, now=lambda: clock["t"]
    )
    provider._opener = SeqOpener([
        _exchange_response("jwt-1", 100),
        _exchange_response("jwt-2", 100),
    ])
    assert provider.token() == "jwt-1"
    clock["t"] = 85.0  # past the 80s refresh point
    assert provider.token() == "jwt-2"
    assert len(provider._opener.requests) == 2


def test_worker_key_provider_force_refresh():
    provider = WorkerKeyTokenProvider("http://users", "k", timeout=5, now=lambda: 0.0)
    provider._opener = SeqOpener([
        _exchange_response("jwt-1", 100),
        _exchange_response("jwt-2", 100),
    ])
    assert provider.token() == "jwt-1"
    provider.refresh()
    assert provider.token() == "jwt-2"


def test_worker_key_provider_raises_on_rejected_key():
    provider = WorkerKeyTokenProvider("http://users", "bad", timeout=5, now=lambda: 0.0)
    provider._opener = SeqOpener([HTTPError("http://users", 401, "unauthorized", {}, None)])
    with pytest.raises(TokenExchangeError):
        provider.token()


def test_worker_key_provider_raises_when_token_missing():
    provider = WorkerKeyTokenProvider("http://users", "k", timeout=5, now=lambda: 0.0)
    provider._opener = SeqOpener([FakeResponse(200, json.dumps({"expires_in": 100}).encode())])
    with pytest.raises(TokenExchangeError):
        provider.token()


def test_provider_from_config_selects_worker_key_mode():
    provider = provider_from_config(
        worker_key="scimesh_wk_live_x",
        userservice_url="http://users",
        bearer_token="ignored",
        request_timeout=5,
    )
    assert isinstance(provider, WorkerKeyTokenProvider)


def test_provider_from_config_falls_back_to_static():
    provider = provider_from_config(
        worker_key=None, userservice_url=None, bearer_token="tok", request_timeout=5
    )
    assert isinstance(provider, StaticTokenProvider)
    assert provider.token() == "tok"


class RefreshCountingProvider:
    def __init__(self) -> None:
        self.tokens = ["stale", "fresh"]
        self.index = 0
        self.refreshes = 0

    def token(self) -> str:
        return self.tokens[min(self.index, len(self.tokens) - 1)]

    def refresh(self) -> None:
        self.refreshes += 1
        self.index += 1


def test_coordinator_client_refreshes_and_retries_once_on_401():
    provider = RefreshCountingProvider()
    client = HttpCoordinatorClient("http://coord", timeout=5, token_provider=provider)
    client._opener = SeqOpener([
        HTTPError("http://coord/tasks/claim", 401, "unauthorized", {}, None),
        FakeResponse(204, b""),
    ])

    status, _ = client._request("POST", "/tasks/claim", {"worker_id": "w"})

    assert status == 204
    assert provider.refreshes == 1
    # The retry carried the refreshed token.
    assert provider.index == 1


def test_coordinator_client_does_not_loop_on_persistent_401():
    provider = RefreshCountingProvider()
    client = HttpCoordinatorClient("http://coord", timeout=5, token_provider=provider)
    client._opener = SeqOpener([
        HTTPError("http://coord/x", 401, "unauthorized", {}, None),
        HTTPError("http://coord/x", 401, "unauthorized", {}, None),
    ])

    status, _ = client._request("POST", "/x", {})

    # One refresh, one retry, then the second 401 is surfaced rather than retried.
    assert status == 401
    assert provider.refreshes == 1


def _base_config(**extra) -> dict:
    return {
        "coordinator_url": "http://coord",
        "worker_id": None,
        "work_dir": Path("."),
        **extra,
    }


def test_worker_key_requires_userservice_url():
    with pytest.raises(ValueError, match="userservice_url"):
        WorkerConfig(**_base_config(worker_key="scimesh_wk_live_x"))


def test_worker_key_with_userservice_url_is_valid():
    cfg = WorkerConfig(**_base_config(worker_key="scimesh_wk_live_x", userservice_url="http://users"))
    assert cfg.worker_key == "scimesh_wk_live_x"
    assert cfg.userservice_url == "http://users"


def test_userservice_url_must_be_absolute():
    with pytest.raises(ValueError, match="userservice_url"):
        WorkerConfig(**_base_config(userservice_url="not-a-url"))
