#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
go_binary="${GO:-go}"
output="${1:-dist}"
mkdir -p -- "$output/tests"
output="$(cd -- "$output" && pwd)"
export CGO_ENABLED=0
for arch in amd64 arm64; do
    GOOS=linux GOARCH="$arch" "$go_binary" build -mod=readonly -trimpath -ldflags '-s -w' -o "$output/gost-$arch" .
    (
        cd x
        GOOS=linux GOARCH="$arch" "$go_binary" test -mod=readonly -trimpath -c -o "$output/tests/wg-tests-$arch" ./dialer/wg
        GOOS=linux GOARCH="$arch" "$go_binary" vet -mod=readonly ./dialer/wg ./connector/wg ./config/cmd ./chain
    )
done
cp -- verify-linux.sh "$output/verify-linux.sh"
(
    cd -- "$output"
    sha256sum gost-amd64 gost-arm64 tests/wg-tests-amd64 tests/wg-tests-arm64 verify-linux.sh > SHA256SUMS
)
echo "Linux builds and compiled tests are in $output. Run bash $output/verify-linux.sh on Linux to execute the tests."
