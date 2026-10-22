# Installing PandaEye on your server

This guide is for server owners. PandaEye runs as a plugin on your server and connects to the hosted
PandaEye cloud (`wss://eye.pandascript.dev/v1/connect`); the dashboard is at
<https://eye-dash.pandascript.dev>. To run the cloud yourself instead, see [OPERATIONS.md](OPERATIONS.md).

## Requirements

| | Supported |
|---|---|
| Server software | Paper, Folia and their forks (Purpur, Pufferfish, Canvas, ...). Spigot and CraftBukkit are not supported: PandaEye needs Paper's schedulers and disables itself with a message on them. |
| Minecraft (server) | 1.21.4 or newer. On older servers PandaEye logs the reason and stays disabled. |
| Java | 21 or newer. |
| Required plugin | [PacketEvents](https://modrinth.com/plugin/packetevents) 2.13 or newer, installed as its own plugin (PandaEye does not bundle it). |
| Clients | Any version that can join your server (ViaVersion/ViaBackwards are fine). The local movement and timer checks need 1.21.2+ clients; older clients are judged by the cloud checks only. Bedrock players (Geyser/Floodgate) are detected and exempt. |
| Proxy | Optional: the Velocity plugin (see below). BungeeCord is not supported. |
| Network | Outbound HTTPS/WebSocket to `eye.pandascript.dev` on port 443. |

Other anticheats can stay installed: in shadow mode PandaEye never sends, cancels or modifies a packet,
so it cannot conflict with them.

## 1. Install in shadow mode

1. Put `PandaEye-<version>.jar` in `plugins/`, next to PacketEvents.
2. Create `plugins/PandaEye/config.yml` (or start the server once to get the default file) and set your
   licence key:

   ```yaml
   mode: shadow            # observe only: nothing is ever sent, cancelled, modified or punished
   server-name: "survival" # shown in the dashboard
   cloud:
     license-key: "pe-..."
   ```

   Everything else has working defaults; [ARCHITECTURE 4.11](ARCHITECTURE.md) lists every key.
3. Restart the server, or hot-load it without a restart: `plugman load PandaEye-<version>` (PlugManX).
4. Run `/pandaeye status` in the console: `mode shadow`, `cloud: READY - connected`, a probe RTT, and
   `enforcer calls attempted in shadow` all 0.

## 2. Open the dashboard

Go to <https://eye-dash.pandascript.dev/setup>, enter the same licence key and create the owner account.
Invite your staff from the dashboard (owner/admin). Verdicts appear there with their evidence and a replay;
in shadow mode they are marked "would have" and nothing happens in game.

## 3. Watch, then go live

Leave PandaEye in shadow mode for at least a week and review the verdicts with your staff (confirm or
overturn them; overturns feed back into tuning). When you are satisfied:

1. Set the punishment commands for your ban plugin in `config.yml` (`punishments.ban-command`,
   `unban-command`; the defaults use vanilla `ban`/`pardon`). PandaEye checks at startup that the labels
   exist and belong to the same plugin.
2. Set `mode: live` and restart (or `plugman unload PandaEye` then `plugman load PandaEye-<version>`).
3. Turn on enforcement in the dashboard (Settings). Autoban, autokick and mitigations can be switched
   separately; leniency 1 (strict) to 5 (lenient), default 4.

Both switches are needed: the local `mode: live` and the dashboard's enforcement. A circuit breaker
(3 bans per hour, 5 kicks per 10 minutes by default) stops runaway punishments until `/pandaeye breaker
reset`. Experimental checks ([CHECKS 4.1](CHECKS.md)) can alert and mitigate but never kick or ban on
their own.

## Networks (Velocity)

Install `pandaeye-velocity-<version>.jar` on the proxy with the same licence key, set the same random
`session-secret` on the proxy and in every backend's `proxy.secret` (with `proxy.session-channel: true`),
and the dashboard follows players across servers. Backends work without the proxy plugin.

## Updating and removing

- Update: replace the jar and restart, or `plugman unload PandaEye`, swap the jar, `plugman load ...`.
  Unloading takes milliseconds and leaves no threads behind; events not yet sent are kept on disk and sent
  after the next start.
- Remove: `plugman unload PandaEye` and delete the jar (or delete it and restart).
- Delete a player's data: `/pandaeye forget <uuid>` (local logs) and the player page in the dashboard
  (cloud data). See [PRIVACY.md](PRIVACY.md), which also has a notice you can add to your server rules.

## Commands and permissions

`/pandaeye status | alerts [on|off] | player <name> | debug <name> | breaker [reset] | forget <uuid> |
reload` (alias `/peye`). Permissions: `pandaeye.command` (status), `pandaeye.alerts` (in-game alerts),
`pandaeye.admin` (reload, breaker, forget), all default to op; `pandaeye.bypass` exempts a player from
enforcement.
