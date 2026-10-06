## Scope: this pull request only

This repository is mostly data, so review only what the PR contains: the diff file, the changed files, the description, the comments, and the review threads. Read the PR diff file. Open a changed file only when you need lines around a hunk. Do not search or read the rest of the repository, and do not look for usages, tests, or documentation elsewhere.

Run a light security pass over the diff and report anything concrete as a finding with category `security`: secrets or tokens, `curl | bash` and other downloads without a pinned version or checksum in build steps, removed or weakened checksums, and URLs pointing at unexpected hosts.
