# Releasing the CLI

This is the runbook for cutting a release and for the one-time setup the
optional release steps need. Commands use placeholders in `<angle
brackets>`; never commit real values.

## How a release runs

1. Land the release on `main` (the version bump PR updates
   `CHANGELOG.md`: move `## Unreleased` under a `## X.Y.Z (date)` heading).
2. Tag the merge commit and push the tag:

   ```bash
   git switch main && git pull
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

3. The tag starts two workflows:
   - [`release`](../.github/workflows/release.yaml) runs GoReleaser
     ([`.goreleaser.yaml`](../.goreleaser.yaml)):
     1. runs `go run ./tools/gendocs` to generate shell completions and man
        pages;
     2. cross-compiles `norcube` for darwin, linux (amd64, arm64) and
        windows (amd64);
     3. signs and notarizes the darwin binaries (only with the Apple
        secrets; see below);
     4. builds the archives (`norcube_<os>_<arch>.tar.gz`, `.zip` on
        Windows), the Linux packages (`.deb`, `.rpm`, `.apk`) and one SPDX
        SBOM per archive (syft);
     5. writes `checksums.txt` and signs it keylessly with cosign, using
        the workflow's GitHub OIDC identity, into
        `checksums.txt.sigstore.json`;
     6. creates the GitHub release with all of the above;
     7. updates the Homebrew cask, the Scoop manifest and opens the winget
        pull request (each only when its token is set);
     8. records SLSA build provenance for every file in `checksums.txt` as
        a GitHub artifact attestation.
   - [`docs-reference`](../.github/workflows/docs-reference.yaml) opens a
     pull request with the regenerated command reference on the docs
     repository.
4. Check the release page: the assets above are attached, and the
   workflow run's summary lists a notice for every optional step that was
   skipped.

A release without any of the optional secrets still succeeds; it is then
an unsigned, unnotarized release that only updates GitHub. `checksums.txt`,
its Sigstore signature, the SBOMs and the attestations need no secrets.

### Try it locally first

GoReleaser and syft versions match the workflow
(`.github/workflows/release.yaml`):

```bash
go run github.com/goreleaser/goreleaser/v2@v2.18.3 check
GOBIN="$PWD/bin" go install github.com/anchore/syft/cmd/syft@v1.54.1
PATH="$PWD/bin:$PATH" go run github.com/goreleaser/goreleaser/v2@v2.18.3 \
  release --snapshot --clean --skip=publish,sign
ls dist/
rm -rf dist/ bin/
```

The snapshot builds every archive, package and SBOM and renders the
Homebrew, Scoop and winget manifests into `dist/` without uploading
anything. `--skip=sign` is needed locally because keyless signing needs
the workflow's OIDC token. Signing and notarization run locally too if you
export the `MACOS_*` variables.

## Secrets

Set them in the repository settings (Settings, Secrets and variables,
Actions) or with `gh secret set <NAME> --repo norcubeplatform/cli`. Every
one of them is optional.

| Secret | Enables | Without it |
| --- | --- | --- |
| `MACOS_SIGN_P12` | Signing the darwin binaries with the Developer ID Application certificate (base64 of the `.p12`) | darwin binaries ship unsigned |
| `MACOS_SIGN_PASSWORD` | The password of that `.p12`; signing needs both | as above |
| `MACOS_NOTARY_ISSUER_ID` | Notarization with Apple (App Store Connect API issuer id) | signed but not notarized |
| `MACOS_NOTARY_KEY_ID` | Notarization (API key id) | as above |
| `MACOS_NOTARY_KEY` | Notarization (base64 of the API key `.p8`) | as above |
| `HOMEBREW_TAP_TOKEN` | Pushing the cask to `norcubeplatform/homebrew-tap` | the cask is rendered but not pushed |
| `SCOOP_BUCKET_TOKEN` | Pushing the manifest to `norcubeplatform/scoop-bucket` | the manifest is rendered but not pushed |
| `WINGET_TOKEN` | Pushing the manifests to the `norcubeplatform/winget-pkgs` fork and opening a pull request on `microsoft/winget-pkgs` | no winget pull request |
| `DOCS_REPO_TOKEN` (+ variable `DOCS_REPO`) | The command reference pull request on the docs repository | skipped with a notice |

`GITHUB_TOKEN` is provided by Actions. The release job gets `contents:
write` (the release), `id-token: write` (Sigstore and attestations) and
`attestations: write`; nothing else.

## One-time setup

Do these in order; each step can be done on its own, and a release picks
it up as soon as the secret exists.

### 1. Developer ID Application certificate (macOS signing)

Needs the Account Holder role of the Apple Developer Program team (only
the Account Holder can create Developer ID certificates). The certificate
is valid for five years.

1. Create a certificate signing request on a Mac:
   1. Open Keychain Access, then Keychain Access menu, Certificate
      Assistant, Request a Certificate From a Certificate Authority.
   2. User Email Address: `<your Apple ID email>`; Common Name: `Norcube
      Developer ID`; CA Email Address: empty; Request is: Saved to disk.
   3. Save `CertificateSigningRequest.certSigningRequest`. Keychain
      Access keeps the matching private key in the login keychain.
2. Create the certificate:
   1. Go to <https://developer.apple.com/account/resources/certificates/list>
      and click the + button.
   2. Choose Developer ID Application, then the G2 Sub-CA profile.
   3. Upload the `.certSigningRequest` and download
      `developerID_application.cer`.
   4. Double-click the `.cer` so Keychain Access imports it next to the
      private key.
3. Export the certificate with its private key:
   1. In Keychain Access, open the login keychain, My Certificates.
   2. Select `Developer ID Application: <Team name> (<TEAM_ID>)`
      (expand it to make sure the private key is under it).
   3. File, Export Items, format Personal Information Exchange (.p12),
      save as `developer-id.p12`, and set a strong export password.
4. Check the file and store it as secrets:

   ```bash
   # prints the certificate subject when the password is right
   openssl pkcs12 -in developer-id.p12 -nokeys -passin 'pass:<p12-password>' \
     | openssl x509 -noout -subject

   base64 -i developer-id.p12 | tr -d '\n' \
     | gh secret set MACOS_SIGN_P12 --repo norcubeplatform/cli
   gh secret set MACOS_SIGN_PASSWORD --repo norcubeplatform/cli   # prompts for the value
   ```

5. Delete `developer-id.p12` and the `.cer` from disk (the key stays in
   your keychain), or keep the `.p12` in the team's password manager.

### 2. App Store Connect API key (notarization)

1. Sign in to <https://appstoreconnect.apple.com> and open Users and
   Access, Integrations, App Store Connect API, Team Keys. The first time,
   the Account Holder has to request access to the API.
2. Click Generate API Key (or +), name it `norcube-cli-notary`, choose the
   Developer role, and generate.
3. Download the key (`AuthKey_<KEY_ID>.p8`). Apple lets you download it
   only once.
4. Note the Key ID (in the keys table) and the Issuer ID (above the
   table).
5. Store them:

   ```bash
   gh secret set MACOS_NOTARY_ISSUER_ID --repo norcubeplatform/cli   # paste the Issuer ID
   gh secret set MACOS_NOTARY_KEY_ID --repo norcubeplatform/cli      # paste the Key ID
   base64 -i AuthKey_<KEY_ID>.p8 | tr -d '\n' \
     | gh secret set MACOS_NOTARY_KEY --repo norcubeplatform/cli
   ```

6. Delete the `.p8` from disk or keep it in the password manager. Revoke
   the key in the same page if it leaks; it has no expiry otherwise.

After the next release, check a darwin binary on a Mac:

```bash
tar -xzf norcube_darwin_arm64.tar.gz norcube
codesign -dv --verbose=2 ./norcube                 # Authority=Developer ID Application: ...
spctl --assess --type execute --verbose ./norcube  # source=Notarized Developer ID
```

Notarization of a bare binary can't be stapled, so Gatekeeper checks it
online on first launch.

### 3. Homebrew tap and Scoop bucket

The tap (`norcubeplatform/homebrew-tap`) already exists. Create the
bucket once:

```bash
gh repo create norcubeplatform/scoop-bucket --public --add-readme \
  --description "Scoop bucket for the Norcube CLI"
```

GoReleaser writes `Casks/norcube.rb` to the tap and `bucket/norcube.json`
to the bucket.

Create one fine-grained personal access token per repository, so each
token can write to exactly one repository:

1. Open <https://github.com/settings/personal-access-tokens/new> (use an
   account with write access to the repository; a dedicated bot account is
   best).
2. Token name: `norcube-cli homebrew-tap` (or `... scoop-bucket`).
   Resource owner: `norcubeplatform`. Expiration: the longest the
   organization allows; add a calendar reminder to rotate it.
3. Repository access: Only select repositories, then
   `norcubeplatform/homebrew-tap` (or `norcubeplatform/scoop-bucket`).
4. Permissions, Repository permissions: Contents: Read and write.
   Metadata: Read-only is added automatically. Nothing else.
5. Generate (an organization owner may have to approve the request under
   Organization settings, Personal access tokens, Pending requests).
6. Store it:

   ```bash
   gh secret set HOMEBREW_TAP_TOKEN --repo norcubeplatform/cli   # paste the tap token
   gh secret set SCOOP_BUCKET_TOKEN --repo norcubeplatform/cli   # paste the bucket token
   ```

If the current `HOMEBREW_TAP_TOKEN` is a classic token, replace it with a
fine-grained one as above.

### 4. winget

winget packages are published by pull request to
[microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs);
GoReleaser pushes the manifests (`manifests/n/Norcube/CLI/<version>/`) to
a branch of a fork and opens the pull request. Stays off until
`WINGET_TOKEN` is set, so set it last.

1. Fork the repository into the organization:

   ```bash
   gh repo fork microsoft/winget-pkgs --org norcubeplatform \
     --fork-name winget-pkgs --clone=false --default-branch-only
   ```

2. Create a classic personal access token with the `public_repo` scope
   at <https://github.com/settings/tokens/new>, on an account that can push
   to the fork (a bot account is best). A fine-grained token is limited to
   one resource owner, so it can push to the fork but can't open the pull
   request on `microsoft/winget-pkgs`.
3. Sign the Microsoft CLA with that account: the CLA bot asks on its
   first pull request.
4. Store it:

   ```bash
   gh secret set WINGET_TOKEN --repo norcubeplatform/cli   # paste the token
   ```

5. The first pull request for the `Norcube.CLI` identifier goes through
   Microsoft's validation and a moderator review, which can take days.
   Answer review comments on the pull request; later versions are usually
   merged automatically once validation passes. `winget install
   Norcube.CLI` works after the first pull request is merged.

Sync the fork now and then (`gh repo sync norcubeplatform/winget-pkgs`)
so its branches start from a recent upstream.

## Rotating or removing a step

- Delete a secret to switch its step off; the next release skips it with
  a notice.
- Developer ID certificates last five years: create a new one (step 1)
  before the old one expires and replace both `MACOS_SIGN_*` secrets.
  Releases signed with the old certificate stay valid.
- Fine-grained tokens expire; regenerate them (step 3) and update the
  secret.
- Once every release is notarized, the cask's post-install hook that
  clears the quarantine attribute (`homebrew_casks.hooks` in
  `.goreleaser.yaml`) is no longer needed and can be removed.
