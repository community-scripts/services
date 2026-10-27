package main

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	msgANSI       = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	msgStamp      = regexp.MustCompile(`^(?:\[\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}\]|\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})\s*`)
	msgLevel      = regexp.MustCompile(`^\[(ERROR|WARN|WARNING)\]\s*`)
	msgEngineEcho = regexp.MustCompile(`^(?:in line \d+: exit code|Hint:|exit_code=\d+)`)

	// A specific cause: what ran out, what was refused, what was missing.
	msgCause = []*regexp.Regexp{
		regexp.MustCompile(`(?i)no space left|permission denied|operation not permitted|not found|no such file|` +
			`connection refused|could not resolve|temporary failure|timed out|unable to locate package|` +
			`unmet dependencies|\bkilled\b|out of memory|rate limit|illegal instruction|segmentation fault|` +
			`\b(?:enospc|eacces|enoent|econnrefused|etimedout)\b|\bavx\d*\b|x509:`),
		regexp.MustCompile(`(?i)certificate.*(?:fail|error|invalid|expired|verif|unknown|trust)|(?:fail|error|invalid|expired|verif|unknown|untrusted).*certificate`),
		regexp.MustCompile(`(?i)\b(?:http(?:/[\d.]+)?|status(?: code)?|returned error|error)[\s:=]+[45]\d\d\b`),
		regexp.MustCompile(`(?i)\b[45]\d\d\s+(?:bad request|unauthorized|forbidden|not found|method not allowed|gone|` +
			`too many requests|internal server error|bad gateway|service unavailable|gateway timeout)\b`),
	}
	// A line that says it is an error without saying of what kind.
	msgError = []*regexp.Regexp{
		regexp.MustCompile(`^E: \S`),
		regexp.MustCompile(`(?i)\berror:\s*\S`),
		regexp.MustCompile(`(?i)\bfatal:\s*\S`),
		regexp.MustCompile(`^[A-Za-z_][\w.]*(?:Error|Exception):\s*\S`),
	}
	msgGeneric = regexp.MustCompile(`\bFATAL\b|(?i:\bfailed\b)`)
)

// deriveErrorMessage picks the line of a trace's output that best says what
// went wrong: the lowest specific cause, else the lowest error line, else the
// lowest generic failure. "" when the trace has no output or nothing matches.
func deriveErrorMessage(trace string) string {
	lines := traceOutputLines(trace)
	if len(lines) == 0 {
		return ""
	}
	type candidate struct {
		text   string
		engine bool
	}
	cands := make([]candidate, 0, len(lines))
	for _, l := range lines {
		l = msgStamp.ReplaceAllString(trimGlyphs(msgANSI.ReplaceAllString(l, "")), "")
		engine := strings.HasPrefix(l, "[ERROR]")
		l = strings.TrimSpace(trimGlyphs(msgLevel.ReplaceAllString(l, "")))
		if len(l) < 4 || strings.HasPrefix(l, "+ ") || l == "---" || msgEngineEcho.MatchString(l) {
			continue
		}
		cands = append(cands, candidate{l, engine})
	}

	pick := func(match func(candidate) bool) string {
		for i := len(cands) - 1; i >= 0; i-- {
			if match(cands[i]) {
				return sanitizeShort(sanitizeIPs(cands[i].text), 300)
			}
		}
		return ""
	}
	anyOf := func(res []*regexp.Regexp, s string) bool {
		for _, re := range res {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	if m := pick(func(c candidate) bool { return anyOf(msgCause, c.text) }); m != "" {
		return m
	}
	if m := pick(func(c candidate) bool { return c.engine || anyOf(msgError, c.text) }); m != "" {
		return m
	}
	return pick(func(c candidate) bool { return msgGeneric.MatchString(c.text) })
}

func trimGlyphs(s string) string {
	return strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.In(r, unicode.So, unicode.Mn, unicode.Sk)
	})
}

// traceOutputLines returns the lines below the header's "---" separator.
func traceOutputLines(trace string) []string {
	trace = strings.ReplaceAll(trace, "\r\n", "\n")
	if strings.HasPrefix(trace, "---\n") {
		return strings.Split(trace[4:], "\n")
	}
	if _, out, ok := strings.Cut(trace, "\n---\n"); ok {
		return strings.Split(out, "\n")
	}
	if _, out, ok := strings.Cut(trace, "|---|"); ok {
		return strings.Split(out, "|")
	}
	return nil
}

// needsErrorMessage reports whether a record should carry a derived message.
func needsErrorMessage(status, message string) bool {
	return message == "" && (status == "failed" || status == "unknown")
}
