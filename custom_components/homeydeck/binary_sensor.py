"""Whether the PC is on, and whether Steam and Sunshine are running."""

from __future__ import annotations

from homeassistant.components.binary_sensor import (
    BinarySensorDeviceClass,
    BinarySensorEntity,
)
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

from . import HomeyDeckConfigEntry
from .entity import HomeyDeckEntity
from .pc import PC


async def async_setup_entry(
    hass: HomeAssistant,
    entry: HomeyDeckConfigEntry,
    async_add_entities: AddConfigEntryEntitiesCallback,
) -> None:
    """Add the sensors."""
    pc = entry.runtime_data
    async_add_entities([OnlineSensor(pc), SteamSensor(pc), SunshineSensor(pc)])


class OnlineSensor(HomeyDeckEntity, BinarySensorEntity):
    """On when the agent has been heard from."""

    _attr_device_class = BinarySensorDeviceClass.CONNECTIVITY

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "online")

    @property
    def is_on(self) -> bool:
        """Return whether the PC is on."""
        return self.pc.online


class SteamSensor(HomeyDeckEntity, BinarySensorEntity):
    """On when Steam is running."""

    _attr_device_class = BinarySensorDeviceClass.RUNNING

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "steam")

    @property
    def available(self) -> bool:
        """Only known while the PC is on."""
        return self.pc.online

    @property
    def is_on(self) -> bool:
        """Return whether Steam is running."""
        return self.pc.steam_running


class SunshineSensor(HomeyDeckEntity, BinarySensorEntity):
    """On when Sunshine is running."""

    _attr_device_class = BinarySensorDeviceClass.RUNNING

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "sunshine")

    @property
    def available(self) -> bool:
        """Only known while the PC is on."""
        return self.pc.online

    @property
    def is_on(self) -> bool:
        """Return whether Sunshine is running."""
        return self.pc.sunshine_running
