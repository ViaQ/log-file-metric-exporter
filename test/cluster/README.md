# Cluster tests

`log_logged_bytes_total` is meant to report how many bytes a container has
logged. `run-accuracy-test.sh` checks that claim against the bytes actually on
disk, which needs a real `/var/log/pods` and a real container runtime, so it
cannot run in CI.

## What it does

1. Builds the exporter from your working tree and pushes it to the cluster's
   internal registry.
2. Runs it on one worker node against the node's real `/var/log/pods`.
3. Starts generator pods that each write a fixed number of lines and then stop.
4. Once the writers are finished and the exporter has settled, compares what the
   exporter reports for each generator against `stat` on that generator's log
   files.

Writers stop before the comparison, so both sides are reading the same settled
files and the numbers are directly comparable.

## Prerequisites

- `oc`, logged in with cluster-admin
- Go, to build the exporter
- A worker node you are willing to run a privileged pod on

The exporter reads `/var/log/pods`, which is root-owned and SELinux-labelled, so
its service account needs the privileged SCC. The script grants this; if your
account may not, it stops and prints the command for you to run:

```
oc adm policy add-scc-to-user privileged -z lfme -n lfme-accuracy-test
```

## Running it

```
./test/cluster/run-accuracy-test.sh
```

Knobs, all optional:

| Variable | Default | Meaning |
|---|---|---|
| `NS` | `lfme-accuracy-test` | namespace to create |
| `GENERATORS` | `8` | generator pods |
| `LINES` | `200000` | lines each generator writes |
| `SETTLE` | `30` | seconds to wait after writing stops |
| `KEEP` | `0` | set to `1` to keep the namespace for inspection |
| `GEN_CPU_REQUEST` | `25m` | CPU each generator reserves |
| `GEN_CPU_LIMIT` | `200m` | CPU each generator may burst to |

It deletes its namespace when finished unless `KEEP=1`. The generators sleep
after writing so their log files survive the comparison, so tear down with:

```
oc delete pods --all -n lfme-accuracy-test --grace-period=0 --force
oc delete ns lfme-accuracy-test
```

Generators left behind by an interrupted run hold CPU reservations and will make
the next run unschedulable. The script checks for this before deploying and
tells you what to look at if the node is too full.

## Reading the output

```
  pod               on_disk       reported    ratio
  gen-1            14444838      150317460   10.41x
```

`ratio` is what the exporter reports divided by what is on disk. **1.00x is
correct.** Above that, the exporter is counting bytes that were never written;
below it, bytes are missing.

A small shortfall (around 0.95x) is normal and self-correcting: it is the bytes
written since a file's most recent event, which the next event's `stat` picks
up. Sustained inflation is not self-correcting — a counter only goes up.

Recorded results for this commit are in `docs/watcher-defects.md`.
