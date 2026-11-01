"""Constants for HomeyDeck."""

DOMAIN = "homeydeck"

CONF_TOKEN_HASH = "token_hash"
CONF_MAC = "mac"
CONF_BROADCAST = "broadcast"

# Keep in step with pairPrefix in agent/main.go.
PAIR_PREFIX = "homeydeck-ha:"
PAIR_CODE_LENGTH = 10
PAIR_CODE_SECONDS = 15 * 60

# How long a poll is held open while there's nothing to do. The agent's own
# timeout (pollTimeout in agent/client.go) must be longer.
POLL_HOLD_SECONDS = 25
# Missing this long means the PC is gone. Survives one lost poll.
OFFLINE_AFTER_SECONDS = 70
# A command nobody picked up in this time is dropped, so a restart can never
# run hours later.
COMMAND_EXPIRES_SECONDS = 90
# After sleep or restart, the agent's last polls shouldn't flip it back online.
GOING_DOWN_SECONDS = 30

ACTION_LAUNCH_STEAM = "launch_steam"
ACTION_SLEEP = "sleep"
ACTION_REBOOT = "reboot"
