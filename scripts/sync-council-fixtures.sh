#!/bin/sh
# Copy the council fixtures the platform owns into this repo.
#
#   sh scripts/sync-council-fixtures.sh <backstage-checkout>
#
# The fixtures are written by the Backstage backend's council-templates tests, in
# plugins/teknoir-device-backend/council-templates/fixtures/ of the Backstage monorepo. They are the
# cross-repo contract: the snapshots say what the platform sends in WorkTurn.Council, and the
# council must run the Standard roster from one exactly as it does with none.
#
# legacy_standard.json is the copy the daemon embeds for a hub that sends no council.
set -eu

usage() {
	echo "usage: $0 <backstage-checkout>" >&2
	exit 2
}

[ "$#" -eq 1 ] || usage

dir=$1
src="$dir/plugins/teknoir-device-backend/council-templates/fixtures"
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
testdata="$root/pkg/council/testdata"

files="standard-snapshot-none.json standard-snapshot-read.json standard-snapshot-creator.json tool-groups.json text-vectors.json"

missing=0
for f in $files; do
	if [ ! -f "$src/$f" ]; then
		echo "$0: $src/$f is missing" >&2
		missing=1
	fi
done
[ "$missing" -eq 0 ] || exit 1

mkdir -p "$testdata"
cp "$src/standard-snapshot-none.json" "$testdata/standard_snapshot_none.json"
cp "$src/standard-snapshot-read.json" "$testdata/standard_snapshot_read.json"
cp "$src/standard-snapshot-creator.json" "$testdata/standard_snapshot_creator.json"
cp "$src/tool-groups.json" "$testdata/tool_groups.json"
cp "$src/text-vectors.json" "$testdata/text_vectors.json"
cp "$src/standard-snapshot-creator.json" "$root/pkg/council/legacy_standard.json"

echo "copied the council fixtures from $src"
