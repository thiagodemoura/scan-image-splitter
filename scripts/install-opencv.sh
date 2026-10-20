#!/usr/bin/env bash
# Installs the native OpenCV libraries required by GoCV on Linux and macOS, if missing.
#
# The OpenCV version is taken from the GoCV module (go.mod, or latest if the
# project does not depend on GoCV yet), so the two always match.
#   Linux: builds OpenCV from source via GoCV's Makefile (uses sudo; 30-90 minutes).
#   macOS: installs OpenCV with Homebrew.
#
# Usage:
#   ./scripts/install-opencv.sh           # install only if needed
#   ./scripts/install-opencv.sh --force   # reinstall even if already installed
set -euo pipefail

force=0
[[ "${1:-}" == "--force" ]] && force=1

cd "$(dirname "$0")/.."

gocv_version=$(go list -m -f '{{.Version}}' gocv.io/x/gocv 2>/dev/null || true)
[[ -z "$gocv_version" ]] && gocv_version=latest
gocv_dir=$(go mod download -json "gocv.io/x/gocv@$gocv_version" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')
[[ -d "$gocv_dir" ]] || { echo "could not download gocv.io/x/gocv@$gocv_version" >&2; exit 1; }

opencv_version=$(sed -n 's/^OPENCV_VERSION?=\(.*\)$/\1/p' "$gocv_dir/Makefile")
[[ -n "$opencv_version" ]] || { echo "could not determine OpenCV version from $gocv_dir/Makefile" >&2; exit 1; }
echo "GoCV $gocv_version requires OpenCV $opencv_version"

installed=$(pkg-config --modversion opencv4 2>/dev/null || true)
if [[ "$installed" == "$opencv_version" && $force -eq 0 ]]; then
    echo "OpenCV $opencv_version is already installed"
else
    [[ -n "$installed" ]] && echo "Found OpenCV $installed, need $opencv_version"
    case "$(uname -s)" in
        Darwin)
            command -v brew >/dev/null || { echo "Homebrew is required: https://brew.sh" >&2; exit 1; }
            brew install opencv pkgconfig
            brew upgrade opencv 2>/dev/null || true
            installed=$(pkg-config --modversion opencv4)
            if [[ "$installed" != "$opencv_version" ]]; then
                echo "warning: Homebrew provides OpenCV $installed, GoCV expects $opencv_version" >&2
            fi
            ;;
        Linux)
            # The module cache is read-only, so run GoCV's Makefile from a copy.
            work=$(mktemp -d)
            trap 'rm -rf "$work"' EXIT
            cp -r "$gocv_dir/." "$work"
            chmod -R u+w "$work"
            make -C "$work" install
            ;;
        *)
            echo "unsupported OS: $(uname -s) (use scripts/install-opencv.ps1 on Windows)" >&2
            exit 1
            ;;
    esac
fi

echo "Verifying..."
(cd "$gocv_dir" && CGO_ENABLED=1 go run ./cmd/version/main.go)
