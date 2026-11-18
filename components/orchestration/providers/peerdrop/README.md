# PeerDrop broker onboarding

This optional command transfers a public connection profile and an expiring
guest credential between two developers. The host and guest run the helper
locally. PeerDrop provides room signaling and ICE configuration. The helper
sends the handoff on an ordered WebRTC data channel after approval. The
broker continues serving HTTPS and has no provider configuration.

Build this command from its directory with `GOWORK=off go build -o llewm-peerdrop .`.
Both developers need the helper only for the initial exchange. A distribution
can omit this module and binary entirely. The
[ordinary profile handoff](../../docs/shared-agent-broker.md#named-connection-profiles-and-expiring-credentials)
works independently.

1. The host enrolls the guest with `shared-broker-member --expires-at`, using a
   future RFC3339 timestamp, and exports a verified profile with
   `shared-broker-info --save-profile`. Use the guest's own credential.
2. The host runs `llewm-peerdrop offer --profile red.json --token-file guest.token`.
   Give the other developer the printed room code. Keep the host process open.
3. The guest runs `llewm-peerdrop receive --room 123-456-789 --profile /private/pair/red.json`
   with the actual room code and a new destination in a private directory.
   Both sides can select the same alternative HTTPS signaling origin with
   `--provider`. The default is https://peerdrop.fluxwall.com.
4. Compare the **entire verification code** on each other's screens or by
   a voice call with the intended developer. Each types the matching code
   locally to approve. Abort on mismatch or an unexpected participant.
5. The guest verifies the credential against the pinned HTTPS project and
   writes the profile and its sibling `.token` file. Use
   `llewm-orchestrate shared-broker-info --profile /private/pair/red.json`
   and the ordinary messaging commands. If a final receipt is lost, check
   the guest's files and access before rotating the credential.

One attempt lasts five minutes. Either developer can cancel with Ctrl-C.
Each new attempt uses a fresh room, WebRTC certificates and random nonces.
The eight-digit verification code binds both nonces and the ordered local
and remote DTLS certificate fingerprints. The host commits to its nonce
before receiving the guest nonce; the guest checks the reveal. The host
requires local approval and the verified channel's guest approval before
sending. The room code selects a session. The independent comparison
authenticates the intended peer. The signaling service's lost-peer cleanup
and empty room expiry follow its own policy.

A transfer fits in one 48 KiB data-channel frame. For unusually large CA
bundles use the ordinary HTTPS profile handoff. The guest profile and
credential must both be new files. After successful pairing the host can
remove the transfer copy and revoke the guest at the broker whenever needed.
