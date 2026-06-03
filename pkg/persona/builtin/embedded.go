/*
 * ChatCLI - Persona System
 * pkg/persona/builtin/embedded.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package builtin ships the essential skills *inside the binary* so they are
 * present for every install method — `go install`, Homebrew, a downloaded
 * release — not just when running from a checkout of the repo.
 *
 * The repo's project-local .agent/skills/ directory only loads when ChatCLI is
 * invoked inside that checkout; an installed user never sees it. These embedded
 * skills are seeded into the user's global skills directory (~/.chatcli/skills)
 * on startup (see Seed), where the normal Loader then discovers them like any
 * other skill — and where the user can freely edit them.
 */
package builtin

import "embed"

// FS holds the embedded essential skills. Paths inside are forward-slash and
// rooted at "skills/<name>/SKILL.md" regardless of host OS (embed always uses
// forward slashes — never filepath.Join to read from it).
//
//go:embed all:skills
var FS embed.FS

// skillsRoot is the directory prefix inside FS.
const skillsRoot = "skills"
