#!/usr/bin/env bash
# Generates protobuf code from proto/pandaeye/v1 (the single source of truth for both languages).
#
#   scripts/gen-proto.sh          Go only (committed to cloud/internal/wire/pandaeyev1)
#   scripts/gen-proto.sh java     Java lite only (into plugin/protocol/build/generated/proto, for
#                                 checks outside Gradle; the Gradle build runs protoc itself)
#   scripts/gen-proto.sh all      both
#
# Requires protoc (libprotoc 36.x) and, for Go, protoc-gen-go matching google.golang.org/protobuf in
# cloud/go.mod.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${1:-go}"
protos=("$root"/proto/pandaeye/v1/*.proto)

gen_go() {
	local out="$root/cloud/internal/wire"
	rm -f "$out"/pandaeyev1/*.pb.go
	mkdir -p "$out/pandaeyev1"
	protoc -I "$root/proto" \
		--go_out="$out" \
		--go_opt=module=github.com/jackh54/PandaEye/cloud/internal/wire \
		"${protos[@]}"
	echo "generated Go into ${out#"$root"/}/pandaeyev1"
}

gen_java() {
	local out="$root/plugin/protocol/build/generated/proto"
	mkdir -p "$out"
	protoc -I "$root/proto" --java_out=lite:"$out" "${protos[@]}"
	echo "generated Java lite into ${out#"$root"/}"
}

case "$target" in
go) gen_go ;;
java) gen_java ;;
all) gen_go; gen_java ;;
*) echo "usage: $0 [go|java|all]" >&2; exit 2 ;;
esac
