package process

import (
	"encoding/json"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// stepOrder is the strict dependency order from cleaning through seating.
var stepOrder = []string{"clean", "dry", "cut", "joint", "glue", "paste", "roll", "cure", "seat"}

// stepIndex returns the position of a step kind, or -1 when not a process step.
func stepIndex(kind string) int {
	for i, k := range stepOrder {
		if k == kind {
			return i
		}
	}
	return -1
}

// requiredLease returns the resource lease required for a process step, plus
// whether a lease is required at all. The resource id is the ring id.
func requiredLease(kind string) (domain.ResourceKind, bool) {
	switch kind {
	case "clean":
		return domain.ResourceCleanBay, true
	case "glue":
		return domain.ResourceGlueTable, true
	case "roll":
		return domain.ResourceRoller, true
	case "seat":
		return domain.ResourceErector, true
	}
	return "", false
}

// boltPayload is the persisted form of a bolt-stage evidence event.
type boltPayload struct {
	Stage      int   `json:"stage"`
	PreloadDev int64 `json:"preload_dev"`
}

// geometryPayload is the persisted form of a geometry evidence event.
type geometryPayload struct {
	Kind  string `json:"kind"`
	Value int64  `json:"value"`
}

// processState is the derived dependency state rebuilt from stored evidence.
type processState struct {
	prefix    int
	lastTime  int64
	glueTime  int64 // -1 when not yet glued
	boltStage int
	geometry  map[string]bool
}

// deriveState rebuilds the dependency state by replaying evidence records.
func deriveState(records []store.EvidenceRecord) processState {
	s := processState{glueTime: -1, geometry: map[string]bool{}}
	done := map[string]bool{}
	for _, rec := range records {
		if rec.LogicalTime > s.lastTime {
			s.lastTime = rec.LogicalTime
		}
		if idx := stepIndex(rec.Kind); idx >= 0 {
			done[rec.Kind] = true
			if rec.Kind == "glue" {
				s.glueTime = rec.LogicalTime
			}
			continue
		}
		switch rec.Kind {
		case "bolt":
			var p boltPayload
			if err := json.Unmarshal(rec.Payload.(json.RawMessage), &p); err == nil && p.Stage > s.boltStage {
				s.boltStage = p.Stage
			}
		case "geometry":
			var p geometryPayload
			if err := json.Unmarshal(rec.Payload.(json.RawMessage), &p); err == nil {
				s.geometry[p.Kind] = true
			}
		}
	}
	// Prefix is the longest consecutive run from the first step.
	for i, k := range stepOrder {
		if !done[k] {
			break
		}
		s.prefix = i + 1
	}
	return s
}

// atSeat reports whether the full process prefix (through seating) is done.
func (s processState) atSeat() bool {
	return s.prefix == len(stepOrder)
}
