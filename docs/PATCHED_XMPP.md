# Patched XMPP Module

## TLDR For Maintainers

`patched-xmpp/` is a git submodule, not an in-tree fork. It points at our `go-xmpp` integration branch:

```sh
https://github.com/microchipster/go-xmpp.git
branch: jmpchat-patches
```

The submodule URL intentionally uses public HTTPS on GitHub so deployment hosts can run recursive clones without SSH host-key or deploy-key setup.

## Repository Distribution

There are two repositories involved, and each has a distribution remote plus a private mirror:

- `mautrix-jmpchat` distribution remote: `https://codeberg.org/microchipster/mautrix-jmpchat.git`
- `mautrix-jmpchat` push/mirror remote: `ssh://git@git.example.com:2222/microchipster/jmpchat.git`
- `go-xmpp` submodule distribution remote: `https://github.com/microchipster/go-xmpp.git`
- `go-xmpp` push/mirror remote: `ssh://git@git.example.com:2222/microchipster/go-xmpp.git`

The playbook should clone `mautrix-jmpchat` from Codeberg. Its recursive submodule checkout should then clone `patched-xmpp/` from GitHub. The git host is kept as a mirror, not as the deploy-facing URL.

Recommended local remotes for this bridge checkout:

```sh
git remote set-url codeberg ssh://git@codeberg.org/microchipster/mautrix-jmpchat.git
git remote set-url origin ssh://git@git.example.com:2222/microchipster/jmpchat.git
```

Recommended local remotes for `/home/user/src/repos/go-xmpp`:

```sh
git remote set-url origin ssh://git@github.com/microchipster/go-xmpp
git remote set-url mirror ssh://git@git.example.com:2222/microchipster/go-xmpp.git
git remote set-url upstream https://github.com/FluuxIO/go-xmpp.git
```

When publishing bridge changes, push Codeberg for distribution and the git host as the mirror:

```sh
git push codeberg main
git push origin main
```

When publishing `go-xmpp` changes, push GitHub for submodule distribution and the git host as the mirror:

```sh
git push origin main jmpchat-patches pr/protocol-edge-cases pr/limit-xml-node-depth pr/add-scram-sasl-auth pr/fix-stream-management-resume pr/command-rsm-compat
git push origin main jmpchat-patches pr/protocol-edge-cases pr/limit-xml-node-depth pr/add-scram-sasl-auth pr/fix-stream-management-resume pr/command-rsm-compat
```

Keep the bridge's root `go.mod` replacement:

```go
replace gosrc.io/xmpp => ./patched-xmpp
```

To rebase our XMPP work against upstream:

```sh
cd /home/user/src/repos/go-xmpp
git fetch upstream
git switch jmpchat-patches
git rebase upstream/main
```

If the rebase conflicts, inspect the conflicted files:

```sh
git status --short
git diff --name-only --diff-filter=U
```

Resolve conflicts manually. Preserve the local behavior listed in "Why This Fork Exists", especially SCRAM auth in `auth.go`, XEP-0440 parsing in `stanza/stream_features.go`, XML depth limiting in `stanza/node.go`, stream-management resume behavior in `client.go`, `session.go`, `router.go`, and `xmpp_transport.go`, and the exported `Config.StreamManagementResume` field.

Do not blindly choose `--theirs` for `auth.go`; upstream currently lacks the SCRAM functionality this bridge needs. If the rebase is clearly wrong and you have not completed it, abort it:

```sh
git rebase --abort
```

After resolving conflicts:

```sh
gofmt -w .
git grep -n -E '<<<<<<<|=======|>>>>>>>' || true
git add <resolved-files>
git rebase --continue
```

The conflict-marker check must print nothing before continuing.

Then test and update the remote integration branch:

```sh
rtk go get -u ./...
rtk go mod tidy
rtk go test ./...
git push --force-with-lease origin jmpchat-patches
git push --force-with-lease origin jmpchat-patches
```

Update this bridge to the newly tested submodule commit:

```sh
cd /home/user/src/repos/matrix-docker-ansible-deploy/mautrix-jmpchat
git submodule update --init --recursive
git -C patched-xmpp fetch origin
git -C patched-xmpp checkout jmpchat-patches
git -C patched-xmpp reset --hard origin/jmpchat-patches
rtk go get -u ./...
rtk go mod tidy
rtk go test ./...
(cd patched-xmpp && rtk go test ./...)
git add patched-xmpp
git commit -m "Update patched-xmpp"
```

## PR Branches

The integration branch is a stack of upstream-friendly commits. Each topic also has its own branch in the `go-xmpp` fork:

- `pr/protocol-edge-cases`: fixes the `NewClient` README example, server-only JID rendering, and SASL failure reason decoding.
- `pr/limit-xml-node-depth`: adds generic XML node depth limiting.
- `pr/add-scram-sasl-auth`: adds SCRAM auth plus Cheogram-aligned SASL hardening, downgrade protection, channel-binding parsing, and legacy GS2 compatibility.
- `pr/fix-stream-management-resume`: fixes stream-management resume behavior used by this bridge.
- `pr/command-rsm-compat`: preserves command/RSM compatibility from the current bridge patch set; review carefully before proposing upstream.

To refresh a PR branch before opening or updating a PR:

```sh
cd /home/user/src/repos/go-xmpp
git fetch upstream
git switch pr/add-scram-sasl-auth
git rebase upstream/main
rtk go get -u ./...
rtk go mod tidy
rtk go test ./...
git push --force-with-lease origin pr/add-scram-sasl-auth
git push --force-with-lease origin pr/add-scram-sasl-auth
```

Repeat with the relevant branch name. Use `--force-with-lease`, not plain `--force`, because these branches are intentionally rebased.

## Current Baseline

The local `go-xmpp` fork was rebased against upstream `main` at commit `784ff58cc201e448b671096b2f6be34f3785a9bd`.

This bridge currently pins the `patched-xmpp` submodule to integration commit `22f4f6cb463e22a147eb3aff5a8f8ae0f247171c` on `jmpchat-patches`.

## Go And Dependency Policy

Do not hold this bridge or the `go-xmpp` fork on older Go or dependency versions for convenience. Update code to follow the latest available Go release and dependency APIs instead of pinning old versions.

`.mise.toml` is the concrete Go patch release used for maintainer builds. The `Dockerfile` must also hardcode the same Go patch release because Docker builds cannot depend on an external helper script in this deployment flow. Keep these two values in sync when updating Go.

Keep the module `go` directive at the latest Go language version supported by dependencies, but do not add a `toolchain` directive just to pin a patch release.

When dependencies or Go itself need to move forward, update code to match the new APIs, then run the verification commands below.

## Why This Fork Exists

Do not remove the `replace` directive or the submodule unless these local changes have been upstreamed or replaced another way:

- SCRAM password auth support in `auth.go`, including `SCRAM-SHA-512`, `SCRAM-SHA-256`, and `SCRAM-SHA-1`.
- Cheogram-aligned SCRAM hardening: minimum iteration count `4096`, GS2 channel-binding signaling when advertised, strict SCRAM attribute parsing, duplicate attribute rejection, reserved `m` rejection, and XEP-0474 downgrade hash verification.
- XEP-0440 SASL channel-binding feature parsing in `stanza/stream_features.go` so downgrade protection can include advertised binding names.
- XML generic node depth limiting in `stanza/node.go` to reject excessively nested XML.
- Stream-management resume fixes across `client.go`, `session.go`, `router.go`, and `xmpp_transport.go` that this bridge relies on for reconnect behavior.
- Exported `Config.StreamManagementResume`, used by bridge configuration in `connector/network_client.go`.
- Local tests in `auth_test.go` and `stanza/node_test.go` that guard the security-sensitive behavior.

## Verification

Run both modules after every submodule update:

```sh
rtk go test ./...
(cd patched-xmpp && rtk go test ./...)
```

If using a direct Go toolchain instead of `rtk`:

```sh
go test ./...
(cd patched-xmpp && go test ./...)
```
