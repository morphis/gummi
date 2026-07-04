#!/usr/bin/env sh
# Creates a throwaway demo repository (a git repo); gummi creates its
# .gummi workspace lazily on first launch. Usage: scripts/demo.sh [dir]
set -eu

dir="${1:-$(mktemp -d "${TMPDIR:-/tmp}/gummi-demo.XXXXXX")}"
bin="$(cd "$(dirname "$0")/.." && pwd)/bin/gummi"

if [ ! -x "$bin" ]; then
    echo "bin/gummi not built — run 'make build' first" >&2
    exit 1
fi

mkdir -p "$dir"
cd "$dir"
if [ ! -d .git ]; then
    # refuse to git-init-and-commit a directory that already has
    # content — this script only scaffolds sandboxes
    if [ -n "$(ls -A .)" ]; then
        echo "$dir is not empty and not a git repo; refusing to scaffold over it" >&2
        exit 1
    fi
    git init -q -b main .
    git config user.name >/dev/null 2>&1 || git config user.name "gummi demo"
    git config user.email >/dev/null 2>&1 || git config user.email "demo@example.invalid"
    printf '# demo\n\nA playground for gummi.\n' > README.md
    printf 'package demo\n' > demo.go
    git add .
    git commit -qm "initial"
fi

# .gummi is created lazily on first launch — no init step needed.
echo
echo "demo repo ready: $dir"
echo "open it with:"
echo "  cd $dir && $bin"
