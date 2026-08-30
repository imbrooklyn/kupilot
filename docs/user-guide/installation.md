# Installing a Release Archive

> [!NOTE]
> This page documents the historical `0.3.0` archive shape. There is no
> documented current `v0.4` archive in this repository; build the current
> candidate from reviewed source unless a separately verified release is
> published.

Kupilot release archives target macOS and Linux on `amd64` and `arm64`.
Windows is experimental and has no `v0.3` release archive.

Release archives are CGO-free and contain exactly two files:

```text
LICENSE
kupilot
```

The binaries are not code-signed or notarized. SHA-256 checksums detect a
damaged or substituted download only when the checksum file itself comes from
a trusted release page; they are not publisher signatures.

## Select the archive

Use the operating-system and machine mapping below:

| `uname -s` | `uname -m` | Archive suffix |
| --- | --- | --- |
| `Darwin` | `x86_64` | `darwin_amd64.tar.gz` |
| `Darwin` | `arm64` | `darwin_arm64.tar.gz` |
| `Linux` | `x86_64` | `linux_amd64.tar.gz` |
| `Linux` | `aarch64` or `arm64` | `linux_arm64.tar.gz` |

For version `0.3.0`, the complete filename starts with `kupilot_0.3.0_`.
Download that archive, `kupilot_0.3.0_checksums.txt`, and the archive's sibling
`.spdx.json` SBOM from the same official release.

## Verify before extraction

Work in a new directory containing the downloaded files. Set `archive` to the
exact filename you selected. On macOS:

```sh
archive=kupilot_0.3.0_darwin_arm64.tar.gz
awk -v name="$archive" '$2 == name { print }' kupilot_0.3.0_checksums.txt |
  shasum -a 256 --check -
```

On Linux:

```sh
archive=kupilot_0.3.0_linux_amd64.tar.gz
awk -v name="$archive" '$2 == name { print }' kupilot_0.3.0_checksums.txt |
  sha256sum -c -
```

The command must print the selected archive followed by `OK`. An empty result,
missing checksum entry, mismatch, or malformed checksum is a failure: do not
extract or run the archive. The sibling SBOM can be verified by repeating the
command with its full `.spdx.json` filename.

## Extract and inspect

List the archive before extraction:

```sh
tar -tzf "$archive"
```

Proceed only when the list contains exactly `LICENSE` and `kupilot`. Then
extract and inspect the embedded build metadata:

```sh
tar -xzf "$archive"
./kupilot --version
```

The version output must match the release, selected platform, and expected
source commit. It also reports Go 1.25.13 and a UTC source-commit time. Stop if
any field is unexpected.

## Install for one user

Choose a user-owned directory already present in `PATH`. For example:

```sh
install_dir="$HOME/.local/bin"
mkdir -p "$install_dir"
install -m 0755 kupilot "$install_dir/kupilot"
kupilot --version
```

If `$HOME/.local/bin` is not in `PATH`, select another user-owned directory or
update the shell configuration according to local policy. Do not run an
unverified binary with elevated privileges. If macOS policy rejects the
unsigned or unnotarized binary, do not disable platform security controls;
follow the organization's approval process or build the reviewed source.

Continue with the [Getting Started Guide](getting-started.md) for
configuration, least-privilege Kubernetes access, informed consent, and a new
Session. Release construction, SBOM details, and maintainer verification are
documented in the [Release Process](../release.md).
