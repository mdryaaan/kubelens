package simulator

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// The functions below produce the output a container writes before a given
// failure.
//
// These are the most important strings in the simulator. The whole product
// claims to explain failures from evidence, so the evidence has to be the kind
// of text a real container actually emits — a JVM writing an OutOfMemoryError
// with a stack trace, a Node process reporting a heap limit, a Go binary
// panicking with goroutine state. Plausible-looking filler would make the demo
// a lie, and would make the eval corpus measure nothing.

// healthyLines are the steady-state lines a workload writes when nothing is
// wrong. They matter because a failure log that contains only failure is not
// what anyone actually reads — the model has to find the signal among them.
func healthyLines(rng *rand.Rand, w workload, at time.Time) []string {
	stamp := func(offset int) string {
		return at.Add(-time.Duration(offset) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	}

	switch w.runtime {
	case runtimeJVM:
		return []string{
			fmt.Sprintf("%s  INFO 1 --- [main] c.a.%s.Application : Started Application in 8.42 seconds", stamp(120), w.name),
			fmt.Sprintf("%s  INFO 1 --- [http-nio-8080-exec-3] c.a.%s.HealthController : GET /healthz 200", stamp(90), w.name),
			fmt.Sprintf("%s  INFO 1 --- [scheduling-1] c.a.%s.MetricsPublisher : published 412 metrics", stamp(60), w.name),
		}
	case runtimeNode:
		return []string{
			fmt.Sprintf("%s info: %s listening on :8080", stamp(120), w.name),
			fmt.Sprintf("%s info: GET /healthz 200 1.4ms", stamp(90)),
			fmt.Sprintf("%s info: cache warm complete (%d keys)", stamp(60), 1200+rng.Intn(800)),
		}
	case runtimeGo:
		return []string{
			fmt.Sprintf(`%s level=info msg="server started" addr=:8080 service=%s`, stamp(120), w.name),
			fmt.Sprintf(`%s level=info msg="request served" path=/healthz status=200 dur=0.9ms`, stamp(90)),
			fmt.Sprintf(`%s level=info msg="worker tick" processed=%d`, stamp(60), 40+rng.Intn(60)),
		}
	default:
		return []string{
			fmt.Sprintf("%s [INFO] %s starting up (pid 1)", stamp(120), w.name),
			fmt.Sprintf("%s [INFO] connected to postgres at db.%s.svc.cluster.local:5432", stamp(90), w.namespace),
			fmt.Sprintf("%s [INFO] processed batch of %d records", stamp(60), 500+rng.Intn(500)),
		}
	}
}

// oomLines is the sequence a container writes on the way to being OOM killed.
func oomLines(rng *rand.Rand, w workload, at time.Time) []string {
	stamp := func(offset int) string {
		return at.Add(-time.Duration(offset) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	}

	lines := healthyLines(rng, w, at)

	switch w.runtime {
	case runtimeJVM:
		return append(lines,
			fmt.Sprintf("%s  WARN 1 --- [scheduling-1] c.a.%s.CacheWarmer : heap usage 91%% of %s", stamp(24), w.name, w.memLimit),
			fmt.Sprintf("%s  WARN 1 --- [scheduling-1] c.a.%s.CacheWarmer : GC overhead 71%%, 8 full collections in 30s", stamp(18), w.name),
			fmt.Sprintf("%s ERROR 1 --- [http-nio-8080-exec-7] o.a.c.c.C.[.[.[/] : Servlet threw exception", stamp(9)),
			"java.lang.OutOfMemoryError: Java heap space",
			fmt.Sprintf("\tat com.acme.%s.CacheWarmer.loadAll(CacheWarmer.java:64)", strings.ReplaceAll(w.name, "-", "")),
			fmt.Sprintf("\tat com.acme.%s.CacheWarmer.refresh(CacheWarmer.java:31)", strings.ReplaceAll(w.name, "-", "")),
			"\tat java.base/java.util.concurrent.ThreadPoolExecutor.runWorker(ThreadPoolExecutor.java:1136)",
		)
	case runtimeNode:
		return append(lines,
			fmt.Sprintf("%s warn: rss 243MB approaching container limit %s", stamp(20), w.memLimit),
			"<--- Last few GCs --->",
			fmt.Sprintf("[1:0x%x]    24913 ms: Mark-sweep 243.2 (256.0) -> 242.8 (256.0) MB, 412.9 / 0.0 ms", rng.Intn(0xffffff)),
			"FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory",
			" 1: 0xb7a3f0 node::Abort() [node]",
			" 2: 0xa8b7f5 node::OOMErrorHandler(char const*, v8::OOMDetails const&) [node]",
		)
	case runtimeGo:
		return append(lines,
			fmt.Sprintf(`%s level=warn msg="memory pressure" heap_alloc_mb=%d limit=%s`, stamp(20), 480+rng.Intn(30), w.memLimit),
			"fatal error: runtime: out of memory",
			"",
			fmt.Sprintf("runtime stack:\nruntime.throw({0x8f2a11?, 0x%x?})", rng.Intn(0xffffff)),
			"\t/usr/local/go/src/runtime/panic.go:1023 +0x5c",
			"runtime.sysMapOS(0xc000000000, 0x4000000)",
		)
	default:
		return append(lines,
			fmt.Sprintf("%s [WARNING] resident set size %dMB of %s limit", stamp(20), 980+rng.Intn(40), w.memLimit),
			"Traceback (most recent call last):",
			fmt.Sprintf(`  File "/app/%s/indexer.py", line 212, in build_index`, strings.ReplaceAll(w.name, "-", "_")),
			"    frame = pandas.concat(chunks, ignore_index=True)",
			"MemoryError: Unable to allocate 412. MiB for an array with shape (54000000,) and data type float64",
		)
	}
}

// crashLines is the sequence a container writes before exiting non-zero.
func crashLines(rng *rand.Rand, w workload, at time.Time) []string {
	lines := healthyLines(rng, w, at)

	switch w.runtime {
	case runtimeJVM:
		return append(lines,
			"Caused by: org.postgresql.util.PSQLException: FATAL: password authentication failed for user \"app\"",
			"\tat org.postgresql.core.v3.ConnectionFactoryImpl.doAuthentication(ConnectionFactoryImpl.java:693)",
			"\tat com.acme.config.DataSourceConfig.dataSource(DataSourceConfig.java:47)",
			"Error starting ApplicationContext. To display the condition evaluation report re-run with 'debug' enabled.",
		)
	case runtimeNode:
		return append(lines,
			"/app/node_modules/pg/lib/client.js:285",
			"        throw new Error('Connection terminated unexpectedly')",
			"        ^",
			"Error: Connection terminated unexpectedly",
			"    at Connection.<anonymous> (/app/node_modules/pg/lib/client.js:285:73)",
			"    at Object.onceWrapper (node:events:632:26)",
			"Node.js v20.11.0",
		)
	case runtimeGo:
		return append(lines,
			"panic: runtime error: invalid memory address or nil pointer dereference",
			fmt.Sprintf("[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x%x]", rng.Intn(0xffffff)),
			"",
			"goroutine 1 [running]:",
			fmt.Sprintf("main.(*%sServer).Start(0x0)", strings.ReplaceAll(w.name, "-", "")),
			fmt.Sprintf("\t/build/cmd/%s/main.go:84 +0x1c", w.name),
		)
	default:
		return append(lines,
			"Traceback (most recent call last):",
			fmt.Sprintf(`  File "/app/%s/__main__.py", line 41, in <module>`, strings.ReplaceAll(w.name, "-", "_")),
			"    main()",
			`  File "/app/config.py", line 18, in load`,
			`    raise KeyError(f"missing required setting: {name}")`,
			"KeyError: 'missing required setting: DATABASE_URL'",
		)
	}
}

// imagePullLines is what a pod shows when its image never arrived — almost
// nothing, because the container never started.
func imagePullLines(_ *rand.Rand, _ workload, _ time.Time) []string {
	return nil
}

// probeLines is the output of a container that is up but answering slowly.
func probeLines(rng *rand.Rand, w workload, at time.Time) []string {
	stamp := func(offset int) string {
		return at.Add(-time.Duration(offset) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	}

	lines := healthyLines(rng, w, at)
	return append(lines,
		fmt.Sprintf(`%s level=warn msg="upstream slow" dep=postgres p99_ms=%d`, stamp(45), 1400+rng.Intn(600)),
		fmt.Sprintf(`%s level=warn msg="healthz exceeded budget" dur_ms=%d budget_ms=1000`, stamp(30), 1100+rng.Intn(900)),
		fmt.Sprintf(`%s level=error msg="connection pool exhausted" in_use=%d max=%d`, stamp(15), 20, 20),
		fmt.Sprintf(`%s level=warn msg="healthz exceeded budget" dur_ms=%d budget_ms=1000`, stamp(5), 1600+rng.Intn(900)),
	)
}

// pendingLines is what a pod that never scheduled shows — nothing at all.
func pendingLines(_ *rand.Rand, _ workload, _ time.Time) []string {
	return nil
}
