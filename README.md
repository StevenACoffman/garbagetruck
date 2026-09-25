<!-- vale Vale.Spelling = NO -->
<!-- rumdl-disable-next-line MD063 -->
# garbagetruck
<!-- vale Vale.Spelling = YES -->

`garbagetruck` removes Google Artifact Registry Docker images that nothing uses
any more. Its first job is the opposite one: knowing exactly which images
something still uses, so that it never removes those.

## What Is Protected

A GitOps Kubernetes manifest repository is the source of truth for which images
a cluster runs. `garbagetruck protected` clones that repository over git+ssh
into a temporary directory, reads every `*.yaml` and `*.yml` file in it, and
collects every `image:` value into a sorted, de-duplicated map of image name to
the versions it pins:

```json
{
  "us-central1-docker.pkg.dev/khan-academy/districts-jobs/cedar_umi_changed": [
    { "tag": "webapp-034d2665381d9ab1eed1784a784c614be173f573" },
    { "tag": "webapp-057cabe8414d1a7723deef50117707cd8e35b982" }
  ]
}
```

Nothing in that map may be deleted.

A tag and a digest are separate fields, so a digest-pinned reference reads
`{ "digest": "sha256:..." }` and a reference that writes both keeps both.
References are parsed with [go-containerregistry][gcr], which also splits an
image name into its registry and its namespace.

An image written without a tag is reported as `latest`, because that is the
image Kubernetes pulls for it. A value that names no image (a templating
placeholder, say) is skipped, because it pins nothing that could be deleted.

[gcr]: https://github.com/google/go-containerregistry

## Usage

```console
$ garbagetruck protected --manifest-repo github.com/Khan/districts-k8s
us-central1-docker.pkg.dev/khan-academy/districts-jobs/cedar_umi_changed
	webapp-034d2665381d9ab1eed1784a784c614be173f573
	webapp-057cabe8414d1a7723deef50117707cd8e35b982
```

Add `--json` for the map above rather than an indented listing.

| Flag                    | Environment variable           | Meaning                                     |
| ----------------------- | ------------------------------ | ------------------------------------------- |
| `-r, --manifest-repo`   | `GARBAGETRUCK_MANIFEST_REPO`   | GitOps repository to read (required)        |
| `-b, --manifest-branch` | `GARBAGETRUCK_MANIFEST_BRANCH` | Branch to read; default is the repository's |
| `--ssh-key`             | `GARBAGETRUCK_SSH_KEY`         | Private key file; default is the ssh-agent  |
| `--json`                | `GARBAGETRUCK_JSON`            | Emit the map as JSON                        |

Write the repository as `github.com/Khan/districts-k8s`, as an `https://` URL,
or in the `git@host:owner/repo.git` form. All three clone over ssh. An explicit
`ssh://` URL is used as given, so a non-standard port or account survives.

Authentication is by ssh-agent. `--ssh-key` reads a private key file instead,
which is what a deployment with a mounted deploy key needs.

## Protecting Those Images in the Registry

`garbagetruck sync` makes a Google Artifact Registry repository agree with the
manifests. It reads every image under a registry prefix and compares it with
the manifests. From that comparison it maintains one family of tags:

- an image the manifests pin by tag gets `protected-` plus that tag;
- an image the manifests pin by digest alone gets `protected-digest-only`;
- a `protected-` tag the manifests no longer justify is removed.

No other tag is touched, and no image is ever deleted.

```console
$ garbagetruck sync --dry-run \
    --manifest-repo github.com/Khan/districts-k8s \
    --registry-prefix us-central1-docker.pkg.dev/khan-academy/districts-jobs
would tag protected-webapp-034d266... on .../ltv2-to-assessments (sha256:1f0c...)
would untag protected-webapp-9b21af0... on .../roster (sha256:77ae...)
```

With `--dry-run` the registry is only read and the output says what would
change. Without it, the same output says what did change. Read-only is
structural rather than a flag consulted deep in the code: the one function that
writes to the registry is not called at all.

| Flag                            | Environment variable                       | Meaning                                  |
| ------------------------------- | ------------------------------------------ | ---------------------------------------- |
| `-p, --registry-prefix`         | `GARBAGETRUCK_REGISTRY_PREFIX`             | Registry subtree to reconcile (required) |
| `-n, --dry-run`                 | `GARBAGETRUCK_DRY_RUN`                     | Describe changes; write nothing          |
| `--impersonate-service-account` | `GARBAGETRUCK_IMPERSONATE_SERVICE_ACCOUNT` | Service account to act as                |

`sync` also takes the `--manifest-repo`, `--manifest-branch`, and `--ssh-key`
flags described above. Registry access uses Application Default Credentials.

`sync` reports two things it cannot fix. A manifest that pins a version the
registry no longer holds means a cluster refers to an image that is already
gone. And because `protected-digest-only` is one name per image, only one
digest-pinned version of an image can hold it. The rest are reported.

## Not Implemented Yet

**The `protected-` tags do nothing on their own.** They only matter alongside a
repository cleanup policy that keeps versions whose tags start with
`protected-`. `garbagetruck` does not install that policy. See
`artifact_registry_cleanup_policy.md` for the policy this convention is built
for. Until it is installed, `sync` maintains tags that nothing consults.

`garbagetruck` also never deletes an image. Deciding what to delete is the
cleanup policy's job. Deciding what to spare is this tool's.
