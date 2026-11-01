"""The live state of one paired PC: what it reported, and commands waiting for it."""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime, timedelta
import re
import socket
import time
from typing import Any
import uuid

from homeassistant.components import persistent_notification
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import CALLBACK_TYPE, HomeAssistant, callback
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.event import async_track_time_interval
from homeassistant.util import dt as dt_util

from .const import (
    ACTION_REBOOT,
    ACTION_SLEEP,
    COMMAND_EXPIRES_SECONDS,
    CONF_BROADCAST,
    CONF_MAC,
    GOING_DOWN_SECONDS,
    OFFLINE_AFTER_SECONDS,
)

MAC_RE = re.compile(r"^[0-9a-f]{2}(:[0-9a-f]{2}){5}$")
IPV4_RE = re.compile(r"^\d{1,3}(\.\d{1,3}){3}$")
WOL_PORT = 9


def clean_mac(value: Any) -> str | None:
    """Return a MAC as aa:bb:cc:dd:ee:ff, or None if it isn't one."""
    if not isinstance(value, str):
        return None
    mac = value.strip().lower().replace("-", ":")
    return mac if MAC_RE.match(mac) else None


def clean_ipv4(value: Any) -> str | None:
    """Return a dotted IPv4 address, or None if it isn't one."""
    if not isinstance(value, str) or not IPV4_RE.match(value):
        return None
    try:
        socket.inet_aton(value)
    except OSError:
        return None
    return value


def magic_packet(mac: str) -> bytes:
    """6 x 0xFF followed by the MAC address repeated 16 times."""
    return b"\xff" * 6 + bytes.fromhex(mac.replace(":", "")) * 16


def send_magic_packet(mac: str, targets: list[str], repeats: int = 3) -> None:
    """Send the packet a few times, since UDP gives no delivery guarantee."""
    packet = magic_packet(mac)
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
        sent = False
        error: OSError | None = None
        for i in range(repeats):
            for target in targets:
                try:
                    sock.sendto(packet, (target, WOL_PORT))
                    sent = True
                except OSError as err:
                    error = err
            if i < repeats - 1:
                time.sleep(0.15)
        if not sent and error:
            raise error


@dataclass
class Command:
    """A command waiting for the agent."""

    id: str
    action: str
    expires: float


class PC:
    """One paired PC."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        self.hass = hass
        self.entry = entry
        self.online = False
        self.steam_running = False
        self.booted_at: datetime | None = None
        self.agent_version: str | None = None
        self._last_seen = 0.0
        self._going_down_until = 0.0
        self._queue: list[Command] = []
        self._waiter: asyncio.Future[None] | None = None
        self._listeners: list[CALLBACK_TYPE] = []
        self._cancel_timer: CALLBACK_TYPE | None = None

    @property
    def mac(self) -> str | None:
        """The MAC the agent last reported."""
        return self.entry.data.get(CONF_MAC)

    @callback
    def async_start(self) -> None:
        """Start watching for the PC going quiet."""
        self._cancel_timer = async_track_time_interval(
            self.hass, self._async_check_offline, timedelta(seconds=15)
        )

    @callback
    def async_stop(self) -> None:
        """Release a held poll and stop the timer."""
        if self._cancel_timer:
            self._cancel_timer()
            self._cancel_timer = None
        self._release_waiter()

    @callback
    def async_add_listener(self, listener: CALLBACK_TYPE) -> Callable[[], None]:
        """Call listener when the state changes. Returns how to stop."""
        self._listeners.append(listener)
        return lambda: self._listeners.remove(listener)

    @callback
    def _notify(self) -> None:
        for listener in list(self._listeners):
            listener()

    @callback
    def async_report(self, msg: dict[str, Any]) -> None:
        """Take in what the agent reported."""
        now = time.monotonic()
        self._last_seen = now
        changed = False

        online = now >= self._going_down_until
        if online != self.online:
            self.online = online
            changed = True

        steam = bool(msg.get("steamRunning"))
        if steam != self.steam_running:
            self.steam_running = steam
            changed = True

        version = msg.get("agentVersion")
        if isinstance(version, str) and version[:32] != self.agent_version:
            self.agent_version = version[:32]
            changed = True

        uptime = msg.get("uptimeSeconds")
        if isinstance(uptime, int | float) and uptime >= 0:
            booted = dt_util.utcnow() - timedelta(seconds=uptime)
            # Timing jitter between reports isn't a new boot.
            if (
                self.booted_at is None
                or abs((booted - self.booted_at).total_seconds()) > 60
            ):
                self.booted_at = booted.replace(microsecond=0)
                changed = True

        self._remember_network(msg)
        if changed:
            self._notify()

    @callback
    def _remember_network(self, msg: dict[str, Any]) -> None:
        """Keep the wake details, so waking works after a Home Assistant restart."""
        mac = clean_mac(msg.get("mac"))
        broadcast = clean_ipv4(msg.get("broadcast"))
        data = dict(self.entry.data)
        if mac and data.get(CONF_MAC) != mac:
            data[CONF_MAC] = mac
        if broadcast and data.get(CONF_BROADCAST) != broadcast:
            data[CONF_BROADCAST] = broadcast
        if data != dict(self.entry.data):
            self.hass.config_entries.async_update_entry(self.entry, data=data)
            self._notify()

    @callback
    def _async_check_offline(self, _now: datetime) -> None:
        if self.online and time.monotonic() - self._last_seen > OFFLINE_AFTER_SECONDS:
            self.online = False
            self._notify()

    @callback
    def _async_mark_going_down(self) -> None:
        self._going_down_until = time.monotonic() + GOING_DOWN_SECONDS
        if self.online:
            self.online = False
            self._notify()

    @callback
    def _release_waiter(self) -> None:
        if self._waiter and not self._waiter.done():
            self._waiter.set_result(None)
        self._waiter = None

    def _take_commands(self) -> list[Command]:
        now = time.monotonic()
        commands = [c for c in self._queue if c.expires > now]
        self._queue.clear()
        return commands

    async def async_wait_for_commands(self, hold: float) -> list[dict[str, str]]:
        """Return waiting commands, holding up to hold seconds for the next one."""
        # A newer poll replaces an older one, such as after a dropped connection.
        self._release_waiter()
        if not self._queue and hold > 0:
            waiter = self.hass.loop.create_future()
            self._waiter = waiter
            try:
                async with asyncio.timeout(hold):
                    await waiter
            except TimeoutError:
                pass
            finally:
                if self._waiter is waiter:
                    self._waiter = None
        return [{"id": c.id, "action": c.action} for c in self._take_commands()]

    @callback
    def async_send_command(self, action: str) -> None:
        """Queue a command for the agent."""
        if not self.online:
            raise HomeAssistantError(f"{self.entry.title} is offline")
        self._queue.append(
            Command(
                id=uuid.uuid4().hex,
                action=action,
                expires=time.monotonic() + COMMAND_EXPIRES_SECONDS,
            )
        )
        self._release_waiter()

    @callback
    def async_command_result(self, msg: dict[str, Any]) -> None:
        """Handle the agent's answer to a command."""
        action = str(msg.get("action", ""))[:32]
        if msg.get("ok"):
            if action in (ACTION_SLEEP, ACTION_REBOOT):
                self._async_mark_going_down()
            return
        error = str(msg.get("error", "unknown error"))[:300]
        persistent_notification.async_create(
            self.hass,
            f"{action} failed: {error}",
            title=f"HomeyDeck: {self.entry.title}",
            notification_id=f"homeydeck_{self.entry.entry_id}_error",
        )

    async def async_wake(self) -> None:
        """Send Wake-on-LAN packets on the local network."""
        mac = self.mac
        if not mac:
            raise HomeAssistantError(
                f"{self.entry.title} hasn't reported its network details yet"
            )
        targets = ["255.255.255.255"]
        if broadcast := self.entry.data.get(CONF_BROADCAST):
            targets.insert(0, broadcast)
        try:
            await self.hass.async_add_executor_job(send_magic_packet, mac, targets)
        except OSError as err:
            raise HomeAssistantError(f"Couldn't send the wake packet: {err}") from err
