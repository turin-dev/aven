package catalog

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// 템플릿을 내보낼 때 명령과 변수 값 안에 그대로 적힌 비밀(API 키, 토큰, 개인 키)을 찾는다.
// 이름으로 비밀을 알아보는 LooksSecret과 달리 값의 모양을 본다: 알려진 접두사(sk_, ghp_, github_pat_, xox?-,
// AKIA/ASIA, -----BEGIN … -----END …-----)이거나, SecretMinLen자 이상이고 엔트로피가 SecretEntropy 이상인 글자.

const (
	// SecretMinLen은 엔트로피로 알아보는 글자의 최소 길이다.
	SecretMinLen = 24
	// SecretEntropy는 글자당 섀넌 엔트로피(비트) 기준이다. 무작위 base62 24자는 약 4.2, 16진수 32자는 약 3.7,
	// 사람이 쓴 낱말·경로는 대개 이보다 낮거나 구분자가 많다.
	SecretEntropy = 3.5
)

// SecretSpan은 s[Start:End]가 비밀처럼 보인다는 뜻이다. Reason은 알아본 까닭(접두사 또는 "entropy")이다.
type SecretSpan struct {
	Start, End int
	Reason     string
}

var (
	pemRe       = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]{1,64}-----(?s:.*?)(?:-----END [A-Z0-9 ]{1,64}-----|\z)`)
	secretTokRe = regexp.MustCompile(`[A-Za-z0-9+/_\-.~]+=*`)
	exprRe      = regexp.MustCompile(`\$\{\{.*?\}\}`)
	awsKeyRe    = regexp.MustCompile(`^(AKIA|ASIA)[0-9A-Z]{16}`)
	slackRe     = regexp.MustCompile(`^xox[a-z]-[A-Za-z0-9-]{10,}`)
)

// Entropy는 s의 글자(바이트)당 섀넌 엔트로피다.
func Entropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

// secretToken은 낱말 하나가 비밀처럼 보이면 까닭을 돌려준다.
func secretToken(t string) string {
	switch {
	case strings.HasPrefix(t, "sk_") && len(t) >= 16:
		return "sk_"
	case strings.HasPrefix(t, "ghp_") && len(t) >= 20:
		return "ghp_"
	case strings.HasPrefix(t, "github_pat_") && len(t) >= 30:
		return "github_pat_"
	case slackRe.MatchString(t):
		return "xox"
	case awsKeyRe.MatchString(t):
		return "AKIA"
	}
	core := strings.TrimRight(t, "=")
	if len(core) < SecretMinLen || strings.HasPrefix(core, "/") || strings.HasPrefix(core, ".") || strings.HasPrefix(core, "-") {
		return ""
	}
	var alnum, digits, upper, lower int
	for i := 0; i < len(core); i++ {
		c := core[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c >= 'A' && c <= 'Z':
			upper++
		case c >= 'a' && c <= 'z':
			lower++
		default:
			continue
		}
		alnum++
	}
	// 구분자가 많으면(경로, 파일 이름, 상수 이름, UUID) 비밀로 보지 않는다. 숫자가 거의 없으면 낱말이다.
	if float64(alnum) < 0.9*float64(len(core)) || digits < 2 || upper+lower < 4 {
		return ""
	}
	if Entropy(core) >= SecretEntropy {
		return "entropy"
	}
	return ""
}

// SecretLiterals는 s 안의 비밀처럼 보이는 글자 위치를 앞에서부터 돌려준다(겹치지 않는다). ${{ … }} 표현식 안은 보지 않는다.
func SecretLiterals(s string) []SecretSpan {
	var spans []SecretSpan
	skip := exprRe.FindAllStringIndex(s, -1)
	for _, m := range pemRe.FindAllStringIndex(s, -1) {
		spans = append(spans, SecretSpan{Start: m[0], End: m[1], Reason: "-----BEGIN"})
	}
	inside := func(a, b int, rs [][]int) bool {
		for _, r := range rs {
			if a < r[1] && b > r[0] {
				return true
			}
		}
		return false
	}
	pems := make([][]int, len(spans))
	for i, sp := range spans {
		pems[i] = []int{sp.Start, sp.End}
	}
	for _, m := range secretTokRe.FindAllStringIndex(s, -1) {
		if inside(m[0], m[1], skip) || inside(m[0], m[1], pems) {
			continue
		}
		if why := secretToken(s[m[0]:m[1]]); why != "" {
			spans = append(spans, SecretSpan{Start: m[0], End: m[1], Reason: why})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans
}

// ReplaceSecretLiterals는 비밀처럼 보이는 글자를 repl의 결과로 바꾸고 찾은 위치들을 돌려준다.
func ReplaceSecretLiterals(s string, repl func(SecretSpan) string) (string, []SecretSpan) {
	spans := SecretLiterals(s)
	if len(spans) == 0 {
		return s, nil
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.Start])
		b.WriteString(repl(sp))
		last = sp.End
	}
	b.WriteString(s[last:])
	return b.String(), spans
}
