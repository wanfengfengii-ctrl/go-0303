package verdict

import (
	"sort"
	"strconv"
	"strings"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// propagate computes the deterministic, sorted, de-duplicated retest set for an
// anomaly source. It seeds from the source, pulls in the directly adjacent ring
// joints (one step), then takes the transitive closure over shared gasket bar
// batches (segments whose bars come from the same batch are replaced together).
// Adhesive-generation sources affect the whole ring.
func propagate(task domain.RingTask, bindings []store.GasketBinding, source string) []int {
	seeds := seedSegments(task, source)
	affected := map[int]bool{}
	for _, s := range seeds {
		affected[s] = true
	}

	n := len(task.Segments)
	seqToIdx := map[int]int{}
	for i, seg := range task.Segments {
		seqToIdx[seg.Seq] = i
	}
	neighbours := func(seq int) []int {
		idx, ok := seqToIdx[seq]
		if !ok || n < 2 {
			return nil
		}
		return []int{task.Segments[(idx-1+n)%n].Seq, task.Segments[(idx+1)%n].Seq}
	}

	// One-step ring adjacency: adjacent joints of every seed.
	for _, s := range seeds {
		for _, nb := range neighbours(s) {
			affected[nb] = true
		}
	}

	// Shared gasket bar batches: batch -> segment seqs bound to bars of it.
	batchSeqs := map[string][]int{}
	for _, b := range bindings {
		batchSeqs[b.Batch] = append(batchSeqs[b.Batch], b.SlotSeq)
	}

	// Transitive closure over shared batches only.
	for changed := true; changed; {
		changed = false
		for _, seqs := range batchSeqs {
			if len(seqs) < 2 {
				continue
			}
			shared := false
			for _, s := range seqs {
				if affected[s] {
					shared = true
					break
				}
			}
			if shared {
				for _, s := range seqs {
					if !affected[s] {
						affected[s] = true
						changed = true
					}
				}
			}
		}
	}

	out := make([]int, 0, len(affected))
	for s := range affected {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// seedSegments parses an anomaly source into an initial set of segment seqs.
// Sources: "joint_crack:<seq>", "offset:<seq>", "preload:<seq>", "empty:<seq>",
// "leak:<bay>" (bay index maps to a segment), "adhesive:<gen>" or "whole"
// (every segment in the generation).
func seedSegments(task domain.RingTask, source string) []int {
	all := func() []int {
		out := make([]int, len(task.Segments))
		for i, s := range task.Segments {
			out[i] = s.Seq
		}
		return out
	}
	switch {
	case source == "whole":
		return all()
	case strings.HasPrefix(source, "adhesive:"):
		return all()
	default:
		idx := strings.LastIndex(source, ":")
		if idx < 0 {
			return nil
		}
		v, err := strconv.Atoi(source[idx+1:])
		if err != nil {
			return nil
		}
		return []int{v}
	}
}
