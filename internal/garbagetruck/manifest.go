package garbagetruck

import (
	"bufio"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	// imageKey is the only YAML key a Kubernetes container spec names its
	// image with. Matching it exactly is what excludes "imagePullPolicy".
	imageKey = "image:"

	// maxLineBytes bounds a single manifest line. bufio.Scanner's default
	// 64KiB limit would abort a scan on a file with one long line, and a
	// silently short image set is worse than a slow one.
	maxLineBytes = 1 << 20
)

// ScanFS walks fsys and returns the sorted, de-duplicated image references
// declared by every file named *.yaml or *.yml. Dot-directories are skipped,
// so a cloned repository's .git is not searched.
//
// A line that names an image which ParseRef rejects is skipped rather than
// failing the scan: a manifest may hold a templating placeholder where an
// image belongs, and a placeholder pins nothing that needs protecting.
func ScanFS(fsys fs.FS) (Refs, error) {
	var found Refs
	walk := func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name != "." && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !isManifest(name) {
			return nil
		}
		refs, scanErr := scanFile(fsys, name)
		if scanErr != nil {
			return scanErr
		}
		found = append(found, refs...)
		return nil
	}
	if err := fs.WalkDir(fsys, ".", walk); err != nil {
		return nil, fmt.Errorf("scan manifests: %w", err)
	}
	return found.Sorted(), nil
}

// isManifest reports whether name is a YAML file, the only kind of file a
// GitOps repository declares images in.
func isManifest(name string) bool {
	switch path.Ext(name) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// scanFile returns the image references named in one manifest, in file order.
func scanFile(fsys fs.FS, name string) (Refs, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = file.Close() }()

	var refs Refs
	lines := bufio.NewScanner(file)
	lines.Buffer(nil, maxLineBytes)
	for lines.Scan() {
		value, isImage := imageValue(lines.Text())
		if !isImage {
			continue
		}
		ref, parseErr := ParseRef(value)
		if parseErr != nil {
			continue
		}
		refs = append(refs, ref)
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return refs, nil
}

// imageValue extracts the reference from a manifest line of the form
// "<indent>image: <ref>" or "<indent>- image: <ref>", trimmed of surrounding
// whitespace, of any trailing YAML comment, and of matching quotes.
//
// The leading whitespace is required: it is what separates a container's image
// key, which is always nested inside a pod spec, from a top-level document key
// that merely happens to be called "image".
func imageValue(line string) (string, bool) {
	body := strings.TrimLeft(line, " \t")
	if len(body) == len(line) {
		return "", false
	}
	if item, isItem := strings.CutPrefix(body, "-"); isItem {
		trimmed := strings.TrimLeft(item, " \t")
		if len(trimmed) == len(item) {
			return "", false // "-image: x" is a scalar, not a list item
		}
		body = trimmed
	}
	value, isImage := strings.CutPrefix(body, imageKey)
	if !isImage {
		return "", false
	}
	if len(strings.TrimLeft(value, " \t")) == len(value) {
		return "", false // YAML requires whitespace after the key
	}
	value = unquote(strings.TrimSpace(stripComment(value)))
	return value, value != ""
}

// stripComment removes a trailing YAML comment: a '#' preceded by whitespace.
// A '#' without one belongs to the scalar.
func stripComment(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}

// unquote removes one matching pair of surrounding quotes.
func unquote(s string) string {
	const pair = 2
	if len(s) < pair {
		return s
	}
	if (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
