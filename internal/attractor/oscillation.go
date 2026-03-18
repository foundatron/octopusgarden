package attractor

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
)

// hashFiles computes a deterministic SHA-256 hash of the given file map.
// Files are sorted by path before hashing to ensure order independence.
// Each entry is encoded as: path + \x00 + content; entries are separated by \x01.
// Using distinct separators prevents path/content boundary confusion.
func hashFiles(files map[string]string) string {
	paths := slices.Sorted(maps.Keys(files))
	h := sha256.New()
	for _, p := range paths {
		// sha256.Hash.Write never returns an error.
		h.Write([]byte(p))
		h.Write([]byte{0}) // \x00 separates path from content
		h.Write([]byte(files[p]))
		h.Write([]byte{1}) // \x01 separates entries
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// detectOscillation returns true when the last 4 hashes form an A→B→A→B pattern.
// This detects cases where the LLM alternates between two solutions without progress.
// The degenerate A==A==A==A case also returns true, signaling a hard stall.
func detectOscillation(hashes []string) bool {
	n := len(hashes)
	if n < 4 {
		return false
	}
	return hashes[n-1] == hashes[n-3] && hashes[n-2] == hashes[n-4]
}

// oscillationSteeringText is injected into the next Generate call when oscillation is detected.
const oscillationSteeringText = `OSCILLATION DETECTED: Your last several attempts have alternated between the same two implementations without making progress. This is a sign that you are stuck in a local loop.

Break out by trying a fundamentally different approach:
- Reconsider your core data structures or algorithm
- Change the architecture rather than tweaking the same implementation
- If you fixed something in a previous attempt that was then undone, keep that fix and build on it

Do NOT revert to a previous implementation. Make a genuinely new attempt.`
