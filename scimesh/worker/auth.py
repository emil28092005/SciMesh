"""Bearer-token strategies for the worker's coordinator calls.

A worker authenticates in one of two ways:

* a *static* token — the shared service token or a directly supplied JWT, fixed
  for the life of the process; or
* a *worker key* — a long-lived per-user credential the worker trades for a
  short-lived JWT at the userservice, refreshing before that JWT expires.

Both are exposed through the small ``TokenProvider`` protocol so the HTTP
clients neither know nor care which one is in play.
"""

from __future__ import annotations

import json
import time
from typing import Callable, Protocol
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener

from .transport import NoRedirectHandler


class TokenExchangeError(RuntimeError):
    """The userservice refused or failed to exchange a worker key."""


class TokenProvider(Protocol):
    def token(self) -> str | None:
        """Return the current bearer token, refreshing it if necessary."""

    def refresh(self) -> None:
        """Force the next token to be re-fetched (e.g. after a 401)."""


class StaticTokenProvider:
    """Serves a fixed token forever. ``None`` means "send no Authorization"."""

    def __init__(self, token: str | None) -> None:
        self._token = token

    def token(self) -> str | None:
        return self._token

    def refresh(self) -> None:  # noqa: D401 - nothing to refresh
        return None


class WorkerKeyTokenProvider:
    """Exchanges a long-lived worker key for short-lived JWTs and refreshes them.

    The token is cached until roughly ``1 - refresh_leeway`` of its lifetime has
    elapsed, so the worker renews ahead of expiry instead of waiting for a 401.
    A monotonic clock is injectable to keep tests deterministic.
    """

    def __init__(
        self,
        userservice_url: str,
        worker_key: str,
        timeout: float,
        *,
        refresh_leeway: float = 0.2,
        now: Callable[[], float] = time.monotonic,
    ) -> None:
        self._url = userservice_url.rstrip("/")
        self._key = worker_key
        self._timeout = timeout
        self._leeway = refresh_leeway
        self._now = now
        self._token: str | None = None
        self._refresh_at: float = 0.0
        self._opener = build_opener(NoRedirectHandler())

    def token(self) -> str:
        if self._token is None or self._now() >= self._refresh_at:
            self._exchange()
        assert self._token is not None  # _exchange sets it or raises
        return self._token

    def refresh(self) -> None:
        self._exchange()

    def _exchange(self) -> None:
        request = Request(
            f"{self._url}/worker-tokens/exchange",
            data=json.dumps({"key": self._key}).encode(),
            method="POST",
            headers={"Content-Type": "application/json"},
        )
        try:
            with self._opener.open(request, timeout=self._timeout) as response:
                raw = response.read()
                data = json.loads(raw) if raw else {}
        except HTTPError as error:
            # A revoked or unknown key is a permanent 401; there is nothing the
            # worker can do but stop, so surface it rather than retry forever.
            raise TokenExchangeError(
                f"worker key exchange rejected with status {error.code}"
            ) from error
        except (URLError, TimeoutError, json.JSONDecodeError) as error:
            raise TokenExchangeError("worker key exchange request failed") from error

        token = data.get("token")
        if not isinstance(token, str) or not token:
            raise TokenExchangeError("worker key exchange response is missing a token")

        expires_in = data.get("expires_in")
        ttl = float(expires_in) if isinstance(expires_in, (int, float)) and expires_in > 0 else 0.0
        self._token = token
        # Renew once ~(1 - leeway) of the lifetime is gone. An unknown TTL falls
        # back to re-exchanging on the next call — correct, just chattier.
        self._refresh_at = self._now() + ttl * (1.0 - self._leeway)


def provider_from_config(
    *,
    worker_key: str | None,
    userservice_url: str | None,
    bearer_token: str | None,
    request_timeout: float,
) -> TokenProvider:
    """Pick the token strategy: a worker key (exchange mode) wins over a static
    bearer token, which in turn wins over no credential at all."""
    if worker_key and userservice_url:
        return WorkerKeyTokenProvider(userservice_url, worker_key, request_timeout)
    return StaticTokenProvider(bearer_token)
