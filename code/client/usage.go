package client

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/server"
)

// FormatTokens is the /tokens line: context used of the window and model.
func FormatTokens(u *server.ThreadUsage) string {
	if u.ContextLimit == 0 {
		return fmt.Sprintf("%s tokens · %s (context window unknown)", HumanTokens(u.ContextTokens), u.Model)
	}
	return fmt.Sprintf("%s / %s tokens (%.1f%%) · %s", HumanTokens(u.ContextTokens), HumanTokens(u.ContextLimit), u.ContextPercent, u.Model)
}

// FormatContext is /context: what the latest model call's input was made of.
func FormatContext(u *server.ThreadUsage) string {
	lines := []string{
		"context " + FormatTokens(u),
		"  system prompt + tools  ~" + HumanTokens(u.FixedTokens),
		"  conversation           ~" + HumanTokens(u.ConversationTokens),
	}
	if u.ContextLimit > 0 {
		lines = append(lines, "  free                   "+HumanTokens(max(u.ContextLimit-u.ContextTokens, 0)))
	}
	return strings.Join(lines, "\n")
}

// FormatCost is /cost: tokens and estimated cost per model, then the total.
func FormatCost(u *server.ThreadUsage) string {
	if len(u.Models) == 0 {
		return "no model calls yet"
	}
	var lines []string
	for _, m := range u.Models {
		in := "in " + HumanTokens(m.InputTokens)
		if m.CacheReadTokens > 0 {
			in += " (" + HumanTokens(m.CacheReadTokens) + " cached)"
		}
		price := "unpriced"
		if m.CostUSD != nil {
			price = formatUSD(*m.CostUSD)
		}
		lines = append(lines, fmt.Sprintf("%s  %s · %s · out %s · %s", m.Model, plural(m.Calls, "call"), in, HumanTokens(m.OutputTokens), price))
	}
	total := "total " + formatUSD(u.TotalCostUSD)
	if len(u.Unpriced) > 0 {
		total += " (excluding unpriced models; add them to ~/.deepagents/" + server.PricesFile + ")"
	}
	return strings.Join(append(lines, total), "\n")
}

// HumanTokens formats a token count as 800, 12.3k or 1.2M.
func HumanTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1000)) + "k"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

func formatUSD(v float64) string { return fmt.Sprintf("$%.4f", v) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
