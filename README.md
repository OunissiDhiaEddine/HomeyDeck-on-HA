# HomeyDeck on Home Assistant

Wake, check and control your Windows gaming PC from Home Assistant. Made for Steam Remote Play and Sunshine.

This is [HomeyDeck](https://github.com/OunissiDhiaEddine/HomeyDeck) without the web app, Vercel, DDNS or an open port. Home Assistant is already on your home network, so it can wake the PC directly.

```
 Home Assistant ◀──agent keeps one request open── Windows agent
       │                                           (hidden, on the PC)
       └──Wake-on-LAN on the local network──▶ PC network card
```

In Home Assistant each PC gets:

| Type | Entities |
|---|---|
| Buttons | Wake, Launch Steam, Sleep, Restart |
| Sensors | Online, Steam, Sunshine, Started, Agent version |

## Install

1. **Integration:** from the [latest release](../../releases/latest), download `homeydeck-integration.zip`, unzip it into `custom_components/` in your Home Assistant config folder, and restart Home Assistant.
2. **Add the PC:** go to **Settings → Devices & services → Add integration → HomeyDeck**. Choose a name and the address the PC should use. Leave that screen open: it shows a pairing line.
3. **Agent:** on the PC, download `homeydeck-ha-agent.exe` from the same release, copy the pairing line, double-click the exe and click **Yes**. The screen in Home Assistant moves on by itself.

The agent reports the PC's MAC address, so there's nothing to look up. The PC still needs Wake-on-LAN turned on in its BIOS and network card settings, and Ethernet is far more reliable for waking than Wi-Fi.

It can run beside the original HomeyDeck agent: they use different names, folders and startup entries.

## Update

- **Agent:** download the newer exe and double-click it, then click **Yes**. It stays paired.
- **Integration:** replace the `custom_components/homeydeck` folder and restart Home Assistant.

## Uninstall

Any one of these stops the agent, removes it from startup and deletes `%LOCALAPPDATA%\HomeyDeckHA`:

- Windows **Settings → Apps → HomeyDeck HA agent → Uninstall**
- Double-click the exe, click **No** to updating, then **Yes** to uninstalling
- Delete the PC in Home Assistant

To remove the integration too, delete the `custom_components/homeydeck` folder and restart Home Assistant.

## Security

- The agent has no Home Assistant login. A one-time pairing code, valid for 15 minutes, is traded for a random token, and Home Assistant stores only the token's hash.
- On the PC, the token is encrypted with Windows DPAPI, readable only by that Windows user.
- The agent accepts only `launch_steam`, `sleep` and `reboot`. There's no remote shell. A command not picked up within 90 seconds is dropped.
- The PC never accepts inbound connections.
- Plain `http` is only allowed to local network and Tailscale addresses. Anywhere else the address must be `https`.
- Wrong pairing codes count towards Home Assistant's IP ban, if that's turned on.

## Development

GitHub Actions tests everything on each push, and builds on each version tag:

```bash
git tag v0.1.1 && git push origin v0.1.1
```

That publishes a release with `homeydeck-ha-agent.exe` and `homeydeck-integration.zip`.

| Part | Check |
|---|---|
| Agent | `cd agent && go vet ./... && go test ./...` |
| Integration | `pip install -r requirements-test.txt && python -m pytest` |

The integration tests pair the real agent with a real Home Assistant. Off Windows, the agent only logs power actions.
