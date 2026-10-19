# docfind-ai

Two Go packages for putting a language model behind an application without
handing it the keys to the application:

- **`llm`** asks a model a question through a chain of providers: an
  OpenAI-compatible endpoint (OpenAI, Groq, OpenRouter, a relay, Ollama on the
  same machine), Google Gemini or Anthropic Claude, tried in order until one
  answers, each behind a circuit breaker.
- **`agent`** turns a goal into a plan of tools the application registered,
  runs the plan, and writes the answer in Go from what the tools returned. The
  model chooses among the tools; it never sees the data and never writes a
  number.

Standard library only. Persian first: the sentences a person may see are in
Persian, goals typed in Finglish or spoken as number words are understood,
and a style rule keeps a model's Persian natural. It began as the assistant
inside DocFind, a document and workflow system, and is published here on its
own.

## llm

```go
chain := llm.ChainFromEnv()          // DOCFIND_AI_*, see below
llm.UseBreakers(dependencies.New()) // set a failing provider aside

answer, used, err := llm.CompleteChain(ctx, chain,
	"پاسخ را کوتاه بده. "+llm.PersianStyle, question)
// used.Describe() names the provider that answered.
```

What it takes care of:

- **The chain.** Up to five entries, tried in order. A refused key, exhausted
  credit, an unknown model, a timeout, a 5xx or an empty answer moves on to
  the next entry; a prompt refused on content grounds stops, because it would
  fail the same way everywhere. When everything fails, every attempt is
  reported, with a short sentence for a reader and the transport detail for
  the log (`ChainError.Verbose`).
- **Circuit breakers**, one per endpoint: two failures in a row set it aside
  for thirty seconds, longer each time it fails again, and a probe finds it
  back.
- **The request shape per model family.** The gpt-5 and o-series models get
  `max_completion_tokens` and low reasoning effort (sized so the hidden
  reasoning cannot eat the whole answer); older and compatible models get
  `max_tokens` and a temperature; Claude gets a ceiling it does not run out
  of, and is told when it did.
- **Keys stay secret.** Never in an error or a log line (redacted), never in a
  URL (Gemini's goes in a header), no empty `Authorization` header to a relay
  that holds its own credentials.
- **A network guard.** HTTP/1.1 only, because a middlebox that mishandles h2
  makes requests fail invisibly, and a dial guard that refuses link-local,
  metadata and multicast addresses whatever the configured name resolves to.
- **Embeddings** (`Embed`, `Cosine`; a local endpoint first so text stays in
  the building), **web search** with the pages used (`SearchChain`: OpenAI's
  web search tool or Gemini grounded with Google Search), and **credit checks**
  for an OpenAI account (`ProbeCredit`, `QuotaSpent`, `OrgCosts` with an Admin
  key).

`llm.Model{Chain: chain}` is the language model `agent` plans with.

### Configuration

Read from the environment, so a key never ends up in a flag, a shell history
or a file under version control. A `Config` can also be built in code;
`WithDefaults` fills in what it leaves blank.

| Variable | |
|---|---|
| `DOCFIND_AI_PROVIDER` | `openai`, `gemini` or `anthropic`. Inferred from the key when unset (`sk-ant-` is Claude, `sk-` is OpenAI). |
| `DOCFIND_AI_KEY` | The key. Optional for a local relay that holds its own. |
| `DOCFIND_AI_MODEL` | The model; a comma-separated list becomes one chain entry per model. A default follows the provider and the address. |
| `DOCFIND_AI_URL` | The base URL of an OpenAI-compatible endpoint, such as `http://127.0.0.1:11434/v1` for Ollama. |
| `DOCFIND_AI_PROVIDER_2` ... `_5`, and `_KEY_2`, `_MODEL_2`, `_URL_2` ... | Fallbacks, in order. An entry of the same provider inherits whatever it leaves blank from the first. |
| `DOCFIND_AI_TIMEOUT` | Seconds (or a Go duration) to wait for an answer. Default: two minutes hosted, three for a model on this machine, four for Claude. |
| `DOCFIND_EMBED_MODEL` | An embedding model; embeddings are off without it. |

## agent

```go
reg := agent.NewRegistry()
reg.MustRegister(agent.Tool{
	Name: "find_files", Title: "جست‌وجو در نام فایل‌ها", Specialist: "files",
	Description: "find files whose name contains a phrase",
	Params:      []agent.Param{{Name: "query", Label: "عبارت", Kind: agent.Text, Required: true}},
	Class:       agent.Read, Permissions: []string{"files.read"}, Audit: "files.find",
	Run:         findFiles,
})

reading := agent.ReadGoal(goal) // spoken numbers, digits, Finglish
plan, err := agent.ModelPlanner{Model: llm.Model{Chain: chain}, Registry: reg}.Plan(ctx, person, reading)
runner := agent.NewRunner(reg, agent.DefaultLimits)
run, err := runner.Start(person, plan, agent.Meta{})
```

The rules it holds to:

- **A tool is a Go function** registered by name, with typed parameters
  (text, int, ref, email, phone, time, choice), a class, the permissions it
  needs, an audit event and a timeout. Every argument is validated before it
  runs; nothing can call a tool that is not registered.
- **The plan is fixed before anything is read.** The model planner is shown
  the goal and the tools this person may use, never a row, a document or an
  email, so nothing a tool returns can be read as an instruction. One invented
  tool, or one argument out of its schema, refuses the whole plan.
- **Reading runs; changing is proposed.** A `Read` tool runs without asking.
  An `Execute` tool can never be a plan step: a plan can only propose an
  action, and carrying it out is a separate request the person makes.
- **Conclusions are written by Go**, as evidence marked fact, inference,
  recommendation or unknown. `Compose` writes the answer from them, in the
  same four parts on a page and aloud; an application with reasoning of its
  own sets `Runner.Finish` to run it first.
- **Runs are bounded**: steps, tool calls, retries (transient failures only),
  duration, runs per person and in all. Each runs in the background, can be
  cancelled, and every tool call is audited, with a message body never in the
  record.

For Persian input, `ReadGoal` turns spoken number words into digits («هزار و
دویست و سی و چهار» is 1234, while «نه» on its own stays "no"), Persian and
Arabic digits into ASCII, and a goal typed in Finglish into the Persian it
means. `Confirmation` reads a spoken or typed yes, and only an unqualified yes
is one: «بله» is, «آره شاید» is not.

## Examples

```sh
go build -o ask ./cmd/ask
./ask -chain                      # the configured providers, in order
./ask -probe                      # which of them accept their key
./ask "پایتخت ایران کدام شهر است؟"
./ask -search "what changed in Go 1.26?"

go run ./examples/agent -dir ~/Documents "chand file pdf daram?"
```

`examples/agent` plans with the configured model over three read-only tools
(file names, file types, the time in Tehran) and prints the plan, the
evidence and the answer.

## Development

```sh
go vet ./...
go test ./...
```

Every test runs against local fakes. One test makes a real, billable request
to the configured chain, and runs only when asked:

```sh
DOCFIND_AI_LIVE_TEST=1 DOCFIND_AI_PROVIDER=openai DOCFIND_AI_KEY=sk-... go test ./llm/ -run Live -v
```

## License

MIT. See [LICENSE](LICENSE).
