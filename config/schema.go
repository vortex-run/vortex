// Package schema exposes VORTEX's embedded master configuration schema.
//
// The authoritative schema lives next to this file as schema.cue (per the
// repo layout in README.md). go:embed can only reference files within its own
// directory tree, so this tiny package embeds the schema here and the config
// engine (internal/config) imports Source rather than reading from disk. This
// keeps the schema inside the single binary (Non-Negotiable Rule #1).
package schema

import _ "embed"

// Source is the raw bytes of the master schema (config/schema.cue).
//
//go:embed schema.cue
var Source []byte

// Filename is the schema's name, used in error messages.
const Filename = "schema.cue"
