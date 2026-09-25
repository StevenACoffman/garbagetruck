# Listing Artifact Registry Images by Prefix in Go

This document provides a complete, production-ready Go script using the official Google Cloud Artifact Registry client library to list Docker packages that match a specific sub-path or prefix pattern.

## Prerequisites

Before running the script, ensure you have the required Go modules installed:

```bash
go get cloud.google.com/go/artifactregistry/apiv1
go get google.golang.org/api/iterator
```

## Go Implementation

```go
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/api/iterator"
)

func main() {
	ctx := context.Background()

	// Initialize the Artifact Registry Client
	client, err := artifactregistry.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// 1. Define your desired target prefix URL
	targetPrefixURL := "us-central1-docker.pkg.dev/khan-academy/districts-jobs"

	// 2. Parse the URL parts to build the GCP API hierarchical Resource Path
	// Template URL format: {location}-docker.pkg.dev/{project-id}/{repository-id}/{optional-subpaths}
	urlParts := strings.Split(targetPrefixURL, "/")
	if len(urlParts) < 4 {
		log.Fatalf("Invalid URL prefix format. Expected at least 'location-docker.pkg.dev/project/repo'")
	}

	// Extract core details
	locationHost := urlParts[0]
	location := strings.Split(locationHost, "-docker.pkg.dev")[0]
	projectID := urlParts[1]
	repoName := urlParts[2]

	// Reconstruct the internal sub-path prefix we need to match against packages
	// Example: "districts-jobs" or "districts-jobs/nested-group"
	packageSubpathPrefix := strings.Join(urlParts[2:], "/")

	// Construct the parent path resource name for GCP's API
	parentPath := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)

	fmt.Printf("Scanning repository: %s\n", parentPath)
	fmt.Printf("Filtering packages starting with: %s\n\n", packageSubpathPrefix)

	// 3. List all Packages in the repository
	req := &artifactregistrypb.ListPackagesRequest{
		Parent: parentPath,
	}
	it := client.ListPackages(ctx, req)

	foundMatches := false
	for {
		pkg, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Fatalf("Error iterating through packages: %v", err)
		}

		// GCP package names return a relative path format.
		// Example: "projects/khan-academy/locations/us-central1/repositories/districts-jobs/packages/ltv2-to-assessments"
		// We want to pull out just the local identifier after /packages/
		parts := strings.Split(pkg.GetName(), "/packages/")
		if len(parts) < 2 {
			continue
		}
		localPackageName := parts[1]

		// Prepend the repository name back to fit your match criteria 
		// (converts "ltv2-to-assessments" to "districts-jobs/ltv2-to-assessments")
		fullyQualifiedLocalName := fmt.Sprintf("%s/%s", repoName, localPackageName)

		// 4. Filter packages using string matching
		if strings.HasPrefix(fullyQualifiedLocalName, packageSubpathPrefix) {
			foundMatches = true
			
			// Re-assemble the external user-facing Docker registry URL path
			fullDockerURL := fmt.Sprintf("%s-docker.pkg.dev/%s/%s", location, projectID, fullyQualifiedLocalName)
			fmt.Printf("👉 Found Image: %s\n", fullDockerURL)
		}
	}

	if !foundMatches {
		fmt.Println("No Docker images matched the specified prefix pattern.")
	}
}
```

## How It Works

1. **Hierarchy Mapping:** In Google Artifact Registry, a Docker repository maps to an overall container registry repository layer, inside of which live unique **Packages** (base image paths like `districts-jobs/ltv2-to-assessments`) and **Versions** (the image tags and shas).
2. **String Matching:** The script breaks down the requested prefix URL to extract the project, location, and root repository. It fetches all packages within that repository, evaluates their paths, and uses `strings.HasPrefix` to filter matching sub-directories natively.
3. **Automatic Pagination:** The standard `client.ListPackages` operation yields a streaming `iterator` that transparently streams chunks of registry packages into memory, safely accommodating production registries that host thousands of individual containers.