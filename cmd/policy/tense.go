package policy

// tense holds the wording one run uses. A preview and a completed run report
// the same plan, so the words are the only thing telling a reader which one
// they are looking at. They must not differ by a single missing "would".
type tense struct {
	install   string
	update    string
	pipeline  string
	unchanged string
	preserved string
	nothing   string
}

// wouldTense describes a plan that has not been applied.
func wouldTense() *tense {
	return &tense{
		install:   "would install",
		update:    "would update",
		pipeline:  "would turn the registry cleanup pipeline",
		unchanged: "would leave",
		preserved: "would keep",
		nothing:   "cleanup policies already match, nothing to do",
	}
}

// didTense describes a plan that has been applied.
func didTense() *tense {
	return &tense{
		install:   "installed",
		update:    "updated",
		pipeline:  "turned the registry cleanup pipeline",
		unchanged: "left",
		preserved: "kept",
		nothing:   "cleanup policies already matched, nothing written",
	}
}
