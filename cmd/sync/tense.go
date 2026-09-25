package sync

// tense holds the wording one run uses. A dry run and a real run differ in
// nothing but this: the same plan, described as intention or as fact.
type tense struct {
	create  string
	move    string
	remove  string
	nothing string
}

// wouldTense describes a plan that has not been applied.
func wouldTense() tense {
	return tense{
		create:  "would tag",
		move:    "would move",
		remove:  "would untag",
		nothing: "registry already agrees with the manifests, nothing to do",
	}
}

// didTense describes a plan that has been applied.
func didTense() tense {
	return tense{
		create:  "tagged",
		move:    "moved",
		remove:  "untagged",
		nothing: "registry already agreed with the manifests, nothing done",
	}
}
