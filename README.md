<!-- vale Vale.Spelling = NO -->
<!-- rumdl-disable-next-line MD063 -->
# garbagetruck

<!-- vale Vale.Spelling = YES -->

`garbagetruck` keeps the container images a Kubernetes cluster still runs, and
expires the rest from Google Artifact Registry. A GitOps manifest repository
decides what counts as in use.

## Commands

| Command     | Reads                  | Writes                              |
| ----------- | ---------------------- | ----------------------------------- |
| `protected` | GitOps manifests       | Nothing                             |
| `sync`      | Manifests and registry | `protected-` tags                   |
| `policy`    | Registry               | Cleanup policies                    |
| `sweep`     | Registry               | **Deletes versions.** Needs `--yes` |
| `version`   | Build info             | Nothing                             |

Run `sync` before `policy` or `sweep`. Both spare versions tagged
`protected-`, and only `sync` writes those tags.

`--dry-run` works on every command that writes and means one thing throughout:
`garbagetruck` writes nothing, because the function that writes is not called.

## Flags

Manifests, for `protected` and `sync`:

| Flag                    | Environment variable           | Meaning                                 |
| ----------------------- | ------------------------------ | --------------------------------------- |
| `-r, --manifest-repo`   | `GARBAGETRUCK_MANIFEST_REPO`   | GitOps repository (required)            |
| `-b, --manifest-branch` | `GARBAGETRUCK_MANIFEST_BRANCH` | Branch, default the repository's        |
| `--ssh-key`             | `GARBAGETRUCK_SSH_KEY`         | Private key file, default the ssh-agent |

Registry, for `sync`, `policy` and `sweep`:

| Flag                            | Environment variable                       | Meaning                     |
| ------------------------------- | ------------------------------------------ | --------------------------- |
| `-p, --registry-prefix`         | `GARBAGETRUCK_REGISTRY_PREFIX`             | Registry subtree (required) |
| `--impersonate-service-account` | `GARBAGETRUCK_IMPERSONATE_SERVICE_ACCOUNT` | Service account to act as   |

Retention, for `policy` and `sweep`:

| Flag                  | Environment variable             | Default | Meaning                            |
| --------------------- | -------------------------------- | ------- | ---------------------------------- |
| `--delete-older-than` | `GARBAGETRUCK_DELETE_OLDER_THAN` | `720h`  | Expire versions older than this    |
| `--keep-most-recent`  | `GARBAGETRUCK_KEEP_MOST_RECENT`  | `5`     | Versions per image kept at any age |

Every flag reads a `GARBAGETRUCK_`-prefixed environment variable. Manifest
repositories clone over ssh through the agent. Registry access uses Application
Default Credentials.

Write the manifest repository as `github.com/Khan/districts-k8s`, as an
`https://` URL, or as `git@host:owner/repo.git`. An explicit `ssh://` URL is
used as given, so a non-standard port survives.

## `protected`

Reports what the manifests pin. Reads nothing else and changes nothing.

```console
$ garbagetruck protected -r github.com/Khan/districts-k8s
us-central1-docker.pkg.dev/khan-academy/districts-jobs/cedar_umi_changed
	webapp-034d2665381d9ab1eed1784a784c614be173f573
	webapp-057cabe8414d1a7723deef50117707cd8e35b982
```

`--json` emits a map of image name to the versions it pins, with `tag` and
`digest` as separate fields. An image written without a tag reports as `latest`
because that is what Kubernetes pulls for it, while a value naming no image,
such as a templating placeholder, is skipped entirely.

## `sync`

Makes the registry's `protected-` tags agree with the manifests.

- A version the manifests pin by tag gets `protected-` plus that tag.
- A version pinned by digest alone gets `protected-digest-only`.
- A `protected-` tag the manifests no longer justify is removed.

No other tag is touched and no image is deleted.

```console
$ garbagetruck sync --dry-run \
    -r github.com/Khan/districts-k8s \
    -p us-central1-docker.pkg.dev/khan-academy/districts-jobs
would tag protected-webapp-034d266... on .../ltv2-to-assessments (sha256:1f0c...)
would untag protected-webapp-9b21af0... on .../roster (sha256:77ae...)
```

Two things `sync` reports but cannot fix. A manifest pinning a version the
registry no longer holds means a cluster refers to an image already gone. And
`protected-digest-only` is one name per image, so only one digest-pinned
version of an image can hold it. The rest are named in the output.

## `policy`

Installs the Artifact Registry cleanup policies that give the tags their
effect. Three rules, each scoped to the prefix:

| Rule                                  | Action                                             |
| ------------------------------------- | -------------------------------------------------- |
| `garbagetruck-delete-old-<scope>`     | Delete versions older than `--delete-older-than`   |
| `garbagetruck-keep-recent-<scope>`    | Keep the `--keep-most-recent` newest of each image |
| `garbagetruck-keep-protected-<scope>` | Keep anything tagged `protected-`                  |

`<scope>` is the part of the prefix after the project, so
`us-central1-docker.pkg.dev/khan-academy/districts-jobs` yields
`garbagetruck-delete-old-districts-jobs`. Policy ids only have to be unique
within a repository, so two repositories never collide. Two prefixes sharing a
repository and differing by subpath would, and the scope prevents it.

Artifact Registry combines the rules with a logical or, and a keep rule wins
over a delete rule, so a version survives if any keep rule matches. The two keep rules stay
separate for that reason. Merged into one, a version would have to be both
recent and protected to survive.

Policies `garbagetruck` did not write are left alone and reported. One of your
own that also deletes will delete on top of these, so `policy` warns about it.

```console
$ garbagetruck policy \
    -p us-central1-docker.pkg.dev/khan-academy/districts-jobs \
    --keep-most-recent 5 --delete-older-than 730h
installed garbagetruck-delete-old-districts-jobs
installed garbagetruck-keep-protected-districts-jobs
installed garbagetruck-keep-recent-districts-jobs
turned the registry cleanup pipeline on
```

Stored on the repository, those become:

```json
{"id":"garbagetruck-delete-old-districts-jobs", "action":"DELETE",
 "condition":{"olderThan":"2628000s"}}
```

`730h` is stored as a duration of `2628000s`, or 30.4 days. No rule carries
`packageNamePrefixes` here because the prefix names the whole repository. A
prefix with a subpath adds one to all three rules.

Re-running changes nothing and says `cleanup policies already matched, nothing
written`.

### Two Dry Runs

`policy` holds back two different things, so they have two names, and they
combine.

| Flag                | Holds back                      | Writes to the registry?                   |
| ------------------- | ------------------------------- | ----------------------------------------- |
| `--dry-run`         | `garbagetruck`                  | No. Prints the plan and stops             |
| `--cleanup-dry-run` | The registry's cleanup pipeline | Yes. Installs the rules with deletion off |

`turned the registry cleanup pipeline on` is the line that matters. Without it
the rules are installed but inert.

## `sweep`

Deletes what the retention rules no longer spare, applying the same rules
`policy` installs.

| Rule                                               | Effect |
| -------------------------------------------------- | ------ |
| Tagged `protected-`                                | Keep   |
| Among the `--keep-most-recent` newest of its image | Keep   |
| Created more recently than `--delete-older-than`   | Keep   |
| Anything else                                      | Delete |

A keep rule wins over a delete rule. A version whose creation time the registry
does not report is never deleted, because its age cannot be established.

```console
$ garbagetruck sweep --dry-run \
    -p us-central1-docker.pkg.dev/khan-academy/districts-jobs \
    --keep-most-recent 5 --delete-older-than 730h
.../roster@sha256:3b9579... created 2023-02-16
... and 73737 more
73787 to delete; keeping 31 protected, 312 newest, 5320 too young, 0 undated
```

**Deletion cannot be undone.** Run `--dry-run` first and read the list.

Two guards stand in the way of an accident. Deleting requires `--yes`, which
has no short form on purpose and is checked before the listing, so a missing
one costs a second rather than a full walk of the registry. Passing both
`--dry-run` and `--yes` deletes nothing. Separately, `sweep` refuses to start
when nothing under the prefix carries a `protected-` tag, since that means
`sync` has never run and every image the cluster uses would look expendable.

Once `policy` is installed the registry applies the same rules on its own
schedule. `sweep` applies them now and prints what it removed.

## Scoping to Part of a Repository

A cleanup policy attaches to a repository, so a prefix ending at the repository
covers all of it. A longer prefix is confined with the registry's
`packageNamePrefixes`, which matches a plain string prefix rather than a path
prefix. `sync` matches packages the same way on purpose: were one path-aware
and the other not, a prefix of `ltv2-` would scope the delete rule to
`ltv2-extra` while `sync` left that package unprotected.
