// Package imageref parses and validates Docker/OCI-style image references —
// the "repository[:tag][@digest]" shape used throughout a consumer's Compose
// file and this mirror's own lock file — so the mirror and its tests share
// one implementation instead of independently drifting ones.
package imageref

import (
	"fmt"
	"strings"
)

// Repository returns ref's repository, stripping a tag or digest — the
// standard Docker reference-parsing rule: a "@" always introduces a digest,
// and a ":" only introduces a tag when it occurs after the last "/"
// (otherwise it's part of a "host:port" registry address that has no tag at
// all).
func Repository(ref string) string {
	if repo, _, ok := strings.Cut(ref, "@"); ok {
		return repo
	}
	if slash, colon := strings.LastIndexByte(ref, '/'), strings.LastIndexByte(ref, ':'); colon > slash {
		return ref[:colon]
	}
	return ref
}

// ValidateTagName checks that tag is a well-formed Docker/OCI tag: 1-128
// characters, starting with an alphanumeric, followed by alphanumerics,
// '_', '.', or '-'. This is the distribution spec's own grammar, not this
// project's invention.
func ValidateTagName(tag string) error {
	if tag == "" || len(tag) > 128 {
		return fmt.Errorf("%q is not a valid tag name: must be 1-128 characters", tag)
	}
	for i, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '_' || r == '.' || r == '-'):
		default:
			return fmt.Errorf("%q is not a valid tag name: invalid character %q at position %d", tag, r, i)
		}
	}
	return nil
}

// composeImagePrefix is the exact indentation Docker Compose service blocks
// use for their "image:" key in the Compose files this mirror rewrites.
const composeImagePrefix = "    image: "

// ImageLineRepository extracts a Compose "    image: ..." line's repository,
// stripping any tag or digest via Repository. ok is false for any line that
// is not an image line at all.
func ImageLineRepository(line string) (repo string, ok bool) {
	rest, isImage := strings.CutPrefix(line, composeImagePrefix)
	if !isImage {
		return "", false
	}
	return Repository(rest), true
}
