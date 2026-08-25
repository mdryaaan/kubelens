# Contributing to kubelens

Thanks for taking a look. This covers what a change is expected to include, and
— more usefully — *why*, so you can tell when a rule here does not apply to what
you are doing.

## Getting set up

```bash
git clone https://github.com/mdryaaan/kubelens.git
cd kubelens

make demo                       # the whole product, no cluster needed
cd web && npm install && npm run dev

make test                       # go test ./... -race with coverage
make eval                       # score the labelled corpus
make ci                         # everything the pipeline runs
```

Go 1.22+ and Node 18+. Nothing else: SQLite is pure Go, so there is no cgo and
no C toolchain. `golangci-lint` is used if installed and skipped politely if not.

## The two rules that matter

**1. A claim must be backed by evidence that exists.**

Everything in `internal/explanation` follows from this. An explanation that
quotes a log line reads as authoritative, so a fabricated quote does not merely
fail to help — it manufactures confidence in an analysis nothing supports. If
you are changing citation handling, hold these lines:

- Verification is literal. No case folding, no whitespace normalisation. A check
  that is lenient about the characters proves nothing about the content.
- Rejected quotes are kept and displayed, never silently dropped. A tool that
  hides fabrications teaches people to trust it more than they should.
- An explanation that cited nothing is labelled unsupported rather than being
  rendered with the same authority as one that quotes the log.

**2. Detection must work with no model available.**

The detector reads structured Kubernetes fields and is the product's floor. If
a change makes detection depend on `internal/llm`, it is the wrong change —
the failure mode of an unreachable model has to be a degraded product, not a
broken one.

## Adding a detection rule

Five things. Skipping any will come up in review:

1. **The rule** in `internal/detector/`, implementing `Rule`. It must read an
   actual Kubernetes field — a container status, an event reason, a condition —
   not grep the logs. Logs are evidence for the *explanation*; they are not how
   detection decides.

2. **A severity you can defend.** `Critical` means someone should look now.
   Marking everything critical is the same as marking nothing.

3. **Table-driven tests** including the cases the rule must **not** fire on. A
   test file with only positive cases has not tested anything interesting. The
   two most valuable tests in `detector_test.go` are both negative: a crash loop
   whose restart count has stopped rising, and a probe that failed once.

4. **A simulator injector** in `internal/simulator/incidentgen.go`, so the rule
   appears in `--demo`. The injector must edit the real pod or deployment object
   rather than fabricate a detection — `TestInjectedFailuresAreDetectedByTheRealRules`
   asserts that the real rule picks it up, which is the coupling that keeps demo
   mode honest.

5. **Corpus cases** in `testdata/eval/` — at least five, with realistic log
   excerpts. Then run `make eval`.

## Writing simulator log content

The generated logs are the most important strings in the simulator. The product
claims to explain failures from evidence, so the evidence has to be what a real
container actually emits: a JVM writing `OutOfMemoryError` with a stack trace, a
Node process reporting a heap limit, a Go binary panicking with goroutine state.

Plausible-looking filler would make the demo a lie and the eval corpus
meaningless. If you are unsure what a runtime actually prints, go and look at a
real one.

Also: some failures produce **no** output. A container whose image never pulled
has never written a line. Those cases must clear the log rather than leave stale
lines in place — `TestFailuresWithNoOutputClearTheLog` covers it.

## Changing the eval corpus

Run `make eval` before and after. If the score moves, one of two things is true
and the pull request should say which:

- **The corpus was wrong.** Fix the label and explain why the old one was wrong.
- **The provider changed behaviour.** Add the case that motivated it.

Do not tune the offline baseline to fix a corpus miss you just discovered. The
one it currently gets wrong — `case-014`, an `InvalidImageName` whose message
contains no pull-related words — is left in deliberately: it is a real gap in
keyword matching, and papering over it would remove the thing the harness exists
to measure.

## The offline provider is not a model

`internal/llm/offline.go` applies hand-written templates. It exists so the whole
pipeline runs with no daemon and no API key, and so a real model has a control
to be measured against.

**Anything derived from it must carry `OfflineDisclaimer`.** That is enforced in
the API response, the eval output, the stored record, and the dashboard, and
there are tests for each. If you add an output surface, add the disclaimer test
with it.

## Frontend

- `web/lib/types.ts` mirrors the Go payloads by hand. Keep them in step; the
  surface is small enough that a generator would be one more thing to run.
- `CitedLogViewer` is the component the product exists to make possible. Changes
  to it should make the link between a claim and its evidence *more* obvious,
  never less.
- Loading and error states are not optional. The most likely state on first run
  is "the Go server is not started", and the dashboard should say so plainly
  rather than showing an empty chart.
- `npm run typecheck` and `npm run build` both have to pass. `strict` and
  `noUncheckedIndexedAccess` are on deliberately.

## Style

- Run `make fmt` before pushing.
- Comments explain **why**, not what. `// increment i` is noise; `// A pod sits
  in CrashLoopBackOff long after the cause is fixed, so...` is the reason the
  code exists.
- Errors say what to do next. `"is ollama serve running?"` beats
  `"connection refused"`.
- Exported identifiers get doc comments.

## Commit messages

Conventional commits, present tense:

```
feat(detector): add statefulset rollout rule
fix(simulator): clear logs for containers that never ran
test(explanation): cover citations quoting an event message
docs: explain why ollama is the default provider
```

## Reporting a security issue

For a vulnerability **in kubelens itself**, open a private security advisory
rather than a public issue. Findings about workloads in your own cluster are
what the tool is for — those belong to you.

## License

By contributing you agree that your contributions are licensed under the MIT
License, the same as the rest of the project.
