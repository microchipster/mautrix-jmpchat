# DEVEL

This document is for an expert Python developer with some Rust experience who now needs to maintain this bridge and barely knows Go.

It is not a generic Go tutorial. It is a tour of the Go concepts that matter in this repo, explained in the order you will encounter them while reading and changing the code.

## Start Here

This repo is a Matrix <-> XMPP bridge built on top of `mautrix`'s `bridgev2` framework.

The fastest way to orient yourself is:

1. Read `cmd/mautrix-jmpchat/main.go`.
2. Read `connector/my_connector.go`.
3. Read `connector/login.go`.
4. Read `connector/network_client.go`.
5. Read `connector/handle_matrix.go`.
6. Read the XMPP feature files like `connector/xmpp_mam.go`, `connector/xmpp_muc.go`, `connector/xmpp_gateway.go`, and `connector/xmpp_message_extensions.go`.
7. Read a few tests in `connector/*_test.go`.

If you keep that order in your head, the code stops feeling like "big Go" and starts feeling like a normal event-driven application with strong interfaces and explicit state.

## Step 1: Learn the Shape of a Go Program

Open `cmd/mautrix-jmpchat/main.go` first.

```go
package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"jmpchat/connector"
)

func main() {
	connector := &connector.MyConnector{}

	m := mxmain.BridgeMain{
		Name:      "jmpchat",
		Connector: connector,
	}

	m.Run()
}
```

That tiny file teaches several Go basics:

- A file belongs to exactly one package. Here it is `package main`, which means this package builds into an executable.
- Imports are explicit and unused imports are compile errors.
- `MyConnector{}` is a struct literal. `&connector.MyConnector{}` means "allocate one and give me a pointer to it".
- There is no class constructor syntax. You either instantiate a struct directly or call a normal function like `NewMyConnector(...)`.
- Capitalization matters. Names starting with an uppercase letter are exported from the package. Lowercase names are package-private.

Python comparison:

- `package` is closer to a module directory than a class.
- Export rules are based on capitalization, not `__all__` or conventions.
- There is no implicit object model behind everything. Methods are just functions attached to a type.

Rust comparison:

- Packages feel like modules plus visibility.
- Struct literals and explicit imports will feel familiar.
- There is much less syntax around traits and lifetimes.

## Step 2: Understand the Module Boundary

Open `go.mod`.

```go
module jmpchat

go 1.25.0

toolchain go1.25.5

require (
	gosrc.io/xmpp v0.5.1
	maunium.net/go/mautrix v0.27.0
)

replace gosrc.io/xmpp => ./patched-xmpp
```

Important ideas:

- `module jmpchat` means local imports can use `jmpchat/...`.
- `require` is your dependency list.
- `replace` is extremely important here: this repo intentionally uses a local patched copy of the XMPP library.

That `replace` line is the kind of thing a maintainer must notice early. If behavior is weird, do not assume upstream `gosrc.io/xmpp` docs exactly match runtime behavior. Check `patched-xmpp/` too.

For the upstream-tracking workflow and the local patch checklist, see `docs/PATCHED_XMPP.md`.

## Step 3: Structs and Methods Replace Classes

Open `connector/my_connector.go`.

```go
type MyConnector struct {
	log    zerolog.Logger
	bridge *bridgev2.Bridge
}
```

This is the Go replacement for a small class with fields.

Methods are declared separately:

```go
func (c *MyConnector) Init(br *bridgev2.Bridge) {
	c.bridge = br
}
```

The `(c *MyConnector)` part is the receiver. You can read it as "this function is a method on `*MyConnector`".

Important rule:

- A pointer receiver like `*MyConnector` can mutate the struct and avoids copying it.
- This repo mostly uses pointer receivers for stateful types like `MyConnector` and `MyNetworkClient`.

If you come from Python, think:

- `func (c *MyConnector) Init(...)` is just `def init(self, ...)` with explicit receiver type.

If you come from Rust, think:

- It is loosely like `impl MyConnector { fn init(&mut self, ...) }`, except receiver syntax is simpler and there is no borrow checker enforcing aliasing rules.

## Step 4: Interfaces Are Used Everywhere

This repo relies heavily on interfaces from `mautrix`.

In `connector/my_connector.go`:

```go
var _ bridgev2.NetworkConnector = (*MyConnector)(nil)
```

In `connector/login.go`:

```go
var _ bridgev2.LoginProcessUserInput = (*SimpleLogin)(nil)
```

In `connector/network_client.go`:

```go
var _ bridgev2.NetworkAPI = (*MyNetworkClient)(nil)
```

This is a compile-time assertion. It says: "a `*MyConnector` must satisfy `bridgev2.NetworkConnector`". If it stops satisfying the interface, the code no longer compiles.

This pattern matters a lot in Go because interfaces are satisfied implicitly. There is no `implements` keyword.

That has two consequences:

1. The code stays lightweight.
2. It is easy to accidentally stop implementing an interface during a refactor.

That `var _ ...` line is the standard guardrail.

## Step 5: There Are No Exceptions

Go uses explicit error returns.

From `connector/login.go`:

```go
func (sl *SimpleLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	username := input["username"]
	if username == "" {
		return nil, fmt.Errorf("username cannot be empty")
	}

	client, err := xmpp.NewClient(...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to XMPP: %w", err)
	}

	...
}
```

This is one of the biggest shifts from Python.

- Errors are values.
- Functions often return `(result, error)`.
- The normal style is "do something, check `err`, return early if needed".

The codebase uses this style consistently. Lean into it. Do not fight it.

`%w` in `fmt.Errorf` wraps another error so callers can preserve the cause chain.

Practical maintenance rule:

- In Go, early returns are normal and desirable.
- Avoid deeply nested `if` blocks when a quick `if err != nil { return ... }` will do.

## Step 6: `context.Context` Is Part of the Function Signature

You will see `context.Context` everywhere:

- `CreateLogin(ctx context.Context, ...)`
- `HandleMatrixMessage(ctx context.Context, ...)`
- `requestMAM(ctx context.Context, ...)`
- `WaitForConnected(ctx context.Context)`

In this codebase, `ctx` carries three things:

1. Cancellation and timeouts.
2. Request-scoped logging context.
3. A standard way to thread operation lifetime through library code.

Example from `connector/xmpp_gateway.go`:

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

Example from `connector/network_client.go`:

```go
connectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
if !nc.WaitForConnected(connectCtx) {
	...
}
cancel()
```

The Go idiom is:

- Accept `ctx` as the first argument.
- Derive narrower contexts with timeouts when needed.
- `defer cancel()` soon after creating them.

Python comparison:

- It is not exactly `asyncio` cancellation, but it serves a similar coordination role.

Rust comparison:

- It fills some of the role that explicit cancellation tokens and deadline wrappers often fill.

## Step 7: Nil, Zero Values, Maps, and Slices Matter

Go's zero values are used constantly in this repo.

Examples:

- `string` zero value is `""`
- `bool` zero value is `false`
- `time.Time` zero value is a special empty timestamp
- pointers default to `nil`
- maps and slices default to `nil`

This affects how you read the code.

### Slices

Slices are dynamic views over arrays. In practice, just think "cheap dynamic sequence".

Examples in this repo:

- `[]string` for participants, instructions, reactions, tags
- `[]XDataField` for XMPP forms
- `[]stanza.DiscoItem` for discovered commands

Typical pattern:

```go
participants := make([]string, 0, len(args))
for _, raw := range args {
	...
	participants = append(participants, jid)
}
```

`make([]string, 0, len(args))` creates an empty slice with preallocated capacity. This is an optimization, not a semantic requirement.

### Maps

Maps are hash tables.

Examples in this repo:

- `map[string]bool` for sets like `rosterItems`, `mucRooms`, `bookmarkedRooms`
- `map[string]string` for occupant tracking
- `map[string]PendingGatewayForm` in login metadata

Common Go trick used throughout the repo:

```go
users := map[string]bool{}
users[jid] = true
```

That is using `map[string]bool` as a set.

Another common pattern:

```go
seen := make(map[string]struct{})
```

Using `struct{}` as the value means "presence only, no payload".

### Nil maps are readable but not writable

This is subtle and important.

- Reading from a nil map is fine.
- Writing to a nil map panics.

That is why `network_client.go` has many blocks like:

```go
if nc.mucRooms == nil {
	nc.mucRooms = map[string]bool{}
}
```

### Pointers and optional data

Go often uses pointers to mean "optional".

Examples:

- `*bridgev2.Bridge`
- `*bridgev2.UserLogin`
- `*time.Time` in metadata
- `*XMPPReport` or `*XMPPStanzaID` in XML payload structs

If you see a pointer field, assume nil is a normal state unless the code proves otherwise.

## Step 8: Tags Drive Serialization

Open `connector/types.go` and `connector/xmpp_message_extensions.go`.

From `types.go`:

```go
type PortalMetadata struct {
	RemoteRoomID        string              `json:"remote_room_id,omitempty"`
	OtherUserID         networkid.UserID    `json:"other_user_id,omitempty"`
	LastSyncedAt        *time.Time          `json:"last_synced_at,omitempty"`
	LastServerMessageID string              `json:"last_server_message_id,omitempty"`
	IsMUC               bool                `json:"is_muc,omitempty"`
}
```

From `xmpp_message_extensions.go`:

```go
type XMPPReply struct {
	stanza.MsgExtension
	XMLName xml.Name `xml:"urn:xmpp:reply:0 reply"`
	To      string   `xml:"to,attr,omitempty"`
	ID      string   `xml:"id,attr,omitempty"`
}
```

Those backtick strings are struct tags.

- `json:"..."` tells the JSON encoder how to store database metadata.
- `xml:"..."` tells the XML encoder how to build and parse XMPP stanzas.
- `omitempty` means "leave this field out if it has the zero value".

This repo uses Go structs as the source of truth for protocol shapes. That is a very Go way to model external formats.

Python comparison:

- Think dataclasses or Pydantic models with explicit field metadata.

Rust comparison:

- Think `serde` derives plus field attributes, except Go uses reflection and tags rather than macros.

## Step 9: `init()` Exists, but Use It Sparingly

Several XMPP files register stanza extensions in `init()`:

- `connector/xmpp_gateway.go`
- `connector/xmpp_mam.go`
- `connector/xmpp_muc.go`
- `connector/xmpp_blocking.go`

Example pattern:

```go
func init() {
	stanza.TypeRegistry.MapExtension(...)
}
```

`init()` runs automatically when the package is initialized.

In this repo it is used for package-level registration side effects: teaching the XMPP library how to decode custom XML payloads.

That is an idiomatic use of `init()`. Do not use it for complicated runtime setup or hidden business logic.

## Step 10: Read Interface-Driven Lifecycle Code

`connector/my_connector.go` defines the bridge lifecycle.

The important methods are:

- `Init`
- `GetName`
- `GetCapabilities`
- `GetDBMetaTypes`
- `GetLoginFlows`
- `CreateLogin`
- `LoadUserLogin`
- `Start`
- `Stop`

This is the contract between your code and `bridgev2`.

### `GetDBMetaTypes`

```go
func (c *MyConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any { return &PortalMetadata{} },
		Ghost: func() any { return &GhostMetadata{} },
		UserLogin: func() any { return &LoginMetadata{} },
	}
}
```

Two Go details matter here:

- `any` is just an alias for `interface{}`.
- Functions are first-class values and are used here as factories.

### `LoadUserLogin`

This method is one of the most important in the repo. It creates a per-login client:

```go
client := &MyNetworkClient{...}
login.Client = client
go client.Connect(context.Background())
```

That tells you the architecture:

- `MyConnector` is global bridge-level glue.
- `MyNetworkClient` is per-user XMPP state.

If you need to change actual protocol behavior, most of the time you will be editing `MyNetworkClient` methods, not `MyConnector`.

## Step 11: Concurrency Is Explicit and Simple

The concurrency model in this repo is very normal Go:

- goroutines for background work
- channels for one-shot signaling
- mutexes for shared mutable state

### Goroutines

You launch a goroutine with `go`:

```go
go client.Connect(context.Background())
go nc.runFullSync(ctx, notify)
go nc.runAutomaticDMCatchup(context.Background())
```

A goroutine is much lighter than an OS thread. Think "spawn a concurrent task".

Important warning for Python developers:

- Goroutines are cheap, but not magical.
- Shared mutable state still needs protection.
- Panics in goroutines are still bad.

### Channels

The clearest example is `connectedCh chan struct{}` in `MyNetworkClient`.

`WaitForConnected` does:

```go
select {
case <-nc.connectedCh:
	return true
case <-ctx.Done():
	return false
}
```

This is a classic Go pattern:

- A `chan struct{}` carries no data.
- Closing it acts like broadcasting "the event happened".
- Waiters unblock by receiving from the channel.

That is how this code models "connection became ready".

### Mutexes

`MyNetworkClient` has many mutexes:

- `connectMu`
- `syncMu`
- `mucBackfillMu`
- `mucHistoryMu`
- `mamQueryMu`
- `callSessionsMu`
- `bridgeStateMu`
- `rosterMu`

Example:

```go
rosterMu sync.RWMutex
```

The code uses:

- `Lock` / `Unlock` for writes
- `RLock` / `RUnlock` for read-only access

This is conventional Go shared-state concurrency. There is no actor framework hiding the state machine.

Maintenance rule:

- When adding a new field to `MyNetworkClient`, decide whether it is immutable after setup, single-threaded by convention, or needs a lock.
- If you are not sure, assume multiple goroutines can touch it.

## Step 12: `defer` Is a Core Readability Tool

You will see `defer` everywhere:

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

```go
nc.syncMu.Lock()
defer nc.syncMu.Unlock()
```

`defer` means "run this when the current function returns".

Use it for:

- unlocking mutexes
- cancelling derived contexts
- stopping timers and tickers
- closing files or response bodies

This keeps cleanup physically close to resource acquisition.

## Step 13: Type Assertions Show Up at Runtime Boundaries

Because interfaces are implicit, code sometimes needs to recover the concrete type.

Example from `my_connector.go`:

```go
if proc, ok := c.bridge.Commands.(*commands.Processor); ok {
	proc.AddHandler(CommandSync)
}
```

Example from many files:

```go
meta, ok := nc.login.Metadata.(*LoginMetadata)
```

This is a type assertion.

- `value.(T)` means "I believe this interface value holds a `T`".
- `value.(T)` can panic if wrong.
- `value.(T)` in the two-result form, `v, ok := value.(T)`, is the safe version.

The repo consistently uses the safe form. Keep doing that.

## Step 14: The Repo Models Protocols with Small Struct Types

Open these files:

- `connector/xmpp_message_extensions.go`
- `connector/xmpp_mam.go`
- `connector/xmpp_gateway.go`
- `connector/xmpp_muc.go`
- `connector/xmpp_blocking.go`

The pattern is the same in all of them:

1. Define a struct that matches an XML payload.
2. Give it `Namespace()` and sometimes `GetSet()` methods if the XMPP library expects them.
3. Register it in `init()`.
4. Build values of that struct when sending IQs/messages.
5. Type-assert the parsed payload when receiving a reply.

For example, `requestGatewayPrompt` in `connector/xmpp_gateway.go`:

1. Builds an IQ packet.
2. Sends it with `SendIQ`.
3. Waits on a channel for the response.
4. Checks `resp.Type`.
5. Asserts `resp.Payload.(*GatewayQuery)`.

That is the general style for protocol code in this repo.

If you add a new XMPP extension, copy an existing one and stay consistent.

## Step 15: Follow the Main Event Flows

The bridge becomes easier once you trace the major directions of data flow.

### Matrix -> XMPP

Read `connector/handle_matrix.go`.

`HandleMatrixMessage`:

1. Validates connection state.
2. Figures out DM vs MUC from portal metadata.
3. Builds a `stanza.Message`.
4. Attaches XMPP extensions for media, replies, receipts, markers, reactions, or origin IDs.
5. Sends with `nc.sendXMPP(...)`.
6. Returns a `database.Message` description for bridge bookkeeping.

This file is a good place to learn common Go control flow because it is just plain business logic with explicit branches and small helper calls.

### XMPP -> Matrix

Most of that flow lives in `connector/network_client.go`.

The client registers handlers on the XMPP router:

```go
nc.xmppRouter.HandleFunc("message", nc.handleXMPPMessage)
nc.xmppRouter.HandleFunc("presence", nc.handleXMPPPresence)
nc.xmppRouter.HandleFunc("iq", nc.handleXMPPIq)
```

Then those handlers inspect packets and queue bridge events like `simplevent.ChatResync` and `simplevent.ChatInfoChange`.

That is the real event bridge: decode remote protocol state, translate it into `bridgev2` events, and let the framework create or update Matrix objects.

### Full sync and backfill

Read `StartFullSync` and `runFullSync` in `connector/network_client.go`, then `connector/history_sync.go`, then `connector/xmpp_mam.go`.

This flow shows several core Go patterns at once:

- lock-protected mutable state
- background goroutines
- timeout contexts
- helper functions operating on plain structs
- explicit state transitions using booleans and timestamps

It is a good example of how Go code often builds state machines without fancy abstractions.

## Step 16: Go Does Not Hide Mutation

A lot of repo maintenance work is really about noticing where mutation happens.

Examples:

- `portal.Metadata = meta`
- `login.Client = client`
- `nc.rosterItems[bareJID] = true`
- `nc.fullSyncRequested = true`
- `meta.LastSyncedAt = &timestamp`

From `connector/history_sync.go`:

```go
if meta == nil {
	meta = &PortalMetadata{}
	portal.Metadata = meta
}
```

Because the code uses pointers heavily, once you obtain a pointer to a struct, mutating fields changes shared state directly.

That sounds obvious, but in Go there is less visual ceremony than in Rust, so it is easier to miss.

## Step 17: Tests Are Straightforward and Table-Friendly

Read `connector/commands_test.go` first.

```go
func TestSplitCheogramGroupContacts(t *testing.T) {
	t.Parallel()

	got := splitCheogramGroupContacts("+15551230001,+15551230002@cheogram.com")
	want := []string{"+15551230001@cheogram.com", "+15551230002@cheogram.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCheogramGroupContacts() = %v, want %v", got, want)
	}
}
```

Key testing norms used here:

- Tests live in `*_test.go` files.
- Test functions are named `TestXxx`.
- `t.Fatalf` aborts the current test immediately.
- `t.Parallel()` marks a test safe to run concurrently with others.

You will also see focused helper tests in files like:

- `connector/xmpp_attachment_test.go`
- `connector/xmpp_call_test.go`
- `connector/xmpp_gateway_test.go`
- `connector/xmpp_mam_test.go`
- `connector/xmpp_reply_test.go`
- `connector/xmpp_text_test.go`

Go testing is intentionally boring. That is good. Keep tests small and direct.

## Step 18: Practical Go Idioms Used in This Repo

These are worth recognizing immediately.

### Short variable declarations

```go
client, err := xmpp.NewClient(...)
```

`:=` both declares and initializes variables.

### Range loops

```go
for _, item := range payload.Items {
	...
}
```

Ignore the index with `_` if you do not need it.

### Switch on concrete payload type

In `handleXMPPIq`:

```go
switch payload := iq.Payload.(type) {
case *stanza.RosterItems:
	...
case *BookmarkStorage:
	...
case *stanza.DiscoItems:
	...
}
```

That is a type switch. It is the cleanest way to branch on interface-backed concrete values.

### Package-level helper functions

Files like `connector/jid.go` and `connector/history_sync.go` include simple functions such as:

- `EscapeJID`
- `UnescapeJID`
- `MakeUserID`
- `shouldUpdateHistoryProgress`

Go code often prefers small package-level helpers instead of forcing everything onto a class-like type.

### Small data structs instead of giant inheritance trees

The repo models many concepts as plain structs with a few fields and methods.

That is the normal Go style. Prefer composition and small helpers over deep type hierarchies.

## Step 19: A Python Developer's Translation Guide

When the code feels unfamiliar, map it mentally like this:

- package = a module namespace with directory-based organization
- struct = a lightweight class with fields only
- method with receiver = a normal instance method
- interface = structural protocol or Rust trait object boundary
- `nil` = `None`
- `[]T` = list-like sequence
- `map[K]V` = dict-like map
- `chan T` = typed queue or signal channel
- goroutine = lightweight spawned task
- `defer` = guaranteed cleanup at function exit
- `context.Context` = explicit cancellation/deadline token
- `error` return = explicit exception value instead of raised control flow

The biggest mindset change is this:

Go code is usually easier to maintain when it stays plain.

If you are tempted to make it more clever, that is often the moment to stop.

## Step 20: A Rust Developer's Translation Guide

Things that will feel familiar:

- explicit imports
- structs with methods
- interfaces as contracts
- explicit error propagation
- preference for small helper functions

Things that will feel strange:

- no borrow checker
- lots of `nil`
- mutation is easier and therefore more dangerous
- interface satisfaction is implicit
- reflection-based serialization via tags instead of derive macros

When modifying shared state in this repo, think with your Rust brain even though Go will not force you to. Be conservative about concurrency and aliasing.

## Step 21: What to Read When Changing Specific Features

If you need to work on a particular area, start here:

- Login flow: `connector/login.go`
- Management room commands: `connector/commands.go`
- Matrix -> XMPP messaging: `connector/handle_matrix.go`
- Connection lifecycle and incoming XMPP handling: `connector/network_client.go`
- JID escaping and bridge IDs: `connector/jid.go`
- History sync state tracking: `connector/history_sync.go`
- MAM backfill: `connector/xmpp_mam.go`
- Group chat creation and MUC behavior: `connector/xmpp_muc.go`
- Gateway and XEP-0050 commands: `connector/xmpp_gateway.go`
- XMPP payload types and extensions: `connector/xmpp_message_extensions.go`, `connector/xmpp_blocking.go`, `connector/xmpp_bookmarks.go`, `connector/xmpp_receipts.go`

## Step 22: How to Make Safe Changes Here

When editing this repo, a good maintenance loop is:

1. Find the narrowest file that owns the behavior.
2. Check the relevant interface contract in `my_connector.go`, `handle_matrix.go`, or `network_client.go`.
3. Look for state shared across goroutines in `MyNetworkClient` before mutating fields.
4. If you add protocol payloads, follow the existing XML-tag plus `init()` registration pattern.
5. If you add persisted metadata, update `connector/types.go` and the save/load path that touches it.
6. Add or extend a focused unit test in `connector/*_test.go`.
7. Run `go test ./connector` at minimum.

## Step 23: Commands You Will Actually Use

From the repo root:

```bash
go build ./cmd/mautrix-jmpchat
go test ./...
go test ./connector
go run ./cmd/mautrix-jmpchat -c config.yaml -r registration.yaml
go run ./cmd/mautrix-jmpchat -g -c config.yaml -r registration.yaml
```

If you use the repo's preferred toolchain wrapper:

```bash
mise exec go@1.25.5 -- go test ./connector
```

## Final Mental Model

This codebase is not "Go magic". It is mostly:

- a framework interface layer in `my_connector.go`
- a per-user stateful client in `network_client.go`
- message translation in `handle_matrix.go`
- protocol definitions in the `xmpp_*.go` files
- JSON/XML serialization through struct tags
- explicit error handling, explicit concurrency, explicit state transitions

If you remember those six ideas, you can maintain this repo effectively without becoming a general Go expert first.

The right strategy is to learn Go through this bridge, not to stop and learn all of Go before touching the bridge.
