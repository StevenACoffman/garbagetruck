package garbagetruck

// lookup answers which version of one image a reference selects.
type lookup struct {
	// byTag maps a tag the registry holds to the digest it points at.
	byTag map[string]string
	// digests holds every digest the registry stores for the image.
	digests map[string]struct{}
}

// versionsByImage builds one lookup per image, keyed by the name a manifest
// would use for it.
func versionsByImage(images []Image) map[string]lookup {
	byImage := make(map[string]lookup, len(images))
	for _, image := range images {
		entry := lookup{
			byTag:   make(map[string]string),
			digests: make(map[string]struct{}, len(image.Versions)),
		}
		for _, version := range image.Versions {
			entry.digests[version.Digest] = struct{}{}
			for _, tag := range version.Tags {
				entry.byTag[tag] = version.Digest
			}
		}
		byImage[image.Repo.Name()] = entry
	}
	return byImage
}

// resolve returns the digest of the version ref selects. A reference that
// names both a tag and a digest is resolved by its digest, which is immutable
// and therefore the stronger claim of the two.
func (l lookup) resolve(ref Ref) (string, bool) {
	if ref.Digest != "" {
		_, stored := l.digests[ref.Digest]
		return ref.Digest, stored
	}
	digest, tagged := l.byTag[ref.Tag]
	return digest, tagged
}
