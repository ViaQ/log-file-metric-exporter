# Defects in the fsnotify watcher

Three problems with the watcher in `pkg/logwatch` and `pkg/symnotify`, with the
tests that show them and the results those tests produced. Every number here is
the output of a test in this repository.

## 1. log_logged_bytes_total is wrong

The metric this component exists to produce does not match the bytes on disk. It
over-reports, by a lot, at ordinary log volume.

### Reproduce

```
./test/cluster/run-accuracy-test.sh
```

See `test/cluster/README.md` for prerequisites. It needs a cluster because it
compares the metric against a real `/var/log/pods` written by a real container
runtime.

### Result

OpenShift 4.22, worker node with 4 CPUs, kernel 5.14.0-687.35.1.el9_8.x86_64.
Eight generator pods, 150000 lines each, writers stopped before measuring:

```
  pod               on_disk       reported    ratio
  gen-1            14444010      100610856    6.97x
  gen-2            14443642       82797395    5.73x
  gen-3            14442998      113793629    7.88x
  gen-4            14442170      102779244    7.12x
  gen-5            14443550       46145777    3.19x
  gen-6            14443734       94460194    6.54x
  gen-7            14443918      183952421   12.74x
  gen-8            14442768      243643194   16.87x
  TOTAL           115546790      968182710    8.38x
```

115 MB of logs were reported as 968 MB. The eight generators wrote the same
number of identical lines, yet their reported totals differ by a factor of five
between them.

Expect your own numbers to differ. A second run of the same test on the same
cluster totalled 12.27x, with individual generators between 6.21x and 24.47x.
The size of the error depends on how the goroutines happen to interleave, so it
varies from run to run and from generator to generator. What does not vary is
the direction: the count is always too high, and because a counter only goes up,
it never comes back.

### Why

`Watch()` starts five goroutines that each pull events and call `Update()`:

```go
max := 5
wg := sync.WaitGroup{}
wg.Add(max)
for i := 1; i <= max; i++ {
    go w.processNextEvent(&wg)
}
```

`Update()` stats the file *before* taking the lock that protects the remembered
size:

```go
stat, err := os.Stat(path)
...
defer w.mutex.Unlock()
w.mutex.Lock()
lastSize, size := w.sizes[l], float64(stat.Size())
w.sizes[l] = size
var add float64
if size > lastSize {
    add = size - lastSize          // grew
} else if size < lastSize {
    add = size                     // truncated: count the whole file
}
counter.Add(add)
```

Two goroutines can stat the same growing file and then reach the lock in the
opposite order:

- A stats the file at 100 bytes; B stats it at 200.
- B takes the lock first: remembered 0, sees 200, adds 200.
- A takes the lock: remembered 200, sees 100. Smaller, so it is read as a
  truncation, and the whole 100 bytes is added again.

300 counted where 200 were written. Under a steady stream of writes this
misfires constantly, and how often depends on how the five goroutines interleave
— which is why identical generators drift apart. Because a counter only goes up,
the error accumulates and never corrects.

## 2. One event per write

An `IN_MODIFY` watch reports every write, so the event rate is whatever the
containers happen to log. The exporter has no say in it.

### Reproduce

```
go test -v -run TestEventVolume ./test/watchvolume/
```

### Result

60000 writes across 200 files:

```
  idle reader:  60000 events (1.00 per write), 0 overflows
  busy reader:   1943 events (0.03 per write), 0 overflows
  backlog under a busy reader: 58057 of 60000 generated events never consumed
```

Exactly one event per write. The kernel queues all 60000 whatever the reader is
doing, so a reader that cannot keep up falls behind by the difference rather
than being throttled.

## 3. The queue overflows and the backlog is discarded

That backlog has a limit. The inotify queue holds
`fs.inotify.max_queued_events` entries, 16384 by default. When it fills, the
kernel throws the backlog away and reports a single `IN_Q_OVERFLOW`. The bytes
those events represented are not recoverable: no later event mentions them.

### Reproduce

```
go test -v -run TestQueueOverflowUnderLoad ./test/watchvolume/
```

### Result

200 files, three rounds, reader stopped for four seconds per round while writers
hammer:

```
  56209100 writes -> 49301 events delivered, 3 overflows
```

Three rounds, three overflows, and 49301 events delivered out of 56 million
writes — 16384 per round, exactly the queue cap.

### Scope of this claim

The stall is deliberate and larger than anything routine. It makes the failure
reproducible in seconds instead of requiring a sustained overload, and it stands
in for a severe pause — a node under memory pressure, a long GC, a container
that has exhausted its CPU quota.

Overflow was **not** observed on the cluster run in section 1, which used eight
generators. That says something about the size of that test, not that the
failure needs an unusual workload: the queue does not care which pod an event
came from, only how many arrive.

What the 16384 entries buy is time. At an aggregate rate of R events per second
across every container on the node, the queue fills in 16384/R seconds, and that
is the longest the exporter can be away before the backlog is thrown away:

| Aggregate events/sec | Time to fill the queue |
|---|---|
| 10000 | 1.6 s |
| 25000 | 0.65 s |
| 50000 | 0.33 s |
| 100000 | 0.16 s |
| 164000 | 0.10 s |

Measured with 250 files, one pause per trial, predicting overflow when the pause
exceeds 16384/R:

```
rate/s       pause      predicted      overflows
10000        100ms      survives       0
10000        500ms      survives       0
25000        100ms      survives       0
25000        500ms      survives       0
50000        100ms      survives       0
50000        500ms      overflows      1
100000       100ms      survives       0
100000       500ms      overflows      1
```

Eight trials, eight agreements with the model.

This is why node density matters more than how noisy any single container is. A
node running 250 pods that each log a modest 400 lines a second aggregates to
100000 events a second, and the exporter then has 160 ms of slack. The CFS quota
period is 100 ms, so a CPU-limited exporter is descheduled for something close to
its entire budget as a matter of routine. Nothing about that requires a pod
behaving badly; it follows from how many pods share the node.
