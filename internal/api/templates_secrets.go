package api

import (
	"fmt"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
)

// redactSecretLiterals는 내보내는 값(변수 값, 명령) 안에 그대로 적힌 비밀처럼 보이는 글자(catalog.SecretLiterals)를
// 모드에 따라 바꾼다: generate면 ${{ secret(32) }}, inputs면 필수 비밀 입력 ${{ inputs.X }}. 바꾸면 경고를 남긴다
// (값 자체는 경고에 넣지 않는다). key는 입력 이름을 만들 때, what은 경고에 쓴다.
func redactSecretLiterals(d *catalog.Definition, mode, svc, key, what, v string, warnings *[]string) string {
	out, spans := catalog.ReplaceSecretLiterals(v, func(catalog.SecretSpan) string {
		if mode == "inputs" {
			ik := uniqueInputKey(d, catalog.InputKey(svc, key))
			d.Inputs = append(d.Inputs, catalog.Input{Key: ik, Label: svc + ": " + what + "의 비밀값", Required: true, Secret: true})
			return "${{ inputs." + ik + " }}"
		}
		return "${{ secret(32) }}"
	})
	if len(spans) == 0 {
		return v
	}
	seen := map[string]bool{}
	var why []string
	for _, sp := range spans {
		if !seen[sp.Reason] {
			seen[sp.Reason] = true
			why = append(why, sp.Reason)
		}
	}
	to := "새 비밀값(${{ secret(32) }})으로"
	if mode == "inputs" {
		to = "필수 입력으로"
	}
	*warnings = append(*warnings, fmt.Sprintf("%s: %s에서 비밀처럼 보이는 값 %d개(%s)를 %s 바꿨습니다",
		svc, what, len(spans), strings.Join(why, ", "), to))
	return out
}

// redactCommandSecrets는 시작/배포 전 명령 안의 비밀처럼 보이는 글자를 바꾼다.
func redactCommandSecrets(d *catalog.Definition, mode, svc string, st *catalog.Settings, warnings *[]string) {
	for _, f := range []struct {
		p          **string
		key, label string
	}{
		{&st.StartCommand, "START_COMMAND", "시작 명령"},
		{&st.PreDeployCommand, "PRE_DEPLOY_COMMAND", "배포 전 명령"},
	} {
		if *f.p == nil {
			continue
		}
		v := redactSecretLiterals(d, mode, svc, f.key, f.label, **f.p, warnings)
		*f.p = &v
	}
}
