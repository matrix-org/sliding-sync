0. Ensure that CI passes against the commit you want to release.
1. Tag that release. The tag name should start with `v`. Push it to GitHub.
2. GitHub should see tag and trigger [a GHA workflow](https://github.com/matrix-org/sliding-sync/actions/workflows/release.yml). It should: 
   - build binaries,
   - create a draft release for this tag, and
   - uploads the binaries as artifacts to this release.
3. Go to https://github.com/matrix-org/sliding-sync/releases/ to find your release. Check that the binaries are attached successfully. Write release notes. When you're happy, publish.