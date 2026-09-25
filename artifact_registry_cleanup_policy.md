# Artifact Registry Cleanup Policy (Go Implementation)

This document provides a complete Go script to configure a 30-day expiration policy on a Google Artifact Registry repository, while explicitly protecting specific image versions using an **OR** logical relationship:
1. **Delete** images older than 30 days.
2. **Keep** the last 5 most recent versions of any image.
3. **Keep** any image versions containing a tag prefix matching `"protected-"`.

## Implementation Script

```go
package main

import (
	"context"
	"fmt"
	"log"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func main() {
	ctx := context.Background()

	// Initialize the Artifact Registry Client
	client, err := artifactregistry.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Replace these placeholders with your actual GCP configuration
	projectID := "your-gcp-project-id"
	location := "us-central1" 
	repoName := "your-docker-repo"

	parentPath := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)

	// Define the map of rules. 
	// ARTIFACT REGISTRY LOGIC: Keep rules act as an "OR" logical evaluation. 
	// If any KEEP rule matches an image version, it overrides the global DELETE rule.
	cleanupPolicies := map[string]*artifactregistrypb.CleanupPolicy{
		
		// 1. Global Purge Rule: Deletes anything older than 30 days
		"delete-after-30-days": {
			Id:     "delete-after-30-days",
			Action: artifactregistrypb.CleanupPolicy_DELETE,
			ConditionType: &artifactregistrypb.CleanupPolicy_Condition{
				Condition: &artifactregistrypb.CleanupPolicy_Condition_{
					OlderThan: &durationpb.Duration{
						Seconds: int64(30 * 24 * 60 * 60), // 30 days in seconds
					},
				},
			},
		},

		// 2. Safeguard Rule A: Keep the 5 most recent versions of any image
		"keep-last-5-versions": {
			Id:     "keep-last-5-versions",
			Action: artifactregistrypb.CleanupPolicy_KEEP,
			ConditionType: &artifactregistrypb.CleanupPolicy_MostRecentVersions_{
				MostRecentVersions: &artifactregistrypb.CleanupPolicy_MostRecentVersions{
					KeepCount: proto.Int32(5), // Protects the 5 most recent entries
				},
			},
		},

		// 3. Safeguard Rule B: Keep anything explicitly tagged with "protected-"
		"keep-protected-string-tags": {
			Id:     "keep-protected-string-tags",
			Action: artifactregistrypb.CleanupPolicy_KEEP,
			ConditionType: &artifactregistrypb.CleanupPolicy_Condition{
				Condition: &artifactregistrypb.CleanupPolicy_Condition_{
					TagNamePrefixes: []string{"protected-"},
				},
			},
		},
	}

	// Prepare the repository update payload
	req := &artifactregistrypb.UpdateRepositoryRequest{
		Repository: &artifactregistrypb.Repository{
			Name:            parentPath,
			CleanupPolicies: cleanupPolicies,
		},
		// UpdateMask restricts the configuration payload strictly to cleanup_policies
		UpdateMask: &fieldmaskpb.FieldMask{
			Paths: []string{"cleanup_policies"},
		},
	}

	// Execute the update request against the Google Cloud API
	repo, err := client.UpdateRepository(ctx, req)
	if err != nil {
		log.Fatalf("Failed to update cleanup policies: %v", err)
	}

	fmt.Printf("Successfully synchronized policies to %s\n", repo.GetName())
}
```

## Prerequisite Commands

To pull down the dependencies required by this script, execute the following commands in your Go workspace:

```bash
go get cloud.google.com/go/artifactregistry/apiv1
go get google.golang.org/genproto/googleapis/devtools/artifactregistry/v1
go get google.golang.org/protobuf/types/known/durationpb
go get google.golang.org/protobuf/proto
```

## Key Engineering Takeaways

* **Precedence Order:** `KEEP` actions always take functional precedence over `DELETE` actions. An image will never be deleted if it satisfies at least one active keep constraint.
* **Why Multiple Keep Rules?** Splitting the count policy (`keep-last-5-versions`) and the pattern policy (`keep-protected-string-tags`) ensures an **OR** relationship. Putting them in a single policy maps them as an **AND** constraint (requiring the image to be both within the last 5 versions *and* prefixed with `protected-`).
* **Field Masks Protection:** Passing `cleanup_policies` inside the `UpdateMask` prevents the payload from modifying other parameters (like Customer-Managed Encryption Keys or access configurations) on your repository.