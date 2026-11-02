"""Tests with a real Home Assistant, and the real agent where it matters.

Off Windows the agent only logs power actions, so nothing here sleeps or
restarts the computer running the tests.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncGenerator
import os
from pathlib import Path
import subprocess
import time
from unittest.mock import patch

import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.homeydeck.api import TOKEN_HEADER, hash_token
from custom_components.homeydeck.const import CONF_MAC, CONF_TOKEN_HASH, DOMAIN, PAIR_PREFIX
from custom_components.homeydeck.pc import clean_mac, magic_packet
from homeassistant.config_entries import ConfigEntryState
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType
from homeassistant.setup import async_setup_component

AGENT_DIR = Path(__file__).parent.parent / "agent"
TOKEN = "t" * 43


@pytest.fixture(autouse=True)
def _custom_integrations(enable_custom_integrations: None) -> None:
    """Let Home Assistant load the integration."""


@pytest.fixture(scope="session")
def agent_binary(tmp_path_factory: pytest.TempPathFactory) -> Path:
    """Build the agent for this computer."""
    out = tmp_path_factory.mktemp("agent") / "agent"
    subprocess.run(["go", "build", "-o", str(out), "."], cwd=AGENT_DIR, check=True)
    return out


async def _paired_entry(hass: HomeAssistant) -> MockConfigEntry:
    entry = MockConfigEntry(
        domain=DOMAIN,
        title="Gaming PC",
        data={CONF_TOKEN_HASH: hash_token(TOKEN), CONF_MAC: "aa:bb:cc:dd:ee:ff"},
    )
    entry.add_to_hass(hass)
    assert await async_setup_component(hass, "http", {})
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    return entry


async def _start_flow(hass: HomeAssistant, url: str) -> dict:
    assert await async_setup_component(hass, "http", {})
    result = await hass.config_entries.flow.async_init(DOMAIN, context={"source": "user"})
    assert result["type"] is FlowResultType.FORM
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {"name": "Gaming PC", "url": url}
    )
    assert result["type"] is FlowResultType.SHOW_PROGRESS
    return result


def _state(hass: HomeAssistant, entity: str) -> str:
    state = hass.states.get(entity)
    assert state is not None, entity
    return state.state


async def _until(check, seconds: float = 15) -> None:
    deadline = time.monotonic() + seconds
    while not check():
        if time.monotonic() > deadline:
            raise AssertionError("timed out")
        await asyncio.sleep(0.05)


class Agent:
    """The real agent program, running against this test's Home Assistant."""

    def __init__(self, binary: Path, data_dir: Path) -> None:
        self.binary = binary
        self.data_dir = data_dir
        self.env = {
            **os.environ,
            "HOMEYDECK_DATA_DIR": str(data_dir),
            "HOMEYDECK_FAKE_STEAM": "0",
        }
        self.process: asyncio.subprocess.Process | None = None

    async def install(self, code: str) -> tuple[int, str]:
        proc = await asyncio.create_subprocess_exec(
            str(self.binary), "install", "--code", code,
            env=self.env,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
        )
        out, _ = await proc.communicate()
        return proc.returncode, out.decode()

    async def run(self) -> None:
        self.process = await asyncio.create_subprocess_exec(
            str(self.binary), "run", env=self.env,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )

    def log(self) -> str:
        path = self.data_dir / "agent.log"
        return path.read_text() if path.exists() else ""

    async def stop(self) -> None:
        if self.process and self.process.returncode is None:
            self.process.terminate()
            await self.process.wait()


@pytest.fixture
async def agent(agent_binary: Path, tmp_path: Path) -> AsyncGenerator[Agent]:
    """A real agent, stopped afterwards."""
    a = Agent(agent_binary, tmp_path / "data")
    yield a
    await a.stop()


async def test_real_agent_full_journey(
    hass: HomeAssistant, aiohttp_server, agent: Agent, socket_enabled
) -> None:
    """Pair, report, take every command, sleep, then get removed."""
    assert await async_setup_component(hass, "http", {})
    # Real Home Assistant keeps its router open, as integrations can arrive after boot.
    hass.http.app._router.freeze = lambda: None
    server = await aiohttp_server(hass.http.app)
    url = f"http://127.0.0.1:{server.port}"

    result = await _start_flow(hass, url)
    line = result["description_placeholders"]["code"]
    assert line.startswith(f"{PAIR_PREFIX}{url}/")

    # The agent pairs from the exact line Home Assistant shows.
    code, out = await agent.install(line)
    assert code == 0, out

    # Home Assistant tells the open page the wait is over, and the page moves on
    # with no button pressed. This is the call the page makes.
    await hass.async_block_till_done()
    done = await hass.config_entries.flow.async_configure(result["flow_id"])
    assert done["type"] is FlowResultType.CREATE_ENTRY
    await hass.async_block_till_done()
    entry = hass.config_entries.async_entries(DOMAIN)[0]
    assert entry.title == "Gaming PC"
    assert entry.state is ConfigEntryState.LOADED
    assert CONF_TOKEN_HASH in entry.data
    assert "token" not in entry.data  # only ever the hash

    await agent.run()
    online = "binary_sensor.gaming_pc_online"
    await _until(lambda: _state(hass, online) == "on")
    assert _state(hass, "binary_sensor.gaming_pc_steam") == "off"
    assert _state(hass, "binary_sensor.gaming_pc_sunshine") == "off"
    assert _state(hass, "sensor.gaming_pc_agent_version") == "dev"
    assert _state(hass, "sensor.gaming_pc_started") not in ("unknown", "unavailable")

    # A held poll answers right away, not at the next poll.
    started = time.monotonic()
    await hass.services.async_call(
        "button", "press", {"entity_id": "button.gaming_pc_launch_steam"}, blocking=True
    )
    await _until(lambda: "command launch_steam" in agent.log())
    assert time.monotonic() - started < 3
    await _until(lambda: "would launch Steam" in agent.log())

    await hass.services.async_call(
        "button", "press", {"entity_id": "button.gaming_pc_restart"}, blocking=True
    )
    await _until(lambda: "would reboot" in agent.log())
    # Restarting marks it offline straight away, instead of 70 s later.
    await _until(lambda: _state(hass, online) == "off")
    assert _state(hass, "button.gaming_pc_sleep") == "unavailable"
    assert _state(hass, "binary_sensor.gaming_pc_steam") == "unavailable"
    assert _state(hass, "binary_sensor.gaming_pc_sunshine") == "unavailable"

    # Removing the PC in Home Assistant uninstalls the agent.
    await hass.config_entries.async_remove(entry.entry_id)
    await hass.async_block_till_done()
    assert agent.process is not None
    await asyncio.wait_for(agent.process.wait(), 15)
    assert not agent.data_dir.exists()


async def test_agent_keeps_pairing_when_home_assistant_restarts_loading(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """A known token on an unloaded entry must never read as removed."""
    entry = await _paired_entry(hass)
    client = await hass_client_no_auth()
    await hass.config_entries.async_unload(entry.entry_id)
    await hass.async_block_till_done()
    resp = await client.post(
        "/api/homeydeck/status", json={}, headers={TOKEN_HEADER: TOKEN}
    )
    assert resp.status == 503
    assert (await resp.json()).get("code") != "pc_removed"


async def test_unknown_token_is_told_removed_without_ban(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """The agent's own removal signal, which mustn't count towards an IP ban."""
    await _paired_entry(hass)
    client = await hass_client_no_auth()
    with patch(
        "homeassistant.components.http.ban.process_wrong_login"
    ) as wrong_login:
        resp = await client.post(
            "/api/homeydeck/poll", json={}, headers={TOKEN_HEADER: "wrong"}
        )
    assert resp.status == 401
    assert (await resp.json())["code"] == "pc_removed"
    wrong_login.assert_not_called()


async def test_no_token_cannot_read_or_command(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """Every agent endpoint needs the token."""
    await _paired_entry(hass)
    client = await hass_client_no_auth()
    for path in ("poll", "status", "result"):
        resp = await client.post(f"/api/homeydeck/{path}", json={})
        assert resp.status == 401, path


async def test_wrong_pairing_code_counts_towards_ban(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """Guessing codes is what the ban is for."""
    await _start_flow(hass, "http://192.168.1.10:8123")
    client = await hass_client_no_auth()
    with patch(
        "homeassistant.components.http.ban.process_wrong_login"
    ) as wrong_login:
        resp = await client.post("/api/homeydeck/pair", json={"code": "WRONGWRONG"})
    assert resp.status == 401
    wrong_login.assert_called_once()


async def test_code_works_once(hass: HomeAssistant, hass_client_no_auth) -> None:
    """A used code can't pair a second PC."""
    result = await _start_flow(hass, "http://192.168.1.10:8123")
    code = result["description_placeholders"]["code"].rsplit("/", 1)[1]
    client = await hass_client_no_auth()
    resp = await client.post("/api/homeydeck/pair", json={"code": code})
    assert resp.status == 200
    token = (await resp.json())["token"]
    assert len(token) >= 40
    resp = await client.post("/api/homeydeck/pair", json={"code": code})
    assert resp.status == 401
    await hass.async_block_till_done()


async def test_closing_the_flow_kills_the_code(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """Closing the dialog means the code stops working."""
    result = await _start_flow(hass, "http://192.168.1.10:8123")
    code = result["description_placeholders"]["code"].rsplit("/", 1)[1]
    hass.config_entries.flow.async_abort(result["flow_id"])
    await hass.async_block_till_done()
    client = await hass_client_no_auth()
    resp = await client.post("/api/homeydeck/pair", json={"code": code})
    assert resp.status == 401


async def test_invalid_address_is_refused(hass: HomeAssistant) -> None:
    """The address has to be a full one."""
    assert await async_setup_component(hass, "http", {})
    result = await hass.config_entries.flow.async_init(DOMAIN, context={"source": "user"})
    result = await hass.config_entries.flow.async_configure(
        result["flow_id"], {"name": "PC", "url": "192.168.1.10"}
    )
    assert result["errors"] == {"url": "invalid_url"}


async def test_older_agent_without_sunshine_reads_off(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """An agent from before Sunshine support keeps working."""
    await _paired_entry(hass)
    client = await hass_client_no_auth()
    resp = await client.post(
        "/api/homeydeck/status",
        json={"steamRunning": True, "uptimeSeconds": 60, "agentVersion": "v0.1.0"},
        headers={TOKEN_HEADER: TOKEN},
    )
    assert resp.status == 200
    await hass.async_block_till_done()
    assert _state(hass, "binary_sensor.gaming_pc_steam") == "on"
    assert _state(hass, "binary_sensor.gaming_pc_sunshine") == "off"


async def test_sunshine_reported_running(
    hass: HomeAssistant, hass_client_no_auth
) -> None:
    """The sensor follows what the agent reports."""
    await _paired_entry(hass)
    client = await hass_client_no_auth()
    resp = await client.post(
        "/api/homeydeck/status",
        json={"sunshineRunning": True, "uptimeSeconds": 60},
        headers={TOKEN_HEADER: TOKEN},
    )
    assert resp.status == 200
    await hass.async_block_till_done()
    assert _state(hass, "binary_sensor.gaming_pc_sunshine") == "on"


async def test_wake_sends_to_broadcast_and_works_while_off(hass: HomeAssistant) -> None:
    """Wake is offered while the PC is off, using the saved MAC."""
    await _paired_entry(hass)
    assert _state(hass, "button.gaming_pc_wake") != "unavailable"
    with patch("custom_components.homeydeck.pc.send_magic_packet") as send:
        await hass.services.async_call(
            "button", "press", {"entity_id": "button.gaming_pc_wake"}, blocking=True
        )
    send.assert_called_once()
    assert send.call_args.args[0] == "aa:bb:cc:dd:ee:ff"
    assert "255.255.255.255" in send.call_args.args[1]


async def test_failed_command_notifies(hass: HomeAssistant, hass_client_no_auth) -> None:
    """A command that fails on the PC shows up in Home Assistant."""
    await _paired_entry(hass)
    client = await hass_client_no_auth()
    resp = await client.post(
        "/api/homeydeck/result",
        json={"id": "1", "action": "sleep", "ok": False, "error": "no sleep here"},
        headers={TOKEN_HEADER: TOKEN},
    )
    assert resp.status == 200
    await hass.async_block_till_done()
    assert any(
        "no sleep here" in (n.get("message") or "")
        for n in hass.data["persistent_notification"].values()
    )


def test_magic_packet_format() -> None:
    """6 x FF, then the MAC 16 times."""
    packet = magic_packet("aa:bb:cc:dd:ee:ff")
    assert len(packet) == 102
    assert packet[:6] == b"\xff" * 6
    assert packet[6:12] == bytes.fromhex("aabbccddeeff")


def test_clean_mac() -> None:
    """Windows and Go spellings both work, and junk doesn't."""
    assert clean_mac("AA-BB-CC-DD-EE-FF") == "aa:bb:cc:dd:ee:ff"
    assert clean_mac("not a mac") is None
    assert clean_mac(None) is None
