// Command ask puts one question to the configured language models and prints
// the answer, and on stderr which provider gave it.
//
//	DOCFIND_AI_PROVIDER=openai DOCFIND_AI_KEY=sk-... ask "پایتخت ایران کدام شهر است؟"
//	echo "a long question" | ask -persian
//	ask -search "what changed in Go 1.26?"
//	ask -chain        # the configured providers, in the order they are tried
//	ask -probe        # one tiny request to each, to see which accept their key
//
// The providers come from the environment (see the README): DOCFIND_AI_* for
// the first, DOCFIND_AI_*_2 to _5 for the fallbacks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-ai/dependencies"
	"github.com/Schayan-nk/docfind-ai/llm"
)

func main() {
	system := flag.String("system", "", "the system message")
	persian := flag.Bool("persian", false, "ask for natural, formal Persian (adds llm.PersianStyle to the system message)")
	search := flag.Bool("search", false, "answer from the web, with the pages used (OpenAI or Gemini only)")
	chainOnly := flag.Bool("chain", false, "list the configured providers and exit")
	probe := flag.Bool("probe", false, "send each provider the smallest request it takes, and report")
	timeout := flag.Duration("timeout", 5*time.Minute, "give up after this long")
	flag.Parse()

	chain := llm.ChainFromEnv()
	if len(chain) == 0 {
		fail(errors.New("no model is configured: set DOCFIND_AI_PROVIDER, DOCFIND_AI_KEY and, " +
			"for a local or compatible endpoint, DOCFIND_AI_URL"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch {
	case *chainOnly:
		for i, c := range chain {
			where := "hosted"
			if c.IsLocal() {
				where = "on this machine"
			}
			fmt.Printf("%d. %s  %s  (%s)\n", i+1, c.Describe(), c.Endpoint(), where)
		}
		return
	case *probe:
		bad := 0
		for i, c := range chain {
			if err := llm.ProbeCredit(ctx, c); err != nil {
				bad++
				fmt.Printf("%d. %s: %v\n", i+1, c.Describe(), err)
				continue
			}
			fmt.Printf("%d. %s: ok\n", i+1, c.Describe())
		}
		if bad > 0 {
			os.Exit(1)
		}
		return
	}

	question := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if question == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail(err)
		}
		question = strings.TrimSpace(string(b))
	}
	if question == "" {
		fail(errors.New("nothing to ask: give the question as arguments or on stdin"))
	}
	sys := strings.TrimSpace(*system)
	if *persian {
		sys = strings.TrimSpace(sys + " " + llm.PersianStyle)
	}

	// The breakers set a provider that keeps failing aside, so a dead first
	// entry costs one timeout, not one per question. In a one-shot command
	// they matter little; in a server they are the difference between a slow
	// minute and a fast fallback.
	llm.UseBreakers(dependencies.New())

	if *search {
		answer, sources, used, err := llm.SearchChain(ctx, chain, sys, question)
		if err != nil {
			fail(err)
		}
		fmt.Println(answer)
		for _, s := range sources {
			fmt.Printf("  %s  %s\n", s.Title, s.URL)
		}
		fmt.Fprintln(os.Stderr, "answered by "+used.Describe())
		return
	}
	answer, used, err := llm.CompleteChain(ctx, chain, sys, question)
	if err != nil {
		fail(err)
	}
	fmt.Println(answer)
	fmt.Fprintln(os.Stderr, "answered by "+used.Describe())
}

// fail prints the error the way the log would want it: a chain's every
// attempt, with the transport detail.
func fail(err error) {
	var ce *llm.ChainError
	var pe *llm.ProviderError
	switch {
	case errors.As(err, &ce):
		fmt.Fprintln(os.Stderr, ce.Verbose())
	case errors.As(err, &pe):
		fmt.Fprintln(os.Stderr, pe.Verbose())
	default:
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
