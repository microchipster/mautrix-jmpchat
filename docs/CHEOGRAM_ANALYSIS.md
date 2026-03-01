# Cheogram Android - JMP.chat Bridge Analysis

This document analyzes the cheogram-android (JMP.chat) Android client to understand how it communicates with the JMP.chat XMPP server. The goal is to replicate this behavior in a Go-based Matrix-to-JMP.chat bridge.

## Table of Contents

1. [Account Configuration](#account-configuration)
2. [XMPP Connection Flow](#xmpp-connection-flow)
3. [Authentication Mechanisms](#authentication-mechanisms)
4. [Post-Login Features](#post-login-features)
5. [Key Differences from Our Current Implementation](#key-differences-from-our-current-implementation)
6. [Implementation Recommendations](#implementation-recommendations)

---

## Account Configuration

### Basic Account Data

From `Account.java` and `AccountConfiguration.java`:

```json
{
  "protocol": "xmpp",
  "address": "user@cheogram.com",  // Bare JID (no resource)
  "password": "secret"
}
```

**Account Entity Fields (from Account.java):**
- `username` - The local part of the JID (before @)
- `server` - The domain part of the JID (after @)
- `password` - The password/credential
- `hostname` - Optional server hostname override (DNS SRV usually used)
- `port` - Default 5222 (can be overridden)
- `resource` - XMPP resource identifier
- `pinnedMechanism` - Optional pinned SASL mechanism
- `pinnedChannelBinding` - Optional pinned channel binding
- `fastMechanism` / `fastToken` - For fast authentication (SASL 2.0)

### Cheogram-specific Configuration

From `ProvisioningUtils.java` and `AccountConfiguration.java`:

The cheogram app uses a simple JSON configuration format:
```json
{
  "protocol": "xmpp",
  "address": "user@cheogram.com",
  "password": "password"
}
```

Validation rules:
- Protocol must be "xmpp"
- Address must be a bare JID (not a full JID with resource, not just a domain)
- Password must be non-empty

---

## XMPP Connection Flow

### Connection Process (from XmppConnection.java)

1. **DNS Resolution**
   - If no hostname specified, uses DNS SRV records for `_xmpp-client._tcp.<domain>`
   - Falls back to direct connection to domain on port 5222

2. **Socket Connection**
   - Connects to resolved hostname:port
   - Default timeout: `Config.SOCKET_TIMEOUT * 1000` ms

3. **Stream Initialization**
   - Opens XMPP stream with `<?xml version='1.0'?>`
   - Expects stream features from server

4. **TLS/StartTLS**
   - Checks if server supports StartTLS
   - Upgrades to TLS if available (unless disabled)

5. **Authentication** (see next section)

6. **Resource Binding**
   - After successful auth, binds a resource
   - Can use XMPP BIND (RFC 6120) or BIND2 (XEP-0386)

7. **Session Establishment**
   - Optionally establishes XMPP session
   - Enables Stream Management (XEP-0198) if supported

8. **Post-Connect Processing**
   - Requests roster (contact list)
   - Requests disco info from server
   - Sets presence (available)
   - Enables CARBONS if supported
   - Enables MAM (Message Archive Management) if supported

---

## Authentication Mechanisms

### Supported SASL Mechanisms (from SaslMechanism.java)

The cheogram-android client supports these mechanisms in priority order:

1. **SCRAM-SHA-512-PLUS** (channel binding)
2. **SCRAM-SHA-256-PLUS** (channel binding)
3. **SCRAM-SHA-1-PLUS** (channel binding)
4. **SCRAM-SHA-512**
5. **SCRAM-SHA-256**
6. **SCRAM-SHA-1**
7. **PLAIN**
8. **DIGEST-MD5** (legacy)
9. **ANONYMOUS**
10. **EXTERNAL** (certificate-based)

### SCRAM Implementation Details

From `ScramMechanism.java`:

- Uses PBKDF2 with SHA-1/SHA-256/SHA-512
- Minimum iteration count: 4096 (RFC 5802 recommends this)
- Client nonce: Random 100-character string
- GS2 header format: `p=<channel-binding>,,` or `y,,` (for no channel binding)
- First message format: `n=<escaped-username>,r=<nonce>`
- Supports both SASL1 and SASL2 (XEP-0388)

### SASL 2.0 Support

The app supports SASL 2.0 (XEP-0388) which includes:
- Fast re-authentication using hashed tokens
- Authorization identifier support
- Different flow for quick start

Key methods in `XmppConnection.java`:
```java
private void authenticate(final SaslMechanism.Version version)
private void authenticate()  // Tries SASL2 first, falls back to SASL1
```

### Our Current Implementation Gap

Our current `gosrc.io/xmpp` library only supports:
- PLAIN
- X-OAUTH2

**We need to add:**
- SCRAM-SHA-1 (minimum for JMP.chat compatibility)
- Ideally: SCRAM-SHA-256, SCRAM-SHA-512

The SCRAM-SHA-1 implementation we added earlier should work, but we need to verify it matches the cheogram behavior.

---

## Post-Login Features

### Presence Management (from PresenceManager.java)

After authentication, the app:

1. **Sends Initial Presence**
   - Type: `available` (online)
   - May include status message
   - May include priority
   - May include caps (client capabilities)

2. **Handles Presence Updates**
   - Tracks online/offline/away/dnd/xa states
   - Handles presence from contacts
   - Handles presence subscription requests

### Roster Management (from RosterManager.java)

1. **Requests Full Roster**
   - Sends IQ get for roster
   - Parses roster items
   - Stores in local database

2. **Handles Roster Pushes**
   - Server can push roster updates
   - Must respond to roster pushes

### Feature Discovery (Disco)

From `DiscoManager.java`:

The app queries server disco info to discover:
- Server capabilities
- Available features
- HTTP upload service
- PEP support
- MAM support
- Message carbons support

### Message Handling

The app supports:
- **Chat messages** (1:1)
- **Group chat** (MUC - Multi-User Chat)
- **Carbons** (message forwarding to other clients)
- **MAM** (Message Archive Management)
- **Receipts** (delivery, read)
- **Typing indicators**
- **Chat states** (composing, paused, etc.)

### File Transfer

Supports Jingle (XEP-0166) for file transfer:
- SOCKS5 bytestreams (S5B)
- In-band bytestreams (IBB)
- WebRTC data channels

---

## Key Differences from Our Current Implementation

### 1. Authentication
- **Cheogram**: Full SCRAM-SHA-1/256/512 support
- **Our Bridge**: Only PLAIN (we added SCRAM-SHA-1, need to verify)

### 2. Connection Features
- **Cheogram**: DNS SRV, StartTLS, TLS, Stream Management, SM resume
- **Our Bridge**: Basic connection, limited feature support

### 3. Post-Login
- **Cheogram**: Full roster, presence, disco, MAM, carbons
- **Our Bridge**: Basic message passing

### 4. Presence Handling
- **Cheogram**: Full presence subscription management
- **Our Bridge**: Minimal presence support

### 5. Message Types
- **Cheogram**: Chat, MUC, receipts, typing, correction
- **Our Bridge**: Basic message bridging

---

## Implementation Recommendations

### 1. Priority: Fix Authentication

The SCRAM-SHA-1 implementation we added needs testing against a real JMP.chat server to ensure it matches cheogram's behavior. Key things to verify:

- PBKDF2 key derivation
- Nonce handling
- Server signature verification
- Error handling

### 2. Add Missing SASL Mechanisms

For full compatibility, add:
- SCRAM-SHA-256
- SCRAM-SHA-512
- Channel binding (SCRAM-SHA-*-PLUS)

### 3. Add DNS SRV Support

Implement proper DNS SRV record resolution:
- Query `_xmpp-client._tcp.<domain>`
- Fall back to direct connection

### 4. Implement Full Post-Login Flow

After authentication:
1. Request roster
2. Send initial presence
3. Query server disco
4. Enable desired features (carbons, MAM)

### 5. Implement Presence Management

- Track presence state
- Handle presence stanzas
- Manage subscriptions

### 6. Message Feature Parity

Add support for:
- Delivery receipts
- Read receipts
- Typing indicators
- Message correction
- MUC (if needed)

---

## JMP.chat Specific Notes

From `Config.java`:

- Default domains: `cheogram.com`, `jmp.chat`
- SIP gateway: `sip.cheogram.com`
- IRC gateway: `irc.cheogram.com`
- Onboarding: `onboarding.cheogram.com`

The JMP.chat server appears to require SCRAM-SHA-1 at minimum based on the error we saw:
```
": no matching authentication ([PLAIN]) supported by server: [SCRAM-SHA-1-PLUS SCRAM-SHA-1]"
```

This confirms the server prefers SCRAM-SHA-1-PLUS and SCRAM-SHA-1 over PLAIN.

### Key XMPP Features Supported by Cheogram

**Stream Features:**
- StartTLS
- SASL Authentication (multiple mechanisms)
- Resource Binding (BIND)
- Session Establishment
- Stream Management (SM)
- Client State Indication (CSI)

**Post-login Features:**
- Roster (contact list) with versioning
- Presence (online, away, dnd, xa)
- Service Discovery (DISCO)
- Message Archive Management (MAM)
- Message Carbons
- HTTP Upload
- PEP (Personal Eventing Protocol)
- Blocking

**Message Types:**
- 1:1 Chat messages
- Groupchat (MUC)
- Delivery receipts
- Read receipts (MAM required)
- Typing indicators (Chat States)
- Message correction (XEP-0308)
- File transfer (Jingle)

---

## Our Current Implementation Status

### What's Working:
- [x] Basic XMPP connection
- [x] PLAIN authentication
- [x] SCRAM-SHA-1 authentication (recently added)
- [x] Basic message passing
- [x] Login flow in bridge

### What's Missing:
- [ ] DNS SRV resolution (using direct connection)
- [ ] SCRAM-SHA-256/512 support
- [ ] Full post-login flow (roster, presence, disco)
- [ ] Message features (receipts, typing, correction)
- [ ] MAM support
- [ ] Carbons support
- [ ] Stream Management
- [ ] Presence management

---

## Next Steps for Implementation

1. **Test SCRAM-SHA-1** - Verify the recently added SCRAM implementation works with JMP.chat
2. **Add DNS SRV** - Implement proper DNS SRV record resolution
3. **Implement post-login** - Add roster, presence, disco after authentication
4. **Add message features** - Receipts, typing, corrections
5. **Test full flow** - End-to-end testing with real JMP.chat account

---

## File References

Key source files analyzed:
- `src/main/java/eu/siacs/conversations/xmpp/XmppConnection.java` - Main XMPP logic
- `src/main/java/eu/siacs/conversations/entities/Account.java` - Account model
- `src/main/java/eu/siacs/conversations/crypto/sasl/ScramMechanism.java` - SCRAM implementation
- `src/main/java/eu/siacs/conversations/crypto/sasl/SaslMechanism.java` - SASL base class
- `src/main/java/eu/siacs/conversations/xmpp/manager/PresenceManager.java` - Presence handling
- `src/main/java/eu/siacs/conversations/xmpp/manager/RosterManager.java` - Roster handling
- `src/main/java/eu/siacs/conversations/xmpp/manager/DiscoManager.java` - Service discovery
- `src/cheogram/java/eu/siacs/conversations/entities/AccountConfiguration.java` - Cheogram config
- `src/cheogram/java/eu/siacs/conversations/utils/ProvisioningUtils.java` - Provisioning flow
