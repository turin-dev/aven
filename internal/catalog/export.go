package catalog

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

var (
	secretKeyRe = regexp.MustCompile(`(?i)(SECRET|PASSWORD|TOKEN|KEY|PRIVATE)`)
	// URL 안의 비밀번호(scheme://user:pass@host)도 비밀로 본다.
	urlPassRe = regexp.MustCompile(`://[^/\s:@]*:[^/\s@]+@`)
)

// LooksSecret은 내보낼 때 값을 숨겨야 하는 변수인지 본다: 이름이 비밀처럼 생겼고 참조가 없는 값,
// 또는 참조 없이 비밀번호가 들어 있는 URL.
func LooksSecret(key, value string) bool {
	if strings.Contains(value, "${{") {
		return false
	}
	return secretKeyRe.MatchString(key) || urlPassRe.MatchString(value)
}

// BaselineSpec은 템플릿으로 새로 만든 인스턴스가 가질 기본 설정이다(내보낼 때 바뀐 것만 남기기 위한 기준).
func BaselineSpec(def ServiceDef) store.Spec {
	in := &store.Instance{}
	in.Spec.Defaults()
	def.Variables = nil
	def.ApplyToInstance(in)
	if def.SourceType == "repo" && def.Settings.RestartPolicy == nil {
		in.RestartPolicy = "on_failure"
	}
	in.Spec.Defaults()
	return in.Spec
}

// SettingsDiff는 cur가 base와 다른 설정만 담는다.
func SettingsDiff(base, cur store.Spec) Settings {
	var st Settings
	ip := func(a, b *int) *int {
		if reflect.DeepEqual(a, b) || b == nil {
			return nil
		}
		v := *b
		return &v
	}
	sp := func(a, b *string) *string {
		if reflect.DeepEqual(a, b) || b == nil {
			return nil
		}
		v := *b
		return &v
	}
	s := func(a, b string) *string {
		if a == b {
			return nil
		}
		return &b
	}
	i := func(a, b int) *int {
		if a == b {
			return nil
		}
		return &b
	}
	bo := func(a, b bool) *bool {
		if a == b {
			return nil
		}
		return &b
	}
	st.Port = ip(base.Port, cur.Port)
	st.StartCommand = sp(base.StartCommand, cur.StartCommand)
	st.PreDeployCommand = sp(base.PreDeployCommand, cur.PreDeployCommand)
	st.Builder = s(base.Builder, cur.Builder)
	st.DockerfilePath = s(base.DockerfilePath, cur.DockerfilePath)
	if len(cur.WatchPaths) > 0 && !reflect.DeepEqual(base.WatchPaths, cur.WatchPaths) {
		st.WatchPaths = append([]string{}, cur.WatchPaths...)
	}
	st.AutoDeploy = bo(base.AutoDeploy, cur.AutoDeploy)
	st.HealthcheckType = s(base.HealthcheckType, cur.HealthcheckType)
	st.HealthcheckPath = s(base.HealthcheckPath, cur.HealthcheckPath)
	st.HealthcheckTimeoutS = i(base.HealthcheckTimeoutS, cur.HealthcheckTimeoutS)
	st.RestartPolicy = s(base.RestartPolicy, cur.RestartPolicy)
	st.RestartMaxRetries = i(base.RestartMaxRetries, cur.RestartMaxRetries)
	st.Replicas = i(base.Replicas, cur.Replicas)
	st.CronSchedule = sp(base.CronSchedule, cur.CronSchedule)
	st.SleepWhenIdle = bo(base.SleepWhenIdle, cur.SleepWhenIdle)
	st.SleepAfterS = i(base.SleepAfterS, cur.SleepAfterS)
	st.MemoryLimitMB = ip(base.MemoryLimitMB, cur.MemoryLimitMB)
	if cur.CPULimit != nil && !reflect.DeepEqual(base.CPULimit, cur.CPULimit) {
		v := *cur.CPULimit
		st.CPULimit = &v
	}
	if len(cur.BuildSecrets) > 0 {
		st.BuildSecrets = append([]string{}, cur.BuildSecrets...)
	}
	st.WaitForCI = bo(base.WaitForCI, cur.WaitForCI)
	st.OverlapSeconds = i(base.OverlapSeconds, cur.OverlapSeconds)
	st.DrainingSeconds = ip(base.DrainingSeconds, cur.DrainingSeconds)
	if len(cur.PrivatePorts) > 0 && !reflect.DeepEqual(base.PrivatePorts, cur.PrivatePorts) {
		st.PrivatePorts = append([]int{}, cur.PrivatePorts...)
	}
	return st
}

// InputKey는 서비스 변수를 입력으로 바꿀 때 쓰는 입력 이름이다(예: web, API_TOKEN → WEB_API_TOKEN).
func InputKey(service, key string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(service + "_" + key) {
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			b.WriteRune(c)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
