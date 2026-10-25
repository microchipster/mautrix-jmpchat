# mautrix-jmpchat

`mautrix-jmpchat` is a Matrix appservice bridge for [JMP](https://jmp.chat/) and the Cheogram XMPP network.

Its purpose is to let a Matrix user operate a JMP-backed XMPP account from Matrix: direct chats, Cheogram phone-number chats, and related account actions are bridged through Matrix portal rooms.

This repository lives inside a `matrix-docker-ansible-deploy` workspace and is written to integrate cleanly with that playbook's appservice deployment model.

## What This Is

JMP gives users a real phone number that works through XMPP/Jabber apps such as Cheogram. This bridge connects that XMPP side to Matrix using `mautrix` `bridgev2`.

The project is not a generic bridge template anymore. It is specifically aimed at:

- Matrix <-> JMP/Cheogram XMPP bridging
- Cheogram phone-number contacts such as `+15550101100@cheogram.com`
- Cheogram multi-recipient group chats
- deployment as a self-hosted Matrix appservice, especially with `matrix-docker-ansible-deploy`

The `cheogram-android/` tree is kept around as a behavior reference while bringing this bridge closer to real Cheogram/JMP behavior.

## Current Scope

The codebase currently includes bridge logic for:

- XMPP login using a JMP/Cheogram JID and password
- per-user XMPP connections through `gosrc.io/xmpp`, with local patches in `patched-xmpp/`
- Matrix portal rooms for DMs and Cheogram-style group chats
- native `bridgev2` provisioning: `create-group`/`resolve-identifier` and contact listing work from clients that use bridge provisioning APIs
- roster fetches, bookmarks/discovery, and MUC joins
- MAM-based history sync/backfill
- management-room commands for chat creation, sync, discovery, blocking, and reporting spam
- Matrix -> XMPP handling for messages, reactions, and read receipts
- typing indicators (XEP-0085), message corrections (XEP-0308), retractions (XEP-0424/0425), and XEP-0372 mentions both ways
- encrypted XMPP payloads (OMEMO/OpenPGP) degrade to a notice instead of being dropped
- vcard-temp avatars (XEP-0054) with XEP-0153 triggers, plus an `avatar-probe` diagnostic
- MUC robustness: join health watchdog with XEP-0410 self-pings, nickname conflict handling and `muc-nick`, auto-rejoin, live XEP-0402 bookmark events, and room subject <-> Matrix topic sync in both directions
- roster management commands and CardDAV contact import (`carddav-*`)
- `geo:` location mapping and gateway service-status headlines routed to the management room
- inline media: XEP-0231 BOB payloads become Matrix media, `cid:`-only bodies degrade to a filename placeholder, and image-based custom emoji reactions are stored in Matrix without being sent to XMPP

This is still an active bridge implementation, not a finished polished product. When behavior differs from Cheogram Android, Cheogram is the reference.

## Repository Layout

- `cmd/mautrix-jmpchat/main.go`: binary entrypoint
- `connector/`: bridge implementation
- `patched-xmpp/`: local fork of `gosrc.io/xmpp` used by this bridge
- `docker-run.sh`: container entrypoint used by the image
- `Dockerfile`: container build for deployment
- `DEVEL.md`: maintainer-oriented Go/codebase walkthrough
- `docs/CHEOGRAM_ANALYSIS.md`: notes comparing this bridge to Cheogram Android and JMP behavior

## Important Implementation Notes

- The Go module replaces upstream XMPP with a local fork:

```go
replace gosrc.io/xmpp => ./patched-xmpp
```

- The binary identifies itself as `A Matrix-XMPP bridge for JMP.chat/Cheogram`.
- The bridge registers custom management-room commands in `connector/my_connector.go`.
- The Docker image persists runtime state in `/data`.

## Logging In And Basic Usage

From a Matrix user's point of view, the bridge is normally used through its management room.

Typical flow:

1. Install and start the bridge, then register its appservice with your homeserver.
2. Open a DM with the bridge bot or otherwise reach the bridge management room.
3. Run `login <jid>`.
4. When prompted, enter your JMP/Cheogram XMPP JID and password.
5. After login, let the bridge connect and create its per-user space/management-room structure.
6. Use commands like `sync`, `create`, and `list-contacts` to discover chats or create new ones.
7. Talk in the bridged portal rooms that the bridge creates.

Current login expectations:

- the username is your XMPP JID, for example `user@cheogram.com`
- the password is your JMP/Cheogram account password
- the bridge verifies the credentials by making a real XMPP connection during login

Important behavior:

- Matrix rooms created directly by a user are not automatically bridged to JMP
- use bridge-created portal rooms for actual message routing
- use `create <jid>` or `create <jid> [jid...]` to start new bridged DMs or Cheogram group chats
- use `sync` to pull in roster/bookmarks and trigger backfill of history

If a user logs in successfully, the bridge stores a user login entry, starts a per-user XMPP client, and then uses portal rooms to carry future Matrix <-> XMPP traffic.

## Management Room Commands

When the bridge is running and a user has a management room, the quick-help text currently includes:

- `login <jid>`
- `logout [login_id]`
- `logout [login_id] purge`
- `delete-all-portals yes`
- `ping`
- `sync [full|30d]`
- `sync-status`
- `create <jid> [jid...]`
- `list-contacts`
- `list-contacted`
- `block [--report] <jid>`
- `report <jid>`
- `gateway-actions [jid]`
- `gateway-form <jid> <node>`
- `gateway-submit <session> field=value`
- `list-logins [--verbose]`
- `list-users`
- `set-name <jid> <name>`
- `set-contact-name <jid> <name|--clear>`
- `muc-nick <room JID> [<nickname>|--clear]`
- `add-to-roster <jid> [name]` / `save-contact` (alias)
- `remove-from-roster <jid>`
- `search-roster <query>`
- `sync-roster`
- `carddav-login <addressbook URL> <username> <password>`
- `carddav-logout`
- `carddav-status`
- `carddav-sync [apply]`
- `carddav-clear` — remove CardDAV-sourced contact names and refresh auto-named rooms
- `clear-contact-names [--source explicit|carddav|roster] [<phone or JID>]` — clear names from one source (or all sources) and refresh auto-named rooms
- `avatar-probe <jid>`

Recent bridge-specific additions include:

- `ping`: show login state, XMPP session state, and run an XMPP server ping when connected
- `list-contacts`: show the loaded XMPP roster
- `list-contacted`: show contacts derived from bridged rooms, including split-out Cheogram group participants
- `block <jid>`: block a contact over XMPP blocking
- `block --report <jid>` / `report <jid>`: report spam using Cheogram-style XMPP reporting and block the contact

## Common User Workflows

Start a direct chat with a phone-number contact:

```text
create +15550101100@cheogram.com
```

Start a Cheogram group chat with multiple contacts:

```text
create +15550101100@cheogram.com +15550101200@cheogram.com
```

Sync existing server-side state and recent history:

```text
sync
```

Run a deeper sync/backfill:

```text
sync full
```

Inspect contacts and already-contacted users:

```text
list-contacts
list-contacted
```

Block or report spam:

```text
block +15550101100@cheogram.com
block --report +15550101100@cheogram.com
```

## Local Development

Build the binary:

```bash
go build ./cmd/mautrix-jmpchat
```

Run the bridge from source:

```bash
go run ./cmd/mautrix-jmpchat -c config.yaml -r registration.yaml
```

Generate a registration file:

```bash
go run ./cmd/mautrix-jmpchat -g -c config.yaml -r registration.yaml
```

Generate an example config file:

```bash
go run ./cmd/mautrix-jmpchat -e -c config.yaml
```

Run tests:

```bash
go test ./...
go test ./connector
```

If you use the repo's configured toolchain wrapper:

```bash
mise install
mise exec -- go test ./connector
```

## Docker Runtime Behavior

The container image is built around `/usr/bin/mautrix-jmpchat` and `docker-run.sh`.
The `Dockerfile` hardcodes the Go patch release for reproducible image builds; keep it in sync with `.mise.toml` when updating Go.

On startup:

1. If `/data/config.yaml` does not exist, the container generates it and exits.
2. If `/data/registration.yaml` does not exist, the container generates it and exits.
3. Once both files exist, the bridge starts normally from `/data`.

That means the normal first-run flow is:

1. Start the container once to get `/data/config.yaml`.
2. Edit `/data/config.yaml`.
3. Start it again to get `/data/registration.yaml`.
4. Install the registration into your homeserver.
5. Start the bridge again for normal operation.

## Configuration Notes

At minimum, you will need to review and set:

- `homeserver.address`
- `homeserver.domain`
- `appservice.address`
- `appservice.hostname`
- `appservice.port`
- `appservice.id`
- `appservice.bot.username`
- `appservice.as_token`
- `appservice.hs_token`
- `appservice.username_template`
- `database.*`
- `bridge.permissions`

The generated `registration.yaml` must match the appservice values in `config.yaml`.

For JMP/Cheogram usage, users log in with their XMPP JID and password, for example `user@cheogram.com`.

Optional `network` config keys (bridge-generated `simple-config.yaml` or the playbook role config):

- `send_typing` (default `true`): allow Matrix typing notifications to become XMPP chat states.
- `send_read_markers` (default `true`): send XMPP displayed markers for read Matrix messages.
- `send_delivery_receipts` (default `true`): request/answer XEP-0184 delivery receipts for DMs.
- `carddav.allow_insecure_http` (default `false`), `carddav.allow_private_hosts` (default `false`), `carddav.default_country_code` (default `1`): CardDAV import behavior; credentials are stored per login and only `carddav-sync apply` writes roster entries.
- `muc_nick_sync` (default `true`): renaming a MUC portal room in Matrix also changes your nickname in the XMPP room (nickname-like names only).

Cheogram gateway chats (phone numbers, group texts) never receive typing/receipt signals, since the SMS/MMS gateway cannot carry them.

## Using With matrix-docker-ansible-deploy

This repo is intended to plug into a [`matrix-docker-ansible-deploy`](https://github.com/spantaleev/matrix-docker-ansible-deploy) setup rather than act as a standalone polished installer.

In practice, that means:

- build a container image from this repository
- persist `/data`
- expose the bridge on the appservice listener configured in `config.yaml`
- register the generated `registration.yaml` with the homeserver managed by the playbook
- keep the bridge and homeserver on the same private network when possible

The exact Ansible wiring depends on how your playbook is customized, but the bridge follows the same appservice lifecycle expected by the playbook: persistent config, generated registration, then long-running service.

## JMP Context

Minimal context that matters for this bridge:

- JMP provides phone numbers and messaging/calling through XMPP/Jabber
- Cheogram is the main reference client for that network
- phone-number contacts are represented as XMPP JIDs
- group texting is represented through Cheogram-specific group JID conventions

This README is intentionally not a JMP marketing page. For product/user-facing information, see:

- <https://jmp.chat/>
- <https://f-droid.org/en/packages/com.cheogram.android/>
- <https://play.google.com/store/apps/details?id=com.cheogram.android.playstore>

## Development References

- `DEVEL.md` for a codebase-specific Go tutorial and maintenance guide
- `docs/CHEOGRAM_ANALYSIS.md` for protocol and behavior notes
- `TODO.md` for remaining bridge-parity work

## Caveats

- `example-config.yaml` still contains some template-era naming and defaults; treat it as a starting point, not authoritative product documentation.
- This bridge depends on local `patched-xmpp` behavior, so upstream library documentation is not always enough.
- Full compatibility with Cheogram/JMP behavior is still in progress.
