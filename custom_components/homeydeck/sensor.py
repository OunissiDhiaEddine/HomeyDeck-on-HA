"""When the PC started, and the agent's version."""

from __future__ import annotations

from datetime import datetime

from homeassistant.components.sensor import SensorDeviceClass, SensorEntity
from homeassistant.const import EntityCategory
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
    async_add_entities([BootedSensor(entry.runtime_data), VersionSensor(entry.runtime_data)])


class BootedSensor(HomeyDeckEntity, SensorEntity):
    """When the PC last started. A time, so it isn't rewritten on every report."""

    _attr_device_class = SensorDeviceClass.TIMESTAMP

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "booted")

    @property
    def available(self) -> bool:
        """Only known while the PC is on."""
        return self.pc.online and self.pc.booted_at is not None

    @property
    def native_value(self) -> datetime | None:
        """Return the start time."""
        return self.pc.booted_at


class VersionSensor(HomeyDeckEntity, SensorEntity):
    """The agent's version, to see which PCs need updating."""

    _attr_entity_category = EntityCategory.DIAGNOSTIC

    def __init__(self, pc: PC) -> None:
        super().__init__(pc, "agent_version")

    @property
    def native_value(self) -> str | None:
        """Return the version."""
        return self.pc.agent_version
