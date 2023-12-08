0. Ensure that CI passes against the commit you want to release.
1. Bump the version string in `cmd/syncv3.main.go` to the new version. Commit.
2. Tag that commit. The tag name should start with `v`. Push it to GitHub.
3. After the tag is pushed, Github will see the tag and trigger [a GHA workflow](https://github.com/matrix-org/sliding-sync/actions/workflows/docker.yml). It should
   - build docker images,
   - publish them to [the project's container repository](https://github.com/matrix-org/sliding-sync/pkgs/container/sliding-sync),
   - and also run some [third-party vulnerability scanner](https://github.com/matrix-org/sliding-sync/blob/97b21d4d9ea2c151ac5535a46ad9930c5a2a7f32/.github/workflows/docker.yml#L66-L76), for some reason.
4. GitHub should also trigger [a second GHA workflow](https://github.com/matrix-org/sliding-sync/actions/workflows/release.yml), which: 
   - builds binaries,
   - creates a draft release for this tag, and
   - uploads the binaries as artifacts to this release.
5. Go to https://github.com/matrix-org/sliding-sync/releases/ to find your release. 
   - **Edit it in place** (do not create a new release).
   - Check that the binaries are attached successfully.
   - Write release notes.
   - Wait for the docker build to finish. This takes ~10 minutes; check the GHA workflow and the (check [packages repo](https://github.com/matrix-org/sliding-sync/pkgs/container/sliding-sync)).
   - Publish when you're happy with the release notes.

That's it. Relax, take a deep breath, pat yourself on the back, then move on with your life.
