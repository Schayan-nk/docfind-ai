// Command agent runs the agent package end to end with three small tools over
// a folder: a goal in Persian, English or Finglish is read, a language model
// chooses among the tools -- among them and nothing else -- the plan runs,
// and the answer is written by Go from what the tools returned.
//
//	DOCFIND_AI_PROVIDER=openai DOCFIND_AI_KEY=sk-... \
//	go run ./examples/agent -dir ~/Documents "چند فایل پی‌دی‌اف دارم و کدام‌ها درباره‌ی قرارداد هستند؟"
//
// The model sees the goal and the list of tools, never a file name: the plan
// is fixed before anything is read, so nothing in a file can steer it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-ai/agent"
	"github.com/Schayan-nk/docfind-ai/llm"
)

// me is the person the run acts for. Every tool asks for "files.read", and
// this one holds it.
type me struct{}

func (me) Username() string     { return "local" }
func (me) Can(perm string) bool { return perm == "files.read" }

// walkLimit bounds how many files one tool call looks at.
const walkLimit = 5000

// faDigits writes ASCII digits as Persian ones, for text a Persian reader sees.
func faDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, s)
}

func faCount(n int) string { return faDigits(strconv.Itoa(n)) }

// clipList keeps the first five of a list, and says there are more.
func clipList(names []string) []string {
	if len(names) > 5 {
		return append(names[:5:5], "…")
	}
	return names
}

func main() {
	dir := flag.String("dir", ".", "the folder the tools look at")
	timeout := flag.Duration("timeout", 3*time.Minute, "give up after this long")
	flag.Parse()
	goal := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if goal == "" {
		fail(errors.New("give the goal as arguments"))
	}
	chain := llm.ChainFromEnv()
	if len(chain) == 0 {
		fail(errors.New("no model is configured: set DOCFIND_AI_PROVIDER and DOCFIND_AI_KEY (and DOCFIND_AI_URL for a local model)"))
	}

	reg := agent.NewRegistry()
	reg.MustRegister(findFiles(*dir))
	reg.MustRegister(fileTypes(*dir))
	reg.MustRegister(clock())

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	reading := agent.ReadGoal(goal)
	if reading.ReadAs != "" {
		fmt.Printf("read as: %s\n", reading.ReadAs)
	}
	planner := agent.ModelPlanner{Model: llm.Model{Chain: chain}, Registry: reg}
	plan, err := planner.Plan(ctx, me{}, reading)
	if err != nil {
		fail(err)
	}
	fmt.Printf("plan (%s):\n", plan.Provider)
	for i, s := range plan.Steps {
		fmt.Printf("  %d. %s %v\n", i+1, s.Title, s.Args)
	}

	runner := agent.NewRunner(reg, agent.DefaultLimits)
	run, err := runner.Start(me{}, plan, agent.Meta{Source: "text"})
	if err != nil {
		fail(err)
	}
	run, err = runner.Wait(ctx, run.ID)
	if err != nil {
		fail(err)
	}

	label := map[agent.EvidenceKind]string{agent.Fact: "واقعیت", agent.Inference: "نتیجه‌گیری",
		agent.Recommendation: "پیشنهاد", agent.Unknown: "نامعلوم"}
	fmt.Println("evidence:")
	for _, e := range run.Evidence {
		fmt.Printf("  [%s] %s\n", label[e.Kind], e.Text)
	}
	if a := run.Answer; a != nil {
		fmt.Println()
		fmt.Println(a.Summary)
		if a.Conclusion != "" {
			fmt.Println(a.Conclusion)
		}
	}
}

// findFiles finds files whose NAME carries a phrase. Names only: the text
// inside a file is never read, so there is nothing to leak and nothing that
// could talk back.
func findFiles(dir string) agent.Tool {
	return agent.Tool{
		Name: "find_files", Title: "جست‌وجو در نام فایل‌ها", Specialist: "files",
		Description: "find files whose name contains a phrase (in the folder and below it)",
		Params:      []agent.Param{{Name: "query", Label: "عبارت", Kind: agent.Text, Required: true, Max: 100}},
		Class:       agent.Read, Permissions: []string{"files.read"}, Audit: "files.find",
		Timeout: 20 * time.Second,
		Run: func(ctx context.Context, c *agent.Call) (*agent.Output, error) {
			q := strings.ToLower(strings.TrimSpace(c.Args["query"]))
			var hits []string
			err := walk(ctx, dir, func(rel string) {
				if strings.Contains(strings.ToLower(filepath.Base(rel)), q) {
					hits = append(hits, rel)
				}
			})
			if err != nil {
				return nil, err
			}
			sort.Strings(hits)
			text := fmt.Sprintf("«%s» در نام %s فایل آمده است", c.Args["query"], faCount(len(hits)))
			if len(hits) > 0 {
				text += ": " + strings.Join(clipList(hits), "، ")
			}
			return &agent.Output{Summary: fmt.Sprintf("%d files", len(hits)),
				Evidence: []agent.Evidence{{Kind: agent.Fact, Text: text}}}, nil
		},
	}
}

// fileTypes counts the files by extension.
func fileTypes(dir string) agent.Tool {
	return agent.Tool{
		Name: "file_types", Title: "شمارش فایل‌ها بر اساس نوع", Specialist: "files",
		Description: "count the files in the folder and below it by extension (pdf, docx, ...)",
		Class:       agent.Read, Permissions: []string{"files.read"}, Audit: "files.types",
		Timeout: 20 * time.Second,
		Run: func(ctx context.Context, c *agent.Call) (*agent.Output, error) {
			count := map[string]int{}
			total := 0
			err := walk(ctx, dir, func(rel string) {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(rel), "."))
				if ext == "" {
					ext = "بدون پسوند"
				}
				count[ext]++
				total++
			})
			if err != nil {
				return nil, err
			}
			kinds := make([]string, 0, len(count))
			for k := range count {
				kinds = append(kinds, k)
			}
			sort.Slice(kinds, func(i, j int) bool { return count[kinds[i]] > count[kinds[j]] })
			parts := make([]string, 0, len(kinds))
			for _, k := range kinds {
				parts = append(parts, fmt.Sprintf("%s %s", faCount(count[k]), k))
			}
			ev := []agent.Evidence{{Kind: agent.Fact,
				Text: fmt.Sprintf("%s فایل: %s", faCount(total), strings.Join(clipList(parts), "، "))}}
			if total > 0 && len(kinds) > 0 && count[kinds[0]]*2 > total {
				ev = append(ev, agent.Evidence{Kind: agent.Inference, Confidence: "high",
					Calc: "the most common extension is more than half of all files",
					Text: fmt.Sprintf("بیشتر فایل‌ها از نوع %s هستند", kinds[0])})
			}
			return &agent.Output{Summary: fmt.Sprintf("%d files", total), Evidence: ev}, nil
		},
	}
}

// clock says what time it is in Tehran.
func clock() agent.Tool {
	return agent.Tool{
		Name: "clock", Title: "ساعت و تاریخ تهران", Specialist: "general",
		Description: "the current date and time in Tehran",
		Class:       agent.Read, Permissions: []string{"files.read"}, Audit: "clock.now",
		Timeout: time.Second,
		Run: func(ctx context.Context, c *agent.Call) (*agent.Output, error) {
			loc, err := time.LoadLocation("Asia/Tehran")
			if err != nil {
				loc = time.FixedZone("IRST", 3*3600+1800)
			}
			now := time.Now().In(loc)
			return &agent.Output{Summary: now.Format("15:04"), Evidence: []agent.Evidence{{Kind: agent.Fact,
				Text: "اکنون در تهران " + faDigits(now.Format("2006-01-02 15:04")) + " است"}}}, nil
		},
	}
}

// walk calls fn with each regular file's path relative to dir, at most
// walkLimit of them, and stops when ctx does.
func walk(ctx context.Context, dir string, fn func(rel string)) error {
	n := 0
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable folder is skipped, not fatal
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type().IsRegular() {
			if n++; n > walkLimit {
				return filepath.SkipAll
			}
			rel, _ := filepath.Rel(dir, path)
			fn(rel)
		}
		return nil
	})
}

func fail(err error) {
	var ce *llm.ChainError
	if errors.As(err, &ce) {
		fmt.Fprintln(os.Stderr, ce.Verbose())
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
