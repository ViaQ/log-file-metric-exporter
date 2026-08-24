#!/usr/bin/env bash
#
# Measures whether log_logged_bytes_total matches the bytes actually on disk.
#
# Builds the exporter from the current working tree, runs it on one node against
# the real /var/log/pods, has a set of generator pods each write a fixed number
# of lines and then stop, and once everything is quiet compares what the exporter
# reports against what stat says. Writers stop before the comparison so both
# sides are reading the same settled files.
#
# See README.md for prerequisites and expected output.

set -euo pipefail

NS="${NS:-lfme-accuracy-test}"
GENERATORS="${GENERATORS:-8}"
LINES="${LINES:-200000}"
SETTLE="${SETTLE:-30}"
KEEP="${KEEP:-0}"

# Each generator asks for little and may burst; it is a shell loop, not a
# workload worth reserving a node for.
GEN_CPU_REQUEST="${GEN_CPU_REQUEST:-25m}"
GEN_CPU_LIMIT="${GEN_CPU_LIMIT:-200m}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
workdir="$(mktemp -d)"

cleanup() {
  [ -n "${pf:-}" ] && kill "$pf" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

say() { printf '\n== %s\n' "$*"; }
die() { printf '\nERROR: %s\n' "$*" >&2; exit 1; }

say "checking prerequisites"
command -v oc >/dev/null || die "oc not found"
oc whoami >/dev/null || die "not logged in to a cluster"
node="$(oc get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[0].metadata.name}')"
[ -n "$node" ] || die "no worker node found"
echo "cluster: $(oc whoami --show-server)"
echo "node:    $node"
echo "kernel:  $(oc get node "$node" -o jsonpath='{.status.nodeInfo.kernelVersion}')"

# A previous run that was interrupted can leave generators holding CPU requests,
# which would make this run unschedulable for reasons that look unrelated.
say "checking the node can fit this run"
need_m=$(( GENERATORS * ${GEN_CPU_REQUEST%m} + 100 ))
free_m=$(oc get node "$node" -o json | python3 -c '
import json,sys
n=json.load(sys.stdin)
def m(v): return int(v[:-1]) if v.endswith("m") else int(float(v)*1000)
print(m(n["status"]["allocatable"]["cpu"]))
')
used_m=$(oc get pods -A --field-selector "spec.nodeName=$node" -o json | python3 -c '
import json,sys
d=json.load(sys.stdin); t=0
def m(v): return int(v[:-1]) if v.endswith("m") else int(float(v)*1000)
for p in d["items"]:
    if p["status"].get("phase") not in ("Running","Pending"): continue
    for c in p["spec"]["containers"]:
        t += m(c.get("resources",{}).get("requests",{}).get("cpu","0"))
print(t)
')
avail_m=$(( free_m - used_m ))
echo "allocatable ${free_m}m, requested ${used_m}m, available ${avail_m}m, this run needs ~${need_m}m"
if [ "$avail_m" -lt "$need_m" ]; then
  echo "Pods left over from an interrupted run are the usual cause. Check with:"
  echo "  oc get pods -A --field-selector spec.nodeName=$node | grep -i lfme"
  die "not enough allocatable CPU on $node"
fi

say "building the exporter from $repo"
CGO_ENABLED=0 GOOS=linux go build -C "$repo" -o "$workdir/lfme" ./cmd
cp "$repo/cmd/testdata/server.crt" "$repo/cmd/testdata/server.key" "$workdir/"
cat > "$workdir/Dockerfile" <<'EOF'
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY lfme /usr/local/bin/lfme
COPY server.crt /etc/certs/tls.crt
COPY server.key /etc/certs/tls.key
RUN chmod +x /usr/local/bin/lfme
EOF

say "creating namespace $NS"
oc get namespace "$NS" >/dev/null 2>&1 && die "namespace $NS already exists; delete it first"
oc create namespace "$NS" >/dev/null
oc create sa lfme -n "$NS" >/dev/null
# The exporter reads /var/log/pods, which is root-owned and SELinux-labelled.
if ! oc adm policy add-scc-to-user privileged -z lfme -n "$NS" >/dev/null 2>&1; then
  echo "Could not grant the privileged SCC. Run this yourself and re-run:"
  echo "  oc adm policy add-scc-to-user privileged -z lfme -n $NS"
  die "missing privileged SCC for serviceaccount lfme"
fi

say "building image in-cluster"
oc new-build --binary --name=lfme --strategy=docker -n "$NS" >/dev/null
oc start-build lfme --from-dir="$workdir" --follow -n "$NS" >/dev/null

image="image-registry.openshift-image-registry.svc:5000/$NS/lfme:latest"

say "deploying exporter and $GENERATORS generators ($LINES lines each)"
{
cat <<EOF
apiVersion: v1
kind: Pod
metadata: {name: exporter, namespace: $NS}
spec:
  serviceAccountName: lfme
  nodeName: $node
  restartPolicy: Never
  containers:
  - name: exporter
    image: $image
    imagePullPolicy: Always
    command: ["/usr/local/bin/lfme"]
    args: ["-dir=/var/log/pods","-crtFile=/etc/certs/tls.crt","-keyFile=/etc/certs/tls.key","-verbosity=0"]
    securityContext:
      privileged: true
      seLinuxOptions: {type: spc_t}
    volumeMounts:
    - {name: varlogpods, mountPath: /var/log/pods, readOnly: true}
  volumes:
  - {name: varlogpods, hostPath: {path: /var/log/pods}}
EOF
for i in $(seq 1 "$GENERATORS"); do
# The trailing sleep keeps the log files in place for the comparison, but is
# bounded so an interrupted run cannot leave pods holding the node's CPU.
cat <<EOF
---
apiVersion: v1
kind: Pod
metadata: {name: gen-$i, namespace: $NS, labels: {app: loggen}}
spec:
  nodeName: $node
  restartPolicy: Never
  containers:
  - name: gen
    image: registry.access.redhat.com/ubi9/ubi-minimal:latest
    command: ["/bin/sh","-c"]
    args: ["i=0; while [ \$i -lt $LINES ]; do echo \"\$i log line from gen-$i with a realistic payload\"; i=\$((i+1)); done; echo GENERATOR_DONE; sleep 1800"]
    resources:
      requests: {cpu: $GEN_CPU_REQUEST, memory: 32Mi}
      limits: {cpu: $GEN_CPU_LIMIT, memory: 64Mi}
EOF
done
} > "$workdir/run.yaml"
oc apply -f "$workdir/run.yaml" >/dev/null
oc wait --for=condition=Ready pod/exporter -n "$NS" --timeout=180s >/dev/null

say "waiting for all $GENERATORS generators to start"
for _ in $(seq 1 30); do
  running=$(oc get pods -n "$NS" -l app=loggen \
    --field-selector status.phase=Running -o name 2>/dev/null | wc -l)
  [ "$running" -ge "$GENERATORS" ] && break
  sleep 5
done
running=$(oc get pods -n "$NS" -l app=loggen --field-selector status.phase=Running -o name 2>/dev/null | wc -l)
if [ "$running" -lt "$GENERATORS" ]; then
  oc get pods -n "$NS" -l app=loggen
  die "only $running of $GENERATORS generators are running; results would not be comparable"
fi

say "waiting for generators to finish writing"
finished=0
for _ in $(seq 1 60); do
  finished=$(for p in $(oc get pods -n "$NS" -l app=loggen -o name); do
      oc logs "${p#pod/}" -n "$NS" --tail=1 2>/dev/null
    done | grep -c GENERATOR_DONE || true)
  echo "  finished: $finished/$GENERATORS"
  [ "$finished" -ge "$GENERATORS" ] && break
  sleep 10
done
[ "$finished" -ge "$GENERATORS" ] || die "generators did not finish writing in time"

say "letting the exporter settle for ${SETTLE}s (writers have stopped)"
sleep "$SETTLE"

say "collecting"
oc port-forward pod/exporter -n "$NS" 19191:2112 >/dev/null 2>&1 &
pf=$!
sleep 8
curl -sk --max-time 30 https://localhost:19191/metrics > "$workdir/metrics.txt"

# Bytes on disk, per generator, straight from the node. Scoped to this
# namespace: /var/log/pods holds every pod on the node, and another run's
# generators would otherwise be counted as ours.
oc exec exporter -n "$NS" -- /bin/sh -c "
for d in /var/log/pods/${NS}_gen-*_*/; do
  [ -d \"\$d\" ] || continue
  name=\$(basename \"\$d\" | sed 's/^[^_]*_//; s/_[^_]*\$//')
  total=\$(find \"\$d\" -name '*.log*' -exec stat -c %s {} \; 2>/dev/null | awk '{s+=\$1} END {print s+0}')
  echo \"\$name \$total\"
done" > "$workdir/disk.txt" 2>/dev/null

say "RESULTS"
printf '  %-10s %14s %14s %8s\n' pod on_disk reported ratio
overall_disk=0; overall_reported=0
while read -r pod disk; do
  [ -z "$pod" ] && continue
  reported=$(grep '^log_logged_bytes_total' "$workdir/metrics.txt" \
    | grep "namespace=\"$NS\"" | grep "podname=\"$pod\"" \
    | head -1 | awk '{printf "%.0f", $2}')
  reported="${reported:-0}"
  ratio=$(awk -v r="$reported" -v d="$disk" 'BEGIN{ if (d>0) printf "%.2fx", r/d; else print "n/a" }')
  printf '  %-10s %14s %14s %8s\n' "$pod" "$disk" "$reported" "$ratio"
  overall_disk=$((overall_disk + disk))
  overall_reported=$((overall_reported + reported))
done < <(sort "$workdir/disk.txt")
printf '  %-10s %14s %14s %8s\n' TOTAL "$overall_disk" "$overall_reported" \
  "$(awk -v r="$overall_reported" -v d="$overall_disk" 'BEGIN{ if (d>0) printf "%.2fx", r/d; else print "n/a" }')"

say "queue overflow reported by the exporter"
oc logs exporter -n "$NS" 2>/dev/null | grep -ci 'queue overflow' | xargs echo "  overflow events:"

echo
echo "A ratio of 1.00x means the exporter agrees with the bytes on disk."
echo "Anything above that is the exporter counting bytes that were never written."

if [ "$KEEP" = "1" ]; then
  echo
  echo "Namespace $NS kept. Delete it with:"
  echo "  oc delete pods --all -n $NS --grace-period=0 --force; oc delete ns $NS"
else
  say "cleaning up"
  # The generators are sleeping, so remove them without waiting out the grace
  # period; leaving them behind would block the next run's scheduling.
  oc delete pods --all -n "$NS" --grace-period=0 --force >/dev/null 2>&1 || true
  oc delete namespace "$NS" >/dev/null 2>&1 || true
fi
