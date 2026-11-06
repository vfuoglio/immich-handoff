# Releasing

For the maintainer. A release is a git tag; everything else is done by the release workflow (`.github/workflows/release.yml`).

## What a release produces

- the container image `vfuoglio/immich-handoff` on Docker Hub, and the identical `ghcr.io/vfuoglio/immich-handoff` on GitHub's registry, for amd64 and arm64, tagged `1.2.3`, `1.2` and, if it is the highest version released so far, `latest` (a version with a suffix such as `1.2.3-rc1` gets only its own tag, and not `X.Y` or `latest`)
- a signature for each image, a software bill of materials and build provenance (see [Verifying an image](#verifying-an-image))
- a GitHub Release named after the tag, whose text is that version's section of `CHANGELOG.md`

## One-time setup

The workflow needs two repository secrets. Docker's and GitHub's menus change from time to time, so look for the names rather than the exact path.

1. **On Docker Hub,** open Settings and choose **Personal access tokens**. Create a new token with **Read & Write** access, give it a name such as `immich-handoff release`, and copy it. It is shown only once.
2. **On GitHub,** open the repository's Settings, then **Secrets and variables**, then **Actions**, and add two repository secrets:
   - `DOCKERHUB_USERNAME`: the Docker Hub account or organisation that owns the image (`vfuoglio`)
   - `DOCKERHUB_TOKEN`: the token you just copied

Without them the release fails at the Docker Hub login, before anything is published. The first push creates the Docker Hub repository; check on Docker Hub that it is public. GitHub's registry needs no secret.

## Versions

[Semantic versioning](https://semver.org). A fix is a patch (1.1.1), a new setting or feature a minor (1.2.0), and a change that makes existing installs behave differently without their choosing it a major (2.0.0). `docker-compose.yml` names the image by `major.minor` (for example `1.1`), so people on it receive patches by pulling and choose when to move to a new minor.

## Checklist

1. **Changelog.** In `CHANGELOG.md` rename `## Unreleased` content to `## X.Y.Z` (and leave an empty `## Unreleased` above it). The release fails without a section for the version being released.
2. **Compose file.** If the minor version changed, update the image tag in `docker-compose.yml` (`:X.Y`).
3. **Test.** CI must be green on `main`. Locally: `go test ./...` and `test/run.sh`. The release workflow runs both again before it builds anything.
4. **Try the workflow first when it changed.** In GitHub: Actions, **release**, **Run workflow**, version `0.0.0-test`. It runs the same checks and publishes only the image tag `0.0.0-test`: no `latest`, no GitHub Release. Pull it and look at it, then delete that version from the package page.
5. **Tag and push.**
   ```
   git tag -a vX.Y.Z -m "X.Y.Z"
   git push origin vX.Y.Z
   ```
   GitHub starts no tag-triggered run when more than three tags are pushed at once; use the manual run with the version in that case (it publishes the image but not the GitHub Release).
6. **Watch the run.** Steps: tests, version check, changelog check, build and smoke test for both architectures, publish, create the release.
7. **First release only: check both images are public.** On Docker Hub the repository must be Public. On GitHub, open the repository's Packages, `immich-handoff`, Package settings, and check the visibility is Public (a package that a workflow creates may start private) and that it is linked to the repository.
8. **Check it from outside.** From a machine that is not logged in to either registry: `docker run --rm vfuoglio/immich-handoff:X.Y.Z --version` and the same with `ghcr.io/vfuoglio/immich-handoff:X.Y.Z` both print `X.Y.Z`.
9. **Point the documentation at the new release.** After the release exists, change the version in the clone command in `README.md` and `docs/install.md` (`git clone --branch vX.Y.Z`, and the "latest release is" wording). People are told to start from a release and never from `main`, so these must name a tag that exists. Do this in a small change after tagging, not before.

## What the workflow checks before it publishes

- the whole CI: unit tests (with the race detector), generated code up to date, and the end-to-end tests against a real Immich
- the version looks like `1.2.3` or `1.2.3-rc1`
- (tag pushes) `CHANGELOG.md` has a non-empty section for it
- for **each** architecture, the freshly built image reports the right version with `--version`, and with no configuration it refuses to start and says `WEBHOOK_SECRET` is missing. That proves the binary runs, as the unprivileged user, on that architecture; it does not start a real Immich (the end-to-end tests do that with the binary, not the image)

If any check fails nothing is published. After the publish step, one more check can still fail the release (and then the images are already out, so fix forward with a new patch): each image is signed, the signature is verified against this repository's release workflow, and an SBOM is found attached to it.

## Verifying an image

Every released image is signed with [cosign](https://github.com/sigstore/cosign), without a key of ours: the signature is tied to this repository's release workflow by GitHub's identity token. It also carries a software bill of materials (SBOM) and build provenance. To check one:

```
cosign verify \
  --certificate-identity-regexp '^https://github\.com/vfuoglio/immich-handoff/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  vfuoglio/immich-handoff:1.2.0

docker buildx imagetools inspect vfuoglio/immich-handoff:1.2.0 --format '{{json .SBOM}}'
docker buildx imagetools inspect vfuoglio/immich-handoff:1.2.0 --format '{{json .Provenance}}'
```

`cosign verify` prints the signature's details and fails if the image was not signed by that workflow. The SBOM lists the Go modules the program was built from; the provenance says which commit and workflow run built the image. The same works for `ghcr.io/vfuoglio/immich-handoff`.

## Dependency updates

Dependabot (`.github/dependabot.yml`) opens a pull request each Monday into `develop`, one for each kind: the GitHub Actions, the program's Go modules, the screenshot tool's modules, and the base images in the `Dockerfile`. The actions in `.github/workflows` are pinned by commit hash, with the version in a comment, so a changed action is always a change you see in a pull request; Dependabot updates the hash and the comment together. Read the notes in the pull request, check that CI is green, and merge it into `develop` like any other change. After a change to the release workflow, run the manual `0.0.0-test` release before the next real tag.

## Patching an older version

When 1.2.0 is out and 1.1 needs a fix, release `1.1.1` without disturbing what people on 1.2 get:

```
git switch -c release/1.1 v1.1.0
git cherry-pick <the fix>
# add a "## 1.1.1" section to CHANGELOG.md, commit
git tag -a v1.1.1 -m "1.1.1" && git push origin release/1.1 v1.1.1
```

The release workflow runs on any tag, so this works from the branch. It moves the `1.1` tag to the fix, so people on `:1.1` receive it, and it leaves `latest` on the newest release, because `latest` goes only to the highest final version released so far (`.github/scripts/latest.sh` decides, and CI tests it). Merge the fix into `develop` as well, so it is not lost in the next minor version.

## A bad release

Do not move or delete a tag people may have pulled. Fix forward: release the next patch. If a published image is dangerous, delete that version from the package page, and say so in the changelog.

## Badges

The README badges for the release, the Docker image and the license update themselves. The **Immich 3.2.0+** badge is fixed text: change it, and the requirement in `docs/install.md`, when you have tested against a different minimum Immich version.
