/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

// releaseSigningPublicKeys lists the Ed25519 public keys (standard base64 of
// the 32-byte key) that sign checksums.txt of every GitHub release. The
// matching private seed lives only in the CHATCLI_RELEASE_SIGNING_KEY secret
// of the release workflow, which runs `go run ./tools/releasesign sign
// checksums.txt` and publishes checksums.txt.sig next to it.
//
// More than one entry is allowed so a key can be rotated: ship a build that
// trusts old and new keys, switch the workflow to the new seed, then drop
// the old key in a later release.
//
// PLACEHOLDER: no release key is embedded yet. With this list empty the
// self-update channel refuses to install anything (fail closed) and tells
// the user to update through Homebrew, go install or a manual download.
var releaseSigningPublicKeys = []string{}
