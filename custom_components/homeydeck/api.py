"""The HTTP endpoints the Windows agent talks to.

The agent has no Home Assistant login. A one-time pairing code is traded for a
random token, and Home Assistant keeps only the token's hash. A token alone can
only report this PC's status and receive its commands.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
import hashlib
import hmac
from http import HTTPStatus
import secrets
import time
from typing import Any

from aiohttp import web

from homeassistant.components.http import KEY_HASS, HomeAssistantView
from homeassistant.components.http.ban import log_invalid_auth
from homeassistant.config_entries import ConfigEntryState
from homeassistant.core import HomeAssistant, callback

from .const import CONF_TOKEN_HASH, DOMAIN, PAIR_CODE_LENGTH, PAIR_CODE_SECONDS, POLL_HOLD_SECONDS
from .pc import PC, clean_ipv4, clean_mac

TOKEN_HEADER = "X-HomeyDeck-Token"
# No 0/O or 1/I/L, so a code can be typed from another screen.
CODE_ALPHABET = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"


@dataclass
class PendingPair:
    """A pairing code shown by a config flow, waiting for its agent."""

    code: str
    expires: float
    paired: asyncio.Event = field(default_factory=asyncio.Event)
    token_hash: str | None = None
    hostname: str | None = None
    mac: str | None = None
    broadcast: str | None = None


@dataclass
class HomeyDeckData:
    """What's shared between config flows and the endpoints."""

    pending: dict[str, PendingPair] = field(default_factory=dict)


def hash_token(token: str) -> str:
    """Hash a token. It's long and random, so a plain hash is enough."""
    return hashlib.sha256(token.encode()).hexdigest()


@callback
def async_ensure_api(hass: HomeAssistant) -> HomeyDeckData:
    """Register the endpoints once. A config flow can run before async_setup."""
    if (data := hass.data.get(DOMAIN)) is not None:
        return data
    data = hass.data[DOMAIN] = HomeyDeckData()
    hass.http.register_view(PairView())
    hass.http.register_view(PollView())
    hass.http.register_view(StatusView())
    hass.http.register_view(ResultView())
    return data


@callback
def async_new_pairing(hass: HomeAssistant) -> PendingPair:
    """Create a one-time pairing code."""
    data = async_ensure_api(hass)
    now = time.monotonic()
    for code, pending in list(data.pending.items()):
        if pending.expires <= now:
            del data.pending[code]
    code = "".join(secrets.choice(CODE_ALPHABET) for _ in range(PAIR_CODE_LENGTH))
    pending = data.pending[code] = PendingPair(code, now + PAIR_CODE_SECONDS)
    return pending


async def _read_json(request: web.Request) -> dict[str, Any] | None:
    try:
        body = await request.json()
    except ValueError:
        return None
    return body if isinstance(body, dict) else None


class _AgentView(HomeAssistantView):
    """An endpoint for an already paired agent."""

    requires_auth = False

    def _find(self, hass: HomeAssistant, request: web.Request) -> PC | web.Response:
        """Find the PC for this request, or the response to give instead."""
        token = request.headers.get(TOKEN_HEADER, "")
        digest = hash_token(token)
        data: HomeyDeckData = hass.data[DOMAIN]
        # Pairing finished but the config flow hasn't created the entry yet.
        if any(p.token_hash == digest for p in data.pending.values()):
            return self.json({"commands": []})
        for entry in hass.config_entries.async_entries(DOMAIN):
            if not hmac.compare_digest(entry.data.get(CONF_TOKEN_HASH, ""), digest):
                continue
            if entry.state is ConfigEntryState.LOADED:
                return entry.runtime_data
            # Disabled or still starting is not the same as removed.
            return self.json(
                {"error": "not ready"}, status_code=HTTPStatus.SERVICE_UNAVAILABLE
            )
        # The agent uninstalls itself when it hears this code, so it must only
        # be sent for a token Home Assistant really doesn't know.
        return self.json(
            {"error": "this PC was removed", "code": "pc_removed"},
            status_code=HTTPStatus.UNAUTHORIZED,
        )


class PairView(HomeAssistantView):
    """Trade a pairing code for a token."""

    url = "/api/homeydeck/pair"
    name = "api:homeydeck:pair"
    requires_auth = False

    @log_invalid_auth
    async def post(self, request: web.Request) -> web.Response:
        """Handle a pairing attempt. Wrong codes count towards an IP ban."""
        hass = request.app[KEY_HASS]
        body = await _read_json(request)
        code = str((body or {}).get("code", "")).strip().upper()
        data: HomeyDeckData = hass.data[DOMAIN]
        pending = next(
            (p for c, p in data.pending.items() if hmac.compare_digest(c, code)), None
        )
        if (
            body is None
            or pending is None
            or pending.token_hash is not None
            or pending.expires <= time.monotonic()
        ):
            return self.json(
                {"error": "that pairing code is wrong or has expired"},
                status_code=HTTPStatus.UNAUTHORIZED,
            )
        token = secrets.token_urlsafe(32)
        pending.token_hash = hash_token(token)
        pending.hostname = str(body.get("hostname", ""))[:64] or None
        pending.mac = clean_mac(body.get("mac"))
        pending.broadcast = clean_ipv4(body.get("broadcast"))
        pending.paired.set()
        return self.json({"token": token})


class PollView(_AgentView):
    """Report status and wait for commands."""

    url = "/api/homeydeck/poll"
    name = "api:homeydeck:poll"

    async def post(self, request: web.Request) -> web.Response:
        """Hold the request until there's a command or the hold time is up."""
        hass = request.app[KEY_HASS]
        found = self._find(hass, request)
        if isinstance(found, web.Response):
            return found
        body = await _read_json(request)
        if body is None:
            return self.json({"error": "bad request"}, HTTPStatus.BAD_REQUEST)
        found.async_report(body)
        commands = await found.async_wait_for_commands(POLL_HOLD_SECONDS)
        return self.json({"commands": commands})


class StatusView(_AgentView):
    """Report a status change without waiting."""

    url = "/api/homeydeck/status"
    name = "api:homeydeck:status"

    async def post(self, request: web.Request) -> web.Response:
        """Take in the status."""
        hass = request.app[KEY_HASS]
        found = self._find(hass, request)
        if isinstance(found, web.Response):
            return found
        body = await _read_json(request)
        if body is None:
            return self.json({"error": "bad request"}, HTTPStatus.BAD_REQUEST)
        found.async_report(body)
        return self.json({})


class ResultView(_AgentView):
    """Report how a command went."""

    url = "/api/homeydeck/result"
    name = "api:homeydeck:result"

    async def post(self, request: web.Request) -> web.Response:
        """Take in the result."""
        hass = request.app[KEY_HASS]
        found = self._find(hass, request)
        if isinstance(found, web.Response):
            return found
        body = await _read_json(request)
        if body is None:
            return self.json({"error": "bad request"}, HTTPStatus.BAD_REQUEST)
        found.async_command_result(body)
        return self.json({})
