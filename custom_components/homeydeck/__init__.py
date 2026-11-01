"""HomeyDeck: wake, check and control a Windows gaming PC from Home Assistant."""

from __future__ import annotations

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import EVENT_HOMEASSISTANT_STOP, Platform
from homeassistant.core import Event, HomeAssistant, callback
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers.typing import ConfigType

from .api import async_ensure_api
from .const import DOMAIN
from .pc import PC

PLATFORMS = [Platform.BINARY_SENSOR, Platform.BUTTON, Platform.SENSOR]
CONFIG_SCHEMA = cv.config_entry_only_config_schema(DOMAIN)

type HomeyDeckConfigEntry = ConfigEntry[PC]


async def async_setup(hass: HomeAssistant, config: ConfigType) -> bool:
    """Set up the agent endpoints."""
    async_ensure_api(hass)
    return True


async def async_setup_entry(hass: HomeAssistant, entry: HomeyDeckConfigEntry) -> bool:
    """Set up one paired PC."""
    async_ensure_api(hass)
    pc = PC(hass, entry)
    entry.runtime_data = pc
    pc.async_start()
    entry.async_on_unload(pc.async_stop)

    @callback
    def _release_on_stop(_event: Event) -> None:
        # A held poll mustn't delay Home Assistant shutting down.
        pc.async_stop()

    entry.async_on_unload(
        hass.bus.async_listen_once(EVENT_HOMEASSISTANT_STOP, _release_on_stop)
    )
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: HomeyDeckConfigEntry) -> bool:
    """Unload one paired PC."""
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
