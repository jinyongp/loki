#!/bin/sh
set -eu
test "$#" -eq 2 || { echo "usage: verify-derived-image.sh BASE_REFERENCE DERIVED_IMAGE" >&2; exit 2; }
base=$1
derived=$2
docker_cmd=${LOKI_DOCKER:-docker}
inspect(){ "$docker_cmd" image inspect --format "$1" "$2"; }
test "$(inspect '{{ index .Config.Labels "org.opencontainers.image.title" }}' "$base")" = Loki || { echo "base is not Loki" >&2; exit 1; }
base_id=$(inspect '{{.Id}}' "$base")
case $base_id in sha256:*) ;; *) echo "base image ID is invalid" >&2; exit 1;; esac
test "$(inspect '{{ index .Config.Labels "io.loki.derived.base" }}' "$derived")" = "$base_id" || { echo "derived base label mismatch" >&2; exit 1; }
for format in '{{json .Config.Entrypoint}}' '{{json .Config.Cmd}}' '{{json .Config.User}}' '{{json .Config.WorkingDir}}' '{{json .Config.Volumes}}' '{{.Os}}' '{{.Architecture}}'; do
  test "$(inspect "$format" "$base")" = "$(inspect "$format" "$derived")" || { echo "derived image changed config: $format" >&2; exit 1; }
done
for label in org.opencontainers.image.title org.opencontainers.image.revision io.loki.devtools.version io.loki.ripgrep.version; do
  test "$(inspect "{{ index .Config.Labels \"$label\" }}" "$base")" = "$(inspect "{{ index .Config.Labels \"$label\" }}" "$derived")" || { echo "derived image changed label: $label" >&2; exit 1; }
done
files="/opt/loki/bin/loki /opt/loki/bin/devtools /opt/loki/libexec/devtools /usr/bin/rg /usr/share/doc/loki/devtools-catalog.json /usr/share/doc/loki/provenance.json"
base_hashes=$("$docker_cmd" run --rm --network none --entrypoint sha256sum "$base" $files)
derived_hashes=$("$docker_cmd" run --rm --network none --entrypoint sha256sum "$derived" $files)
test "$base_hashes" = "$derived_hashes" || { echo "derived image replaced Loki-managed files" >&2; exit 1; }
"$docker_cmd" run --rm --network none --entrypoint /bin/sh "$derived" -ec '
  test "$(stat -c "%u:%g:%a" /workspace)" = 10000:10001:2770
  grep -q "^runner:x:10000:10000:" /etc/passwd
  grep -q "^workspace:x:10001:" /etc/group
  grep -q "^egress:x:10002:10002:" /etc/passwd
  /opt/loki/bin/loki version >/dev/null
  /opt/loki/bin/devtools version >/dev/null
  /opt/loki/bin/devtools schema version >/dev/null
'
printf 'derived image contract: passed\n'
