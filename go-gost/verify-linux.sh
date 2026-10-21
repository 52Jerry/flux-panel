#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo 'Unsupported CPU architecture' >&2; exit 1 ;;
esac
verify_file() {
    local checksum
    checksum="$(awk -v file="$1" '$2 == file' SHA256SUMS)"
    [[ -n "$checksum" ]] || { echo "No checksum found for $1" >&2; exit 1; }
    printf '%s\n' "$checksum" | sha256sum --check --strict -
}
binary="gost-$arch"
test_binary="tests/wg-tests-$arch"
verify_file "$binary"
chmod u+x -- "$binary"
./"$binary" -V
if [[ ! -f "$test_binary" ]]; then
    echo "Version check passed; add $test_binary and its checksum to run protocol tests." >&2
    exit 2
fi
verify_file "$test_binary"
chmod u+x -- "$test_binary"
export GOST_TEST_BINARY="$PWD/$binary"
"$test_binary" -test.v -test.timeout=120s
