"""Wake, launch Steam, launch Sunshine, sleep and restart."""

from __future__ import annotations

from homeassistant.components.button import ButtonDeviceClass, ButtonEntity
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from . import HomeyDeckConfigEntry
from .const import (
    ACTION_LAUNCH_STEAM,
    ACTION_LAUNCH_SUNSHINE,
    ACTION_REBOOT,
    ACTION_SLEEP,
)
from .entity import HomeyDeckEntity
from .pc import PC


async def async_setup_entry(
    hass: HomeAssistant,
    entry: HomeyDeckConfigEntry,
    async_add_entities: AddConfigEntryEntitiesCallback,
) -> None:
    """Add the buttons."""
    pc = entry.runtime_data
    async_add_entities(
        [
            WakeButton(pc),
            CommandButton(pc, "launch_steam", ACTION_LAUNCH_STEAM),
            CommandButton(pc, "launch_sunshine", ACTION_LAUNCH_SUNSHINE),
            CommandButton(pc, "sleep", ACTION_SLEEP),
            CommandButton(pc, "restart", ACTION_REBOOT, ButtonDeviceClass.RESTART),
        ]
    )


class WakeButton(HomeyDeckEntity, ButtonEntity):
    """Send Wake-on-LAN."""

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "wake")

    @property
    def available(self) -> bool:
        """Needs the MAC the agent reports."""
        return self.pc.mac is not None

    async def async_press(self) -> None:
        """Wake the PC."""
        await self.pc.async_wake()


class CommandButton(HomeyDeckEntity, ButtonEntity):
    """Ask the agent to do something. Only offered while the PC is on."""

    def __init__(
        self,
        pc: PC,
        key: str,
        action: str,
        device_class: ButtonDeviceClass | None = None,
    ) -> None:
        super().__init__(pc, key)
        self._action = action
        self._attr_device_class = device_class

    @property
    def available(self) -> bool:
        """Only while the PC is on."""
        return self.pc.online

    async def async_press(self) -> None:
        """Send the command."""
        self.pc.async_send_command(self._action)
