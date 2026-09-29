#!/bin/sh
set -eu

prefix=${PREFIX:-"$HOME/.local"}
binary=
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
    cat <<'USAGE'
Install losh from a source checkout or an existing binary.

Usage:
  ./scripts/install.sh [--prefix DIR]
  ./scripts/install.sh --binary PATH [--prefix DIR]

Options:
  --binary PATH  Install an already-built binary instead of building with Go.
  --prefix DIR   Installation prefix. Defaults to $HOME/.local.
  -h, --help     Show this help.

The executable is installed at PREFIX/bin/losh. This script does not modify
shell startup files and does not install anything on an SSH target.
USAGE
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --binary)
            [ "$#" -ge 2 ] || { echo "install.sh: --binary requires a path" >&2; exit 2; }
            binary=$2
            shift 2
            ;;
        --prefix)
            [ "$#" -ge 2 ] || { echo "install.sh: --prefix requires a directory" >&2; exit 2; }
            prefix=$2
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "install.sh: unknown option: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

tmp_dir=
cleanup() {
    if [ -n "$tmp_dir" ]; then
        rm -rf "$tmp_dir"
    fi
}
trap cleanup EXIT HUP INT TERM

if [ -z "$binary" ]; then
    command -v go >/dev/null 2>&1 || {
        echo "install.sh: Go 1.22+ is required to build from source." >&2
        echo "Pass --binary PATH to install a prebuilt losh binary." >&2
        exit 1
    }
    tmp_dir=$(mktemp -d)
    echo "Building losh..."
    (cd "$repo_root" && go build -o "$tmp_dir/losh" ./cmd/losh)
    binary=$tmp_dir/losh
fi

[ -f "$binary" ] || { echo "install.sh: binary not found: $binary" >&2; exit 1; }
[ -x "$binary" ] || chmod u+x "$binary"

install_dir=$prefix/bin
mkdir -p "$install_dir"
install -m 0755 "$binary" "$install_dir/losh"

echo "Installed losh to $install_dir/losh"

case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *)
        echo
        echo "$install_dir is not currently in PATH."
        echo "Add it to your shell configuration, then open a new shell:"
        echo '  export PATH='"$install_dir"':$PATH'
        ;;
esac

missing=
command -v ssh >/dev/null 2>&1 || missing="$missing ssh"
command -v codex >/dev/null 2>&1 || missing="$missing codex"
if [ -n "$missing" ]; then
    echo
    echo "Missing runtime dependencies:$missing" >&2
    exit 1
fi

echo
echo "Runtime checks:"
echo "  ssh:   $(command -v ssh)"
echo "  codex: $(command -v codex)"
echo
echo "Next:"
echo "  codex login status"
echo "  ssh user@host true"
echo "  losh user@host"
