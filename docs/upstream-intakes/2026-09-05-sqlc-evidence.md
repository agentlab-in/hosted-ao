# sqlc source repair support evidence

Historical isolated helper evidence. This does not replace owner exact-head checks. The owner had already integrated the five equivalent query reorders in 877b3a321; reconciliation added only the two explanatory SQL comments. No helper-generated Go files were imported.

# sqlc agent-switch projection repair

Supplement to backend v3. Apply after the source resolutions, using the owner's already-corrected sqlc.yaml. This patch changes only `backend/internal/storage/sqlite/queries/agent_switching.sql`.

## Root cause

Migration 0125 appends `agent_switches.failure_point` at the end of the table, after `final_handoff_hash`. Five explicit full-row SELECT projections instead placed failure_point before requested_at. Although the selected column set matched, its order did not match the table model.

Fresh sqlc v1.31.1 generation therefore produced separate GetAgentSwitchRow, GetAgentSwitchByIdempotencyKeyRow, GetActiveAgentSwitchRow and list-row types instead of reusing gen.AgentSwitch. The store intentionally shares the common model and reassigns lookup results across retries; the regenerated types broke that boundary.

The corrected owner bool overrides were not responsible. The stale upstream generated source had concealed this query/schema order mismatch during the earlier fixture tests. Fresh generation reproduced the owner's compile failures locally before the patch.

## Resolution

Move failure_point to the final position in these five SELECT lists:

- GetAgentSwitch
- GetAgentSwitchByIdempotencyKey
- GetActiveAgentSwitch
- ListActiveAgentSwitches
- ListAgentSwitches

Keep every selected column, filter, ordering rule, insert/update statement, and schema unchanged. Add a short source comment documenting why projection order matters. Do not change already-merged migrations, add store casts/conversion adapters, or hand-edit generated structs.

The resulting fresh generator output returns AgentSwitch from each single-row query and []AgentSwitch from both list queries, restoring the existing store boundary.

## Apply and regenerate

```sh
support=/Users/harshitsinghbhandari/.ao/data/worktrees/hosted-ao/hosted-ao-91/.intake-support/artifacts-sqlc
git apply --check "$support/agent-switch-projection-order.patch"
git apply "$support/agent-switch-projection-order.patch"
npm run sqlc
```

Then rerun the conductor's focused suites and repeat regeneration. The patch does not touch sqlc.yaml, so it retains the owner's corrected agent_switch_failure_policy.enabled and cloud_offering bool overrides.

## Verification

Only the isolated support fixture was changed. The owner's corrected sqlc configuration was copied read-only as a verification input. The owner query was confirmed byte-identical to the frozen fixture before applying the fix. No owner source or generated files were edited.

- Reproduced the pre-patch generated-row compile failure with fresh sqlc output: compile-before.log.
- Fresh `npm run sqlc` passed with the query fix: generate-after.log.
- All three one-row accessors return AgentSwitch; both list accessors return []AgentSwitch.
- A second fresh sqlc generation produced identical bytes for all 25 generated Go files: determinism.txt and generated-hashes-first.json.
- Full `go test ./internal/storage/sqlite/...` passed, including existing switch idempotency, active/history reads, failure-point persistence, migration, and store tests: storage-after.log.
- `go build ./...` passed against regenerated sqlc output: build-after.log (empty on success).
- Controller, CLI, and daemon dependent tests: dependents-after.log.
- Plain patch applied cleanly to its documented baseline and reproduced the tested query bytes.

No generated output is included in this handoff. Generation ran only in the disposable support fixture to close the stale-generated verification gap. The owner retains final regeneration, merged OpenAPI validation and integration responsibility. No merge, commit, push, PR, or migration operation was performed.

## Preserved helper logs

### build-after.log

SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

```text
```

### compile-after-confirmed.log

SHA-256: `bef19e6bc5063419a3a8d97c6717114c759d839260b1f6af0fa896650301c7fe`

```text
ok  	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store	0.162s [no tests to run]
```

### compile-before.log

SHA-256: `328e2e623c1eefd7fecb6a3018ca0fe24b3977f11c2022d1adc4e276d20648ad`

```text
# github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store [github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store.test]
internal/storage/sqlite/store/agent_switching_store.go:144:34: cannot use row (variable of struct type gen.GetAgentSwitchByIdempotencyKeyRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:155:13: cannot use 1st function result (value of struct type gen.GetActiveAgentSwitchRow) as gen.GetAgentSwitchByIdempotencyKeyRow value in multiple assignment
internal/storage/sqlite/store/agent_switching_store.go:157:34: cannot use row (variable of struct type gen.GetAgentSwitchByIdempotencyKeyRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:165:16: cannot use 1st function result (value of struct type gen.GetAgentSwitchRow) as gen.GetAgentSwitchByIdempotencyKeyRow value in multiple assignment
internal/storage/sqlite/store/agent_switching_store.go:166:29: cannot use row (variable of struct type gen.GetAgentSwitchByIdempotencyKeyRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:183:28: cannot use row (variable of struct type gen.GetAgentSwitchRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:209:28: cannot use row (variable of struct type gen.GetActiveAgentSwitchRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:402:27: cannot use row (variable of struct type gen.GetAgentSwitchRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:499:27: cannot use switchRow (variable of struct type gen.GetAgentSwitchRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:596:27: cannot use switchRow (variable of struct type gen.GetAgentSwitchRow) as gen.AgentSwitch value in argument to agentSwitchFromGen
internal/storage/sqlite/store/agent_switching_store.go:183:28: too many errors
FAIL	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store [build failed]
FAIL
```

### dependents-after.log

SHA-256: `e1b5fc9491f5b71f50d22a895e5dabcab579e322939ea5916378c09b14166fd8`

```text
ok  	github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers	4.027s
ok  	github.com/aoagents/agent-orchestrator/backend/internal/cli	2.766s
ok  	github.com/aoagents/agent-orchestrator/backend/internal/daemon	1.951s
```

### generate-after.log

SHA-256: `bf3902505ba0ca366c35f2c626c47301688474a1d7a099e929f1ae5b7ac8c687`

```text
npm notice run sqlc
npm notice run cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

### generate-before.log

SHA-256: `bf3902505ba0ca366c35f2c626c47301688474a1d7a099e929f1ae5b7ac8c687`

```text
npm notice run sqlc
npm notice run cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

### generate-repeat.log

SHA-256: `bf3902505ba0ca366c35f2c626c47301688474a1d7a099e929f1ae5b7ac8c687`

```text
npm notice run sqlc
npm notice run cd backend && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

### storage-after.log

SHA-256: `ae47140974d8e7c7be346674727e80bc0c5241a9cb2032db23fe20f0165f156a`

```text
ok  	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite	12.162s
?   	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen	[no test files]
ok  	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest	0.657s
ok  	github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store	7.181s
```

### determinism.txt

```text
PASS: two fresh sqlc v1.31.1 generations produced identical bytes for all 25 generated Go files.
```
