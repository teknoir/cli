# The council eval

`TestEval` (`../../eval_test.go`) runs whole council turns against a scripted model and a fixture
MCP platform (`eval_platform_test.go`: production tool names, results indented as the council's
server sends them) through the real `tools.Set`. Run: `go test -run TestEval -v ./pkg/council/`.

It pins Stage 0: a cut result's `RESULT PARTIAL` header (counts and offset checked against the
result) and the `read_result` page that follows it; one `<untrusted>` block per result, in which
every tag of the corpus has only its opener escaped (`escaped` below), arriving as JSON
(`mqtt_get_sample`), as `json.Marshal` JSON where `<` is `\u003c` (`mqtt_sample`), and as plain text
(`inspect_logs`); the level refusal; read never offered `inspect_*`; the round and token budgets.

Add a scenario as a subtest of `TestEval`: `newEval`, set `h.p.devices`/`sample`/`preview`/`logs`,
`h.model.push`, `h.start`, `h.waitFinish`, `h.stop`. A `scriptStep.build` runs on the turn's
goroutine: report with `t.Errorf` and return a terminal step, never `t.Fatal`.

`injection.json` entries are `{"marker", "why", "payload", "escaped"}`, pure ASCII (`\uXXXX`
escapes). Each payload carries its unique marker; a tag payload also gets `escaped`, the payload
with each opener replaced by `&lt;`.
