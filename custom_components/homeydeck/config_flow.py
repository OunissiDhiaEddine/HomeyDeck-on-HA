"""Add a PC: show a pairing code, then wait for the agent to use it."""

from __future__ import annotations

import asyncio
import time
from typing import Any
from urllib.parse import urlparse

import voluptuous as vol

from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.const import CONF_NAME, CONF_URL
from homeassistant.helpers.network import NoURLAvailableError, get_url

from .api import PendingPair, async_new_pairing
from .const import (
    CONF_BROADCAST,
    CONF_MAC,
    CONF_TOKEN_HASH,
    DOMAIN,
    PAIR_CODE_SECONDS,
    PAIR_PREFIX,
)


class HomeyDeckConfigFlow(ConfigFlow, domain=DOMAIN):
    """Pair a PC."""

    VERSION = 1

    def __init__(self) -> None:
        self._name = ""
        self._url = ""
        self._pending: PendingPair | None = None
        self._wait_task: asyncio.Task[None] | None = None

    def _default_url(self) -> str:
        try:
            return get_url(self.hass, prefer_external=False)
        except NoURLAvailableError:
            return ""

    async def async_step_user(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Ask for a name and the address the PC should use."""
        errors: dict[str, str] = {}
        if user_input is not None:
            url = user_input[CONF_URL].strip().rstrip("/")
            parsed = urlparse(url)
            if parsed.scheme not in ("http", "https") or not parsed.netloc:
                errors[CONF_URL] = "invalid_url"
            else:
                self._name = user_input[CONF_NAME].strip()
                self._url = url
                self._pending = async_new_pairing(self.hass)
                return await self.async_step_pair()
        return self.async_show_form(
            step_id="user",
            data_schema=vol.Schema(
                {
                    vol.Required(CONF_NAME, default="Gaming PC"): str,
                    vol.Required(CONF_URL, default=self._default_url()): str,
                }
            ),
            errors=errors,
        )

    async def _async_wait_for_agent(self) -> None:
        assert self._pending is not None
        async with asyncio.timeout(self._pending.expires - time.monotonic()):
            await self._pending.paired.wait()

    async def async_step_pair(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Show the code until the agent has used it."""
        assert self._pending is not None
        if self._wait_task is None:
            self._wait_task = self.hass.async_create_task(self._async_wait_for_agent())
        if not self._wait_task.done():
            return self.async_show_progress(
                step_id="pair",
                progress_action="wait_for_agent",
                description_placeholders={
                    "code": f"{PAIR_PREFIX}{self._url}/{self._pending.code}",
                    "minutes": str(PAIR_CODE_SECONDS // 60),
                },
                progress_task=self._wait_task,
            )
        if self._wait_task.cancelled() or self._wait_task.exception():
            return self.async_show_progress_done(next_step_id="expired")
        return self.async_show_progress_done(next_step_id="finish")

    async def async_step_expired(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Nobody used the code in time."""
        self._forget_pending()
        return self.async_abort(reason="code_expired")

    async def async_step_finish(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """The agent paired."""
        assert self._pending is not None
        pending = self._pending
        data = {CONF_TOKEN_HASH: pending.token_hash}
        if pending.mac:
            data[CONF_MAC] = pending.mac
        if pending.broadcast:
            data[CONF_BROADCAST] = pending.broadcast
        result = self.async_create_entry(title=self._name, data=data)
        # Dropped after the entry exists, so the agent's first poll can't look
        # like a removed PC.
        self.hass.loop.call_soon(self._forget_pending)
        return result

    def _forget_pending(self) -> None:
        if self._pending is not None:
            self.hass.data[DOMAIN].pending.pop(self._pending.code, None)

    def async_remove(self) -> None:
        """The flow was closed, so its code stops working."""
        if self._wait_task is not None:
            self._wait_task.cancel()
        self._forget_pending()
