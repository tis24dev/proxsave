package orchestrator

// PBSRestoreBehavior controls how PBS objects are reconciled during staged apply.
// It is intentionally chosen at restore time (UI), not via backup.env.
type PBSRestoreBehavior int

const (
	PBSRestoreBehaviorUnspecified PBSRestoreBehavior = iota
	PBSRestoreBehaviorMerge
	PBSRestoreBehaviorClean
)

func (b PBSRestoreBehavior) String() string {
	switch b {
	case PBSRestoreBehaviorMerge:
		return "merge"
	case PBSRestoreBehaviorClean:
		return "clean-1to1"
	default:
		return "unspecified"
	}
}

func (b PBSRestoreBehavior) DisplayName() string {
	switch b {
	case PBSRestoreBehaviorMerge:
		return "Merge (existing PBS)"
	case PBSRestoreBehaviorClean:
		return "Clean 1:1 (fresh PBS install)"
	default:
		return "Unspecified"
	}
}
