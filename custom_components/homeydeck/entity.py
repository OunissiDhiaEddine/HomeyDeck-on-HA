"""Base entity for a paired PC."""

from __future__ import annotations

from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.entity import Entity

from .const import DOMAIN
from .pc import PC


class HomeyDeckEntity(Entity):
    """Something belonging to one PC, updated when the agent reports."""

    _attr_has_entity_name = True
    _attr_should_poll = False

    def __init__(self, pc: PC, key: str) -> None:
        self.pc = pc
        self._attr_translation_key = key
        self._attr_unique_id = f"{pc.entry.entry_id}_{key}"
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, pc.entry.entry_id)},
            name=pc.entry.title,
            manufacturer="HomeyDeck",
        )

    async def async_added_to_hass(self) -> None:
        """Listen for changes."""
        self.async_on_remove(self.pc.async_add_listener(self.async_write_ha_state))
