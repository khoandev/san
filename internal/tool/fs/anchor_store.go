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
// later fills the gap. byAnchor is the same data keyed the way an edit looks it
// up — an anchor resolves in constant time instead of scanning every line the
// model has seen. served records the anchors whose rows the model was shown,
// with the checksum of the row it saw: an edit may only touch a line the model
// has actually looked at.
type anchorSnapshot struct {
	stamp    fileStamp
	lines    map[int]anchorEntry
	byAnchor map[string]int
	served   map[string]uint64
}

func (s *anchorSnapshot) current(info os.FileInfo) bool {
	return s.stamp.modTime.Equal(info.ModTime()) && s.stamp.size == info.Size()
}

// set records a line's anchor in both directions. The two maps are always
// written together; a stale byAnchor entry would resolve an edit to a line
// whose anchor it is not.
func (s *anchorSnapshot) set(line int, entry anchorEntry) {
	if s.lines == nil {
		s.lines = map[int]anchorEntry{}
	}
	if s.byAnchor == nil {
		s.byAnchor = make(map[string]int, len(s.lines)+1)
	}
	if old, ok := s.lines[line]; ok && old.anchor != entry.anchor {
		delete(s.byAnchor, old.anchor)
	}
	s.lines[line] = entry
	s.byAnchor[entry.anchor] = line
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
		snap = &anchorSnapshot{}
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
			entry = anchorEntry{anchor: mintAnchor(), checksum: checksum}
			snap.set(row.lineNo, entry)
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

// anchorRange is the outcome of resolving an anchored edit against one file
// snapshot. stale lists the lines in the range the model was never shown (or
// that changed after it was); missing is set when an anchor does not resolve at
// all.
type anchorRange struct {
	start, end int
	reversed   bool
	stale      []int
	missing    string
}

// resolveAnchoredRange resolves both anchors and verifies the whole range
// inside one lock hold: an edit otherwise pays three acquisitions and two full
// scans of the same snapshot. boundariesOnly checks just the two end lines,
// which is enough for a deletion — the interior is removed, not rewritten.
func resolveAnchoredRange(filePath string, info os.FileInfo, lines []string, from, to string, boundariesOnly bool) (anchorRange, bool) {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok || !snap.current(info) {
		return anchorRange{}, false
	}

	start, ok := snap.byAnchor[from]
	if !ok {
		return anchorRange{missing: from}, true
	}
	end, ok := snap.byAnchor[to]
	if !ok {
		return anchorRange{missing: to}, true
	}
	resolved := anchorRange{start: start, end: end}
	if start > end {
		resolved.start, resolved.end = end, start
		resolved.reversed = true
	}
	if resolved.end > len(lines) {
		return anchorRange{missing: to}, true
	}

	for line := resolved.start; line <= resolved.end; line++ {
		if boundariesOnly && line != resolved.start && line != resolved.end {
			continue
		}
		entry, ok := snap.lines[line]
		if !ok {
			resolved.stale = append(resolved.stale, line)
			continue
		}
		served, ok := snap.served[entry.anchor]
		if !ok || served != lineChecksum(lines[line-1]) {
			resolved.stale = append(resolved.stale, line)
		}
	}
	return resolved, true
}

// serveRange mints anchors for any line in [start,end] that has none and marks
// the whole range served, returning the anchors in line order. The rejection
// path prints exactly these rows, so the model's retry carries anchors it has
// been shown and needs no extra Read.
func serveRange(filePath string, info os.FileInfo, lines []string, start, end int) []string {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap := snapshotFor(filePath, info)
	if snap.served == nil {
		snap.served = map[string]uint64{}
	}
	out := make([]string, 0, end-start+1)
	for line := start; line <= end; line++ {
		checksum := lineChecksum(lines[line-1])
		entry, ok := snap.lines[line]
		if !ok || entry.checksum != checksum {
			entry = anchorEntry{anchor: mintAnchor(), checksum: checksum}
			snap.set(line, entry)
		}
		snap.served[entry.anchor] = checksum
		out = append(out, entry.anchor)
	}
	return out
}

// recordAnchorEdit moves the snapshot onto the file as it is after an edit: the
// replaced range loses its anchors, the lines after it shift by delta, and the
// new lines are minted and served. Serving the new rows is what lets a
// follow-up anchored edit land without a second Read — the edit result prints
// the same anchors.
func recordAnchorEdit(filePath string, info os.FileInfo, start, end int, replacement []string, replacementTexts []string) {
	anchorsMu.Lock()
	defer anchorsMu.Unlock()
	snap, ok := anchorSnapshots[fileViewKey(filePath)]
	if !ok {
		snap = &anchorSnapshot{}
		anchorSnapshots[fileViewKey(filePath)] = snap
	}

	removed := end - start + 1
	delta := len(replacement) - removed
	if delta == 0 {
		// Same-size replacement: nothing shifts, so the surrounding anchors are
		// already correct and only the range is rewritten. This is the common
		// case (edit one line, write one line) and it stays O(range) instead of
		// rebuilding both maps for the whole file on every edit.
		for line := start; line <= end; line++ {
			if entry, ok := snap.lines[line]; ok {
				delete(snap.byAnchor, entry.anchor)
				delete(snap.lines, line)
				delete(snap.served, entry.anchor)
			}
		}
	} else {
		moved := make(map[int]anchorEntry, len(snap.lines))
		byAnchor := make(map[string]int, len(snap.lines)+len(replacement))
		for line, entry := range snap.lines {
			switch {
			case line < start:
				moved[line] = entry
				byAnchor[entry.anchor] = line
			case line > end:
				moved[line+delta] = entry
				byAnchor[entry.anchor] = line + delta
			}
		}
		snap.lines = moved
		snap.byAnchor = byAnchor
		// Anchors that left the file stop being served; the rest keep their rows.
		for anchor := range snap.served {
			if _, ok := byAnchor[anchor]; !ok {
				delete(snap.served, anchor)
			}
		}
	}
	if snap.served == nil {
		snap.served = map[string]uint64{}
	}
	for i, anchor := range replacement {
		checksum := lineChecksum(replacementTexts[i])
		snap.set(start+i, anchorEntry{anchor: anchor, checksum: checksum})
		snap.served[anchor] = checksum
	}
	snap.stamp = fileStamp{modTime: info.ModTime(), size: info.Size()}
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
