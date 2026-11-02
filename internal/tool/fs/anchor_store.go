package fs

import (
	"os"
	"sync"
)

// anchorEntry is what the session remembers about one file line: the anchor it
// was given and the fingerprint of the text the anchor was minted for.
type anchorEntry struct {
	anchor   string
	checksum uint64
}

// anchorSnapshot is one file's anchors as of the on-disk state in stamp. lines
// is sparse: only the lines a Read actually printed have anchors, so reading a
// 2,000-line window of a 100,000-line file costs 2,000 entries, and paging
// later fills the gap. served records the anchors whose rows the model was
// shown, with the checksum of the row it saw: an edit may only touch a line the
// model has actually looked at.
type anchorSnapshot struct {
	stamp  fileStamp
	lines  map[int]anchorEntry
	served map[string]uint64
}

func (s *anchorSnapshot) current(info os.FileInfo) bool {
	return s.stamp.modTime.Equal(info.ModTime()) && s.stamp.size == info.Size()
}

var (
	anchorsMu       sync.Mutex
	anchorSnapshots = map[string]*anchorSnapshot{}
)

// resetAnchors forgets every file's anchors. ResetFileViews calls it when the
// conversation the model sees is replaced, so anchors — like "this file was
// read" — mean the current conversation and not the process lifetime. The mint
// cursor keeps counting across the reset: handing the sequence back out would
// let a handle from the previous conversation resolve to a line of this one.
func resetAnchors() {
	anchorsMu.Lock()
	clear(anchorSnapshots)
	anchorsMu.Unlock()
}

// forgetAnchors drops one file's snapshot after a wholesale write. The next
// Read mints fresh anchors, which is the honest answer: none of the anchors the
// model holds describe the new content.
func forgetAnchors(filePath string) {
	anchorsMu.Lock()
	delete(anchorSnapshots, fileViewKey(filePath))
	anchorsMu.Unlock()
}

// snapshotFor returns the file's snapshot, replacing it when the file changed
// on disk since the anchors were minted. A replaced snapshot keeps no anchors
// and no served rows: every anchor the model holds for this file is stale, and
// the first edit that uses one says so instead of resolving to a line that has
// since moved.
func snapshotFor(filePath string, info os.FileInfo) *anchorSnapshot {
	key := fileViewKey(filePath)
	snap, ok := anchorSnapshots[key]
	stamp := fileStamp{modTime: info.ModTime(), size: info.Size()}
	if !ok || !snap.current(info) {
		snap = &anchorSnapshot{stamp: stamp}
		anchorSnapshots[key] = snap
	}
	snap.stamp = stamp
	return snap
}

// anchorRows assigns an anchor to each Read row and returns them in order. A
// line that already has an anchor for the same text keeps it, so paging a file
// twice shows the same handles; a line whose anchor exists but whose text
// changed (a same-size rewrite under an unchanged mtime) gets a fresh one, and
// the old anchor stops resolving.
func anchorRows(filePath string, info os.FileInfo, rows []anchorRow) []string {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap := snapshotFor(filePath, info)
	out := make([]string, len(rows))
	for i, row := range rows {
		checksum := lineChecksum(row.text)
		entry, ok := snap.lines[row.lineNo]
		if !ok || entry.checksum != checksum {
			if snap.lines == nil {
				snap.lines = map[int]anchorEntry{}
			}
			entry = anchorEntry{anchor: mintAnchor(), checksum: checksum}
			snap.lines[row.lineNo] = entry
		}
		out[i] = entry.anchor
		if row.editable {
			if snap.served == nil {
				snap.served = map[string]uint64{}
			}
			snap.served[entry.anchor] = checksum
		}
	}
	return out
}

// anchorLineOf resolves an anchor to the 1-based line it was minted for. ok is
// false when the anchor is unknown to this file or the file has changed since
// the anchors were handed out — in both cases the model's handle no longer
// names a line it can safely edit.
func anchorLineOf(filePath string, info os.FileInfo, anchor string) (int, bool) {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok || !snap.current(info) {
		return 0, false
	}
	for line, entry := range snap.lines {
		if entry.anchor == anchor {
			return line, true
		}
	}
	return 0, false
}

// servedChecksumOf reports the fingerprint of the row the model was shown for
// this anchor. A missing entry means the model never saw that line.
func servedChecksumOf(filePath, anchor string) (uint64, bool) {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok || snap.served == nil {
		return 0, false
	}
	checksum, ok := snap.served[anchor]
	return checksum, ok
}

// recordAnchorEdit moves the snapshot onto the file as it is after an edit: the
// replaced range loses its anchors, the lines after it shift by delta, and the
// new lines are minted and served. Serving the new rows is what lets a
// follow-up anchored edit land without a second Read — the edit result prints
// the same anchors.
func recordAnchorEdit(filePath string, info os.FileInfo, start, end int, replacement []string, replacementTexts []string) []string {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok {
		snap = &anchorSnapshot{}
		anchorSnapshots[fileViewKey(filePath)] = snap
	}

	removed := end - start + 1
	delta := len(replacement) - removed
	moved := make(map[int]anchorEntry, len(snap.lines))
	for line, entry := range snap.lines {
		switch {
		case line < start:
			moved[line] = entry
		case line > end:
			moved[line+delta] = entry
		}
	}
	if snap.lines == nil {
		snap.lines = map[int]anchorEntry{}
	}
	snap.lines = moved
	snap.stamp = fileStamp{modTime: info.ModTime(), size: info.Size()}

	live := make(map[string]struct{}, len(moved))
	for _, entry := range moved {
		live[entry.anchor] = struct{}{}
	}
	for anchor := range snap.served {
		if _, ok := live[anchor]; !ok {
			delete(snap.served, anchor)
		}
	}

	for i, anchor := range replacement {
		checksum := lineChecksum(replacementTexts[i])
		snap.lines[start+i] = anchorEntry{anchor: anchor, checksum: checksum}
		if snap.served == nil {
			snap.served = map[string]uint64{}
		}
		snap.served[anchor] = checksum
	}
	return replacement
}

// verifyServedRange reports the lines in [start,end] that were never shown to
// the model or that changed after they were shown, and whether the snapshot
// still describes the file at all. A deletion is checked at its two boundary
// lines only: the interior costs a full scan to validate and is being removed
// anyway, so trusting it is the cheap, safe choice — the boundaries are what
// an off-by-one range would get wrong.
func verifyServedRange(filePath string, info os.FileInfo, lines []string, start, end int, boundariesOnly bool) (stale []int, current bool) {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok || !snap.current(info) {
		return nil, false
	}
	for line := start; line <= end; line++ {
		if boundariesOnly && line != start && line != end {
			continue
		}
		entry, ok := snap.lines[line]
		if !ok {
			stale = append(stale, line)
			continue
		}
		served, ok := snap.served[entry.anchor]
		if !ok || served != lineChecksum(lines[line-1]) {
			stale = append(stale, line)
		}
	}
	return stale, true
}

// serveRange mints anchors for any line in [start,end] that has none and marks
// the whole range served, returning the anchors in line order. The rejection
// path prints exactly these rows, so the model's retry carries anchors it has
// been shown and needs no extra Read.
func serveRange(filePath string, info os.FileInfo, lines []string, start, end int) []string {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap := snapshotFor(filePath, info)
	if snap.lines == nil {
		snap.lines = map[int]anchorEntry{}
	}
	if snap.served == nil {
		snap.served = map[string]uint64{}
	}
	out := make([]string, 0, end-start+1)
	for line := start; line <= end; line++ {
		checksum := lineChecksum(lines[line-1])
		entry, ok := snap.lines[line]
		if !ok || entry.checksum != checksum {
			entry = anchorEntry{anchor: mintAnchor(), checksum: checksum}
			snap.lines[line] = entry
		}
		snap.served[entry.anchor] = checksum
		out = append(out, entry.anchor)
	}
	return out
}

// changedLineRange locates the range of lines an edit rewrote, comparing the
// file before and after. It returns 1-based inclusive bounds and the number of
// lines in the new range; ok is false when the two contents are equal.
func changedLineRange(oldContent, newContent string) (first, last, replacementCount int, ok bool) {
	if oldContent == newContent {
		return 0, 0, 0, false
	}
	oldLines := splitFileLines(oldContent)
	newLines := splitFileLines(newContent)
	if len(oldLines) == 0 {
		return 1, 0, len(newLines), true
	}

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	first = prefix + 1
	last = len(oldLines) - suffix
	replacementCount = len(newLines) - prefix - suffix
	return first, last, replacementCount, true
}
