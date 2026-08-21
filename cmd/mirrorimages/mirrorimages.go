package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/brennoo/gvm-mirror/internal/imageref"
	"github.com/brennoo/gvm-mirror/internal/mirror"
)

// run parses flags, mirrors every image the lock file's previous run
// recorded, and (outside -dry-run) writes the updated lock file and, when
// -compose-file is set, rewrites its pins to match — even if some images
// failed, so one broken source never costs the rest of the run its progress.
func run(ctx context.Context, args []string, runner mirror.Runner, now func() time.Time, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mirrorimages", flag.ContinueOnError)
	composeFile := fs.String("compose-file", "",
		"optional Compose file whose image: pins are rewritten to the mirrored digests; empty (the default) skips the rewrite")
	lockFile := fs.String("lock-file", "image-mirror.lock.json",
		"the mirror's provenance lock file: read as this run's seed of what to mirror, then overwritten with the result")
	destPrefix := fs.String("dest-prefix", "ghcr.io/brennoo/gvm-mirror",
		"destination repository prefix; each source image is mirrored to <dest-prefix>/<name>")
	dryRun := fs.Bool("dry-run", false,
		"resolve and plan, but skip copying and writing the lock file")
	allowMove := fs.String("allow-move", "",
		"comma-separated destination tag names allowed to move to a new digest (default: none, all tags are write-once)")
	allowMoveRevision := fs.String("allow-move-revision", "",
		"comma-separated image names whose revision tag may move — for upstreams that rebuild an unchanged commit in place")
	feedVersionTag := fs.String("feed-version-tag", "",
		"comma-separated image names to additionally tag with their net.greenbone.feed.version label (which must then be present)")
	rewriteComposeOnly := fs.Bool("rewrite-compose-only", false,
		"rewrite -compose-file's pins from -lock-file and exit; no registry access, and -lock-file is not modified")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; mirrorimages takes flags only", fs.Arg(0))
	}
	if *rewriteComposeOnly {
		if *composeFile == "" {
			return fmt.Errorf("-rewrite-compose-only requires -compose-file")
		}
		return rewriteComposeOnlyFromLock(*lockFile, *composeFile)
	}
	policies := tagPolicies{
		revisionMovable: parseNameSet(*allowMoveRevision),
		feedVersionTag:  parseNameSet(*feedVersionTag),
	}
	return runWithFlags(ctx, *composeFile, *lockFile, *destPrefix, *dryRun, parseNameSet(*allowMove), policies, runner, now, stdout, stderr)
}

// tagPolicies are per-image tagging opt-ins, keyed by mirror.ImageName.
type tagPolicies struct {
	revisionMovable map[string]bool
	feedVersionTag  map[string]bool
}

func parseNameSet(list string) map[string]bool {
	set := map[string]bool{}
	for name := range strings.SplitSeq(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
}

// validatePolicyNames fails closed on a policy naming an image the lock file
// does not contain — a typo would otherwise silently disable the policy.
func validatePolicyNames(images []mirror.SourceImage, policies tagPolicies) error {
	known := make(map[string]bool, len(images))
	for _, img := range images {
		known[mirror.ImageName(img.Repository)] = true
	}
	check := func(flagName string, names map[string]bool) error {
		sorted := make([]string, 0, len(names))
		for name := range names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			if !known[name] {
				return fmt.Errorf("%s: image %q is not in the lock file", flagName, name)
			}
		}
		return nil
	}
	if err := check("-allow-move-revision", policies.revisionMovable); err != nil {
		return err
	}
	return check("-feed-version-tag", policies.feedVersionTag)
}

// rewriteComposeOnlyFromLock rewrites composeFile's pins from lockFile's
// entries without touching the registry or the lock file itself — for a
// consumer repo regenerating its pins from a vendored copy of the lock.
func rewriteComposeOnlyFromLock(lockFile, composeFile string) error {
	lock, err := mirror.ReadLock(lockFile)
	if err != nil {
		return fmt.Errorf("reading lock file %s: %w", lockFile, err)
	}
	rewritable := rewritableEntries(lock.Entries)
	return rewriteComposeFile(composeFile, rewritable)
}

func runWithFlags(ctx context.Context, composeFile, lockFile, destPrefix string, dryRun bool, movable map[string]bool, policies tagPolicies, runner mirror.Runner, now func() time.Time, stdout, stderr io.Writer) error {
	if !dryRun {
		if err := checkSkopeoAvailable(); err != nil {
			return err
		}
	}

	seedLock, err := mirror.ReadLock(lockFile)
	if err != nil {
		return fmt.Errorf("reading seed lock file %s (mirrorimages needs a prior run's lock file to know what to mirror): %w", lockFile, err)
	}
	images := mirror.SourceImagesFromLock(seedLock)
	if err := validatePolicyNames(images, policies); err != nil {
		return err
	}

	destinations, err := mirror.PlanDestinations(destPrefix, images)
	if err != nil {
		return err
	}

	// A source that fails this run keeps its seed entry rather than vanishing
	// from the lock file, so the next run still has something to retry it from.
	byRepo := make(map[string]mirror.LockEntry, len(seedLock.Entries))
	for _, e := range seedLock.Entries {
		byRepo[e.SourceRepository] = e
	}

	succeeded, failed, total := mirrorAll(ctx, runner, images, destinations, byRepo, movable, policies, dryRun, now, stderr)
	sort.Slice(succeeded, func(i, j int) bool { return succeeded[i].SourceRepository < succeeded[j].SourceRepository })

	if dryRun {
		for _, e := range succeeded {
			_, _ = fmt.Fprintf(stdout, "%s -> %s tags=%v platforms=%v\n", e.SourceRepository, e.DestinationRepository, e.Tags, e.Platforms)
		}
	} else if err := writeResults(lockFile, composeFile, byRepo, succeeded, failed, total, stderr); err != nil {
		return err
	}

	if len(failed) > 0 {
		return fmt.Errorf("failed to mirror %d image(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// mirrorAll mirrors every image, updating byRepo in place with each success.
func mirrorAll(ctx context.Context, runner mirror.Runner, images []mirror.SourceImage, destinations map[string]string, byRepo map[string]mirror.LockEntry, movable map[string]bool, policies tagPolicies, dryRun bool, now func() time.Time, stderr io.Writer) (succeeded []mirror.LockEntry, failed []string, total tagCounts) {
	for _, img := range images {
		entry, counts, err := mirrorOne(ctx, runner, img, destinations[img.Repository], movable, policies, dryRun, now, stderr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "mirrorimages: %s: %v\n", img.Repository, err)
			failed = append(failed, img.Repository)
			continue
		}
		// Keep the seed's captured_at when nothing else changed, so a no-op
		// run leaves the lock file byte-identical and opens no PR.
		if seed, ok := byRepo[img.Repository]; ok && sameExceptCapturedAt(entry, seed) {
			entry.CapturedAt = seed.CapturedAt
		}
		byRepo[img.Repository] = entry
		succeeded = append(succeeded, entry)
		if !dryRun && counts != (tagCounts{}) {
			_, _ = fmt.Fprintf(stderr, "mirrorimages: %s: %s\n", img.Repository, counts)
			total.add(counts)
		}
	}
	return succeeded, failed, total
}

func sameExceptCapturedAt(a, b mirror.LockEntry) bool {
	a.CapturedAt = time.Time{}
	b.CapturedAt = time.Time{}
	return reflect.DeepEqual(a, b)
}

func rewritableEntries(entries []mirror.LockEntry) []mirror.LockEntry {
	// A source with no DestinationDigest has never mirrored successfully, so
	// it's excluded here rather than handed to RewriteCompose, which would
	// otherwise fail closed on a digest this run was never going to have.
	rewritable := make([]mirror.LockEntry, 0, len(entries))
	for _, e := range entries {
		if e.DestinationDigest != "" {
			rewritable = append(rewritable, e)
		}
	}
	return rewritable
}

func rewriteComposeFile(composeFile string, entries []mirror.LockEntry) error {
	composeData, err := os.ReadFile(composeFile) //nolint:gosec // composeFile is an operator-supplied CLI flag, not untrusted input
	if err != nil {
		return fmt.Errorf("reading compose file to rewrite its pins: %w", err)
	}
	rewritten, err := mirror.RewriteCompose(composeData, entries)
	if err != nil {
		return fmt.Errorf("rewriting compose file pins: %w", err)
	}
	if err := os.WriteFile(composeFile, rewritten, 0o644); err != nil { //nolint:gosec // matching the compose file's own pre-existing permissions
		return fmt.Errorf("writing rewritten compose file: %w", err)
	}
	return nil
}

// writeResults writes byRepo (the seed lock merged with this run's
// successes) to lockFile, then — when composeFile is set — rewrites its pins
// for every entry that has ever mirrored successfully.
func writeResults(lockFile, composeFile string, byRepo map[string]mirror.LockEntry, succeeded []mirror.LockEntry, failed []string, total tagCounts, stderr io.Writer) error {
	entries := make([]mirror.LockEntry, 0, len(byRepo))
	for _, e := range byRepo {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].SourceRepository < entries[j].SourceRepository })

	if err := mirror.WriteLock(lockFile, mirror.Lock{Entries: entries}); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "wrote %s (%d image(s) mirrored, %d failed, %s)\n", lockFile, len(succeeded), len(failed), total)

	if composeFile == "" {
		return nil
	}
	if err := rewriteComposeFile(composeFile, rewritableEntries(entries)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "rewrote %s\n", composeFile)
	return nil
}

// tagCounts summarizes what WriteOnceTag actually did across one image's
// tags, so a real push is distinguishable in the log from a run that only
// found everything already correct.
type tagCounts struct {
	created, moved, unchanged int
}

func (c *tagCounts) add(o tagCounts) {
	c.created += o.created
	c.moved += o.moved
	c.unchanged += o.unchanged
}

func (c *tagCounts) record(r mirror.TagResult) {
	switch r {
	case mirror.TagCreated:
		c.created++
	case mirror.TagMoved:
		c.moved++
	case mirror.TagUnchanged:
		c.unchanged++
	}
}

func (c tagCounts) String() string {
	return fmt.Sprintf("created %d tag(s), moved %d, unchanged %d", c.created, c.moved, c.unchanged)
}

type resolvedTags struct {
	tags                           []string
	version, revision, feedVersion string
	movable                        map[string]bool // whole-run movable set plus this image's policy opt-ins
}

// resolveTags derives img's destination tags from its platform labels and
// applies its name-keyed policies.
func resolveTags(img mirror.SourceImage, fallback string, labelsByPlatform map[string]map[string]string, movable map[string]bool, policies tagPolicies, stderr io.Writer) (resolvedTags, error) {
	tags := []string{fallback}
	version, revision, verErr := mirror.ResolveVersionRevision(labelsByPlatform)
	switch {
	case verErr != nil:
		_, _ = fmt.Fprintf(stderr, "mirrorimages: %s: no version/revision tags (%v); mirroring under %s only\n", img.Repository, verErr, fallback)
	default:
		if err := imageref.ValidateTagName(version); err != nil {
			return resolvedTags{}, fmt.Errorf("version label %q: %w", version, err)
		}
		if err := imageref.ValidateTagName(revision); err != nil {
			return resolvedTags{}, fmt.Errorf("revision label %q: %w", revision, err)
		}
		tags = append(tags, version, revision)
	}

	name := mirror.ImageName(img.Repository)
	var feedVersion string
	if policies.feedVersionTag[name] {
		v, err := mirror.ResolveFeedVersion(labelsByPlatform)
		if err != nil {
			return resolvedTags{}, err
		}
		if err := imageref.ValidateTagName(v); err != nil {
			return resolvedTags{}, fmt.Errorf("feed version label %q: %w", v, err)
		}
		feedVersion = v
		tags = append(tags, feedVersion)
	}
	if policies.revisionMovable[name] && revision != "" {
		withRevision := make(map[string]bool, len(movable)+1)
		for tag := range movable {
			withRevision[tag] = true
		}
		withRevision[revision] = true
		movable = withRevision
	}
	return resolvedTags{tags: tags, version: version, revision: revision, feedVersion: feedVersion, movable: movable}, nil
}

// mirrorOne resolves, inspects, and (outside dryRun) copies and tags one
// source image, returning the LockEntry describing what was done or found
// plus a summary of what its tag writes actually did. A source image whose
// org.opencontainers.image.version/.revision labels don't resolve is not a
// failure of the whole entry — it mirrors under its digest-derived fallback
// tag only, noted on stderr.
func mirrorOne(ctx context.Context, runner mirror.Runner, img mirror.SourceImage, destRepo string, movable map[string]bool, policies tagPolicies, dryRun bool, now func() time.Time, stderr io.Writer) (mirror.LockEntry, tagCounts, error) {
	srcDigestRef, err := mirror.ResolveDigest(ctx, runner, img.Channel)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}

	platforms, err := mirror.InspectPlatforms(ctx, runner, srcDigestRef)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}

	labelsByPlatform := make(map[string]map[string]string, len(platforms))
	platformNames := make([]string, 0, len(platforms))
	for _, p := range platforms {
		labels, err := mirror.PlatformLabels(ctx, runner, img.Repository, p.Digest)
		if err != nil {
			return mirror.LockEntry{}, tagCounts{}, err
		}
		name := p.OS + "/" + p.Arch
		labelsByPlatform[name] = labels
		platformNames = append(platformNames, name)
	}
	sort.Strings(platformNames)

	fallback := mirror.FallbackTag(srcDigestRef)
	resolved, err := resolveTags(img, fallback, labelsByPlatform, movable, policies, stderr)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}
	tags, movable := resolved.tags, resolved.movable

	entry := mirror.LockEntry{
		SourceRepository:      img.Repository,
		SourceChannel:         img.Channel,
		SourceDigest:          srcDigestRef,
		DestinationRepository: destRepo,
		Version:               resolved.version,
		Revision:              resolved.revision,
		FeedVersion:           resolved.feedVersion,
		Tags:                  tags,
		Platforms:             platformNames,
		CapturedAt:            now(),
	}
	if dryRun {
		return entry, tagCounts{}, nil
	}

	var counts tagCounts
	fallbackResult, err := mirror.WriteOnceTag(ctx, runner, destRepo, fallback, srcDigestRef, nil)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}
	counts.record(fallbackResult)

	dstDigestRef, err := mirror.ResolveDigest(ctx, runner, destRepo+":"+fallback)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}
	entry.DestinationDigest = dstDigestRef

	dstPlatforms, err := mirror.InspectPlatforms(ctx, runner, dstDigestRef)
	if err != nil {
		return mirror.LockEntry{}, tagCounts{}, fmt.Errorf("verifying destination platforms for %s: %w", destRepo, err)
	}
	if err := mirror.VerifyPlatformsMatch(platforms, dstPlatforms); err != nil {
		return mirror.LockEntry{}, tagCounts{}, err
	}

	for _, tag := range tags[1:] {
		result, err := mirror.WriteOnceTag(ctx, runner, destRepo, tag, dstDigestRef, movable)
		if err != nil {
			return mirror.LockEntry{}, tagCounts{}, err
		}
		counts.record(result)
	}
	return entry, counts, nil
}
