# Round 2 report, 2026-10-11

Branch `round2/kiln`, created from `enhance/evidence-depth` (not from `main`). Nothing pushed, nothing on `main`, nothing deleted, nothing outside the repository changed; Docker Desktop was started on this machine and left running. Every number below comes from a command in this document and in `README.md`; the ones from runs that could not finish are labelled with how far they got.

## 1. Audit delta since round 1

| Round 1 weak spot | What round 2 did | Status |
|---|---|---|
| The trace replay was a model of the scoring, not the scheduler | The same trace through `kube-scheduler` itself, both profiles, on envtest; model and binary compared row by row | Done for the empty cluster; the full cluster is bounded by what the binary can do in an hour, see 4.2 |
| Canary results came from one traffic model and the alternatives were tuned in sample | Twelve new traffic classes, two-fold out-of-sample tuning, alpha sweep, intervals, a plot, and a generated list of where the sequential test loses | Done; three honest losses found |
| Phase 8's proof had never passed in CI | Docker is available here, but the local cluster cannot run the platform: see 4.1 | Still unverified; the reason is now measured, not guessed |
| Controller scalability was unmeasured | A scale benchmark under envtest at production defaults, the bottleneck found and one fix made | Done for 500 and 1,000 rollouts before and after the fix; 2,000 and 5,000 were still running when this was written |
| "Six subsystems behind one fail-closed gate", "zero SLA violations" | README and the target line now say three gated kinds and "latency-class placements on preemptible capacity" | Done |

What the audit found new this round:

- **The local kind cluster is unusable, and the cause is the machine, not the platform.** The WSL2 VM is capped at 4 GB by `~/.wslconfig` (the user's own setting, outside the repo). With the five-node cluster and the whole platform in it the VM sat at a load average of 171 with 111 MB free; the API server answered every request with a TLS handshake timeout for the whole session, controllers lost leader election and were killed by liveness probes in a loop, and ArgoCD could not sync. The cluster's kiln images also date from 2026-09-07, before the Phase 8 push.
- **envtest works on Windows.** The binaries were already in `scheduler-plugin/bin/k8s/1.36.0-windows-amd64` and `chaos/bin/k8s`, so round 1's "no envtest here" was wrong; everything in this round that needed a control plane ran on them.
- **Two of round 1's own harness assumptions did not survive scale.** The Windows cleanup of an envtest control plane killed every `kube-apiserver.exe` on the machine, and a second test running alongside lost its API server; and etcd on Windows stalled for ten seconds on the API server's periodic count of 111,443 scheduler Events. Both are fixed in `377c9cf`.

## 2. Ranked options

Effort is what each took here. Keywords are the general terms a host searching by concept would match.

| # | Option | Hours | Keywords earned | Done? |
|---|---|---|---|---|
| 1 | The real scheduler on the public trace, compared with the model | 3.5 | cluster scheduling, scheduler framework, trace-driven simulation, simulation validation, bin packing, preemptible capacity | Yes (empty cluster whole; full cluster bounded) |
| 2 | Held-out canary evaluation on unseen traffic shapes with intervals and a plot | 3 | sequential hypothesis testing, out-of-sample evaluation, false-positive control, time to detect, correlated failures, heavy-tailed latency | Yes |
| 3 | Controller scale benchmark, bottleneck found, one fix | 3 | controller scalability, reconcile latency, API server load, client-side throttling, concurrency | Yes: bottleneck measured, fix made and measured |
| 4 | Phase 8 proof on the local cluster, nightly alert exercised | 1 (diagnosis) | CI reliability, capacity limits | No: measured why not |
| 5 | GKE window, Terraform with verified teardown and a cost estimate | 1.5 | infrastructure as code, managed Kubernetes, cost estimation, teardown verification | Written and validated, not applied (needs approval) |

Order of value: 1 and 2 turn the two headline claims from "my model says" into "the binary says" and "unseen traffic says, with intervals"; 3 answers the first question a host asks about a controller; 5 is the next step and is ready to approve.

## 3. Results

### 3.1 Phase 8 on the local cluster, and the nightly alert

**Before:** Phase 8's end-to-end proof had failed on every nightly since 2026-09-12 and the harness fix from round 1 sat unverified. **After:** still unverified, now with the reason measured. Docker Desktop started and the 35-day-old `kiln-dev` cluster came back, but its control plane never answered:

```text
$ kubectl --context kind-kiln-dev get --raw /readyz     # 10.4 s, every time, all session
Unable to connect to the server: net/http: TLS handshake timeout
$ docker exec kiln-dev-control-plane sh -c 'uptime; free -m'
 05:06:18 up 1:42, load average: 171.55, 170.50, 155.39
Mem: total 3918  used 3498  free 111
```

The whole platform (ArgoCD, Crossplane, Kyverno, Istio, Kafka, Postgres, Prometheus, Jaeger and five kiln controllers on five nodes) does not fit the 4 GB the WSL VM is allowed, and raising that cap is a change outside the repository. The images in the cluster predate Phase 8, so a run would also need five image builds inside the same VM. The `nightly-alert` job is `schedule`-only; it cannot be exercised without a run on `main`, and `act` is not installed. The README's Status paragraph already says the proof is pending; it stays true.

Reproduce the diagnosis: the two commands above, with Docker Desktop running.

Keywords: capacity limits, CI reliability, incident diagnosis.

Story: a proof that needs the whole system is only as available as the smallest environment that can hold the system, and the honest move when the environment cannot is to measure why and say so rather than report a flaky pass. At Google scale this is why integration proofs run in a fleet, not on a laptop, and why the fleet's capacity is itself monitored. The branch carries the fix; the first run on `main` is the verification.

### 3.2 The scheduler binary on the Google trace

**Before:** the public-trace replay was the plugin's scoring function in a loop, with a model of the default profile written by the same hand. **After:** `TestGoogleTraceRealScheduler` runs the same 12,477 machines, 57,012 placements, synthetic economics and billing ledger through `kube-scheduler` itself on an envtest API server, with the default profile and the kiln profile exactly as `config/scheduler-config.yaml` declares it. Trace time is driven a second at a time; every departure is deleted and awaited, every arrival waits for the scheduler's verdict, and only the node choice changes hands.

Empty cluster, spot fraction 0.5, verbatim from `KILN_TRACE_REAL_SCENARIOS=empty make -C scheduler-plugin trace-real`:

| Placement | Billed $, model | Billed $, binary | vs default, model | vs default, binary | Latency-class on spot, model | binary | Machines touched, model | binary | Mean request util, model | binary |
|---|---|---|---|---|---|---|---|---|---|---|
| default profile | 723.06 | 713.29 | | | 249 | 257 | 12403 | 12466 | 5.9% | 5.6% |
| kiln profile | 59.56 | 65.51 | -91.8% | -90.8% | 0 | 0 | 2104 | 2253 | 43.5% | 40.0% |

Where they diverge: the binary packs a little looser (149 more machines, 40% against 43.5% utilisation, $5.95 more) because the real scheduler samples 623 feasible nodes from a cursor that advances by nodes scanned in parallel and breaks ties at random; the saving moves by one point. The class guarantee is identical: 0 against 257. No task was rejected. The binary places 257 to 341 tasks per second end to end through one API server on this laptop, which is the driver's cost, not the scheduler's ceiling.

Full cluster: did not finish, and the reason is the finding. With the trace cut to its first 40 minutes (29,906 arrivals, 14,137 of which the model leaves unplaced on the full cluster, placing the rest in 36 s), the binary offered 3,785 arrivals in 2m15s, collected 1,364 rejections, and was still inside the first retry batch of those 1,364 forty minutes later, its scheduler at full CPU. On a full 12,477-node cluster each rejected pod costs the real scheduler a filter pass over every node; a retry batch of a thousand is minutes of work, and the model's few hundred placements per second "when nothing fits" is the cost of its own loop. The full-cluster rows in `METRICS.md` therefore remain the model's only.

Reproduce:

```bash
bash hack/google-trace.sh
KILN_TRACE_REAL_SCENARIOS=empty make -C scheduler-plugin trace-real        # about 7 minutes
KILN_TRACE_WINDOW=40 KILN_TRACE_REAL_SCENARIOS=warm make -C scheduler-plugin trace-real
KILN_TRACE_WINDOW=40 make -C scheduler-plugin trace                         # the model on the same window
```

Keywords: cluster scheduling, scheduler framework, trace-driven simulation, simulation validation, bin packing, preemptible capacity.

Story: a simulator is only evidence once it has been checked against the thing it simulates, and the check here is the scheduler binary on the same workload, which agreed with the model on the bill to within a few percent and on the class guarantee exactly. The disagreement it did find, on a full cluster, is the real scheduler's cost of saying no, which the model had made free. That is the same discipline Google applies to Borg's simulators: trust them where they have been validated against the real scheduler, and name where they have not.

### 3.3 Held-out canary evaluation

**Before:** one traffic family, alternatives tuned on the classes they were scored on, no intervals. **After:** 21 classes, twelve new (diurnal load that tracks errors, retry storms with retry volume, log-normal latency tails, 20 rps and 5 rps), a fixed stratified two-fold split with each alternative tuned on one half and scored on the other both ways, the sequential test untuned and also swept over alpha, Wilson and bootstrap intervals, a generated list of losses, and a plot. All of it is in `docs/METRICS.md` under "Held-out evaluation"; the plot is `docs/assets/detector-tradeoff.svg`.

Where the sequential test loses, from the generated list:

| Class | Sequential test (ADR-0014 defaults) | Best alternative, tuned out of sample | What it is |
|---|---|---|---|
| very low traffic, 5 rps, healthy | 99.3% false rollback, all `Inconclusive` | threshold 0.0%, burn rate 1.5% | the 500-request sample gate takes 33 minutes at 5 rps, past the 30-minute step deadline: a spec mismatch the controller does not catch |
| retry storms, rare bursts at 5x with 1.5x volume, healthy | 7.5% false rollback (6.0 to 9.3), over its 5% alpha | threshold 0.1%, burn rate 2.9% | correlated bursts break the independence the likelihood ratio assumes |
| heavy-tailed latency, mean 0.5% to 2% | misses 27.9%, over its 10% beta | burn rate misses 42.4%, threshold 97% | everyone loses; the sequential test least |
| diurnal, 2x-times-load step from the trough | p50 5 min | burn rate 3 min, missing 15.6% | 15 requests per window in the trough; the sample gate waits |
| step to 30% | p50 30 s | burn rate 15 s | one window against three, as in round 1 |

What it keeps: zero false rollbacks on every diurnal, clean, half-limit, spike and 20 rps healthy class; no misses on any round-1 regression; and the alternatives, tuned out of sample, miss 16 to 97% of the new regressions where in round 1 they missed 2 to 7%. The alpha sweep is not monotone (7.3% at 0.01, 9.9% at 0.10, 5.9% at 0.20 on the retry-storm class) because a larger alpha also ends healthy rollouts sooner.

Reproduce (two to four minutes):

```bash
cd delivery-controller && KILN_HOLDOUT_REPORT=holdout.md KILN_HOLDOUT_SVG=../docs/assets/detector-tradeoff.svg go test ./internal/analysis -run TestDetectorHoldout -count=1
```

Keywords: sequential hypothesis testing, out-of-sample evaluation, false-positive control, time to detect, correlated failures, heavy-tailed latency, confidence intervals.

Story: a detector tuned on the traffic it is judged on will look better than it is, and the only cure is traffic it has never seen; here that traffic found the two assumptions the sequential test depends on, independence and a sample gate sized to the rate, and showed where each breaks. At Google scale the same release gate runs across services whose traffic spans five orders of magnitude and whose failures arrive in bursts, so a rule's documented blind spots matter more than its headline rate. The fix for two of the three losses is configuration the controller could derive from the rate it observes; that is the next change.

### 3.4 Controller scalability

**Before:** nothing measured beyond one rollout at a time. **After:** `TestCanaryRolloutScale` drives N concurrent rollouts through the reconciler on an envtest API server, at production defaults and then with the fix. Verbatim rows and the per-verb request rates are in `docs/METRICS.md`, "Controller scalability".

| Rollouts | Config | Rollout wave | Finished | Reconcile p50 | Tick lateness p50 / p99 (of 15 s) | Queue depth mean | Controller writes/s |
|---|---|---|---|---|---|---|---|
| 500 | 1 worker, 20 qps / 30 burst (as shipped) | 9m21s | 500 | 98ms | 35s / 35s | 491.6 of 500 | 33.8 |
| 1000 | as shipped | 12m39s, timed out | 0 | 100ms | 85s / 85s | 998.6 of 1000 | 33.3 |
| 500 | 8 workers, 100 qps / 200 burst (fix) | 2m39s | 500 | 14ms | 0s / 1s | 33.0 | 151.7 |
| 1000 | fix | 3m58s | 1000 | 81ms | 5s / 10s | 618.8 | 193.2 |

**The bottleneck:** `main.go` built its manager from a `rest.Config` with no QPS, so controller-runtime gave it 20 qps and 30 burst. At 500 rollouts the queue sat full, every analysis tick fired 35 s late and a reconcile spent most of its 98 ms waiting for a client token; at 1,000 nothing promoted in twelve minutes. **The fix** (`b13c410`): `--kube-api-qps` (100), `--kube-api-burst` (200) and `--concurrent-reconciles` (8). 500 rollouts now promote in 2m39s with ticks on time; 1,000 promote in 3m58s but already press the new budget (status patches at 101 per second, ticks 5 s late at the median). **Next in line**, measured not guessed: one live `GET virtualservices` per reconcile (44 per second at 1,000) because unstructured reads bypass the cache, and Events at 42 writes per second.

Reproduce (about 15 minutes for 500 and 1,000; add 2000,5000 for the sizes still running when this was written):

```bash
cd delivery-controller
KILN_SCALE_ROLLOUTS=500,1000 KILN_SCALE_REPORT=before.md go test -tags=scale ./test/scale/ -run TestCanaryRolloutScale -count=1 -timeout 120m
KILN_SCALE_ROLLOUTS=500,1000 KILN_SCALE_QPS=100 KILN_SCALE_BURST=200 KILN_SCALE_WORKERS=8 KILN_SCALE_REPORT=after.md go test -tags=scale ./test/scale/ -run TestCanaryRolloutScale -count=1 -timeout 120m
```

Keywords: controller scalability, reconcile latency, API server load, client-side throttling, concurrency, workqueue.

Story: a controller that is correct for one object can be silently late for a thousand, and lateness in a canary controller is risk: a regression that should roll back in 30 s rolls back in two minutes while the queue drains. The measurement found the limit in the client's own rate limiter, a default nobody had set, which is exactly the kind of fleet-wide quiet failure Google's SRE practice hunts with load tests before production does. The fix is a flag; the benchmark is what makes the next flag a number instead of an opinion.

### 3.5 GKE window (not run)

`terraform/gke/` creates a zonal GKE Standard cluster with an on-demand pool and a Spot pool carrying the economics labels and an Artifact Registry repository; `terraform validate` passes. `hack/gke-run.sh` applies it, pushes the two images, installs Prometheus, Jaeger and Istio 1.30.4 from the same sources the cluster uses, runs the scheduler as a second scheduler and the delivery controller without the audit pipeline, runs `TestPhase3` and `TestPhase4` against real Istio traffic for a 20-minute window, keeps the evidence, destroys everything from a trap on exit and verifies by label that no cluster, repository or disk remains; it refuses to start without `KILN_GKE_APPROVED=yes` and cuts itself at two hours. The README has the exact commands and the estimate: about $0.30 to $0.50 for the window, under $1 if the management fee applies, with the list prices named as inputs to confirm before approval. No `gcloud` is installed here, so the first command of that README is the user's.

Keywords: infrastructure as code, managed Kubernetes, cost estimation, teardown verification.

Story: the cheapest cloud experiment is one whose teardown is proven before its creation is approved, and whose cost is a number on the page, not a hope. Google's own guidance for experiments on shared fleets is the same: declare the resources, bound the time, verify the release. The plan is ready; it costs less than a coffee and needs one approval.

## 4. Keywords earned overall

cluster scheduling, scheduler framework, simulation validation, sequential hypothesis testing, out-of-sample evaluation, controller scalability, client-side throttling, infrastructure as code.

## 5. What remains unverified

- Phase 8's end-to-end proof and the `nightly-alert` job: unverified until the branch reaches `main`; the local cluster cannot hold the platform in 4 GB.
- The scheduler binary on the full cluster: the binary could not get through the full-cluster retries in the time given, so the model's full-cluster rows (zero saving, identical bills) are not confirmed by the binary; what is confirmed is that the binary is far slower there than the model.
- The GKE window: nothing applied.
- The held-out evaluation is still a simulation of traffic; the classes are shaped from published patterns, not measured from a real service.
- The scale benchmark runs against envtest on one laptop, with a stand-in for the Deployment controller and no Istio; the API server numbers are an envtest API server's, not a managed control plane's.

## 6. Commits

```text
f5461fc test(delivery): write the scale report after every size, retry etcd stalls, expire Events
f301afa test(scheduler): size the verdict wait by the batch and report the retry verdict rate
b13c410 perf(delivery): expose the API client budget and reconcile rollouts concurrently
94b2ad9 docs(metrics): the scheduler binary on the trace, empty cluster
096bec1 docs(metrics): held-out detector evaluation
a43f129 test(delivery): score the trade-off curve on the ten plotted classes; commit the plot
be22052 docs(testing): binary trace replay, hold-out detector evaluation, scale benchmark and the GKE plan
377c9cf test: keep envtest control planes apart on Windows and ride out etcd stalls
3c68ce7 feat(infra): terraform for a short GKE window with a cost estimate and verified teardown
cbaabc4 test(delivery): scale benchmark for concurrent CanaryRollouts under envtest
0c65942 docs: name the three gated kinds and the class guarantee instead of SLA
c15a268 test(delivery): hold-out detector evaluation on diurnal, retry-storm, heavy-tailed and low-traffic classes
7f6fb2f test(scheduler): replay the Google trace through the scheduler binary on envtest
6f35d38 refactor(scheduler): share the trace replay's billing ledger and allow a shorter window
plus the docs commits that close the round: this report, METRICS.md's scalability and full-cluster paragraphs, the README's reproduce commands.
```
