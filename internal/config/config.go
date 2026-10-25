// Package config는 저장소의 config-as-code(aven.json / railway.json / railway.toml / aven.toml)를 읽어
// 배포 설정을 덮어쓴다. 키는 railway.json 스키마를 따른다.
//
// 모르는 키("$schema", nixpacksPlan 등)는 무시한다(JSON/TOML 모두). 아는 키의 타입이 틀리면 오류다.
// environments.<환경 이름>.{build,deploy}는 그 환경에 배포할 때 최상위 값 위에 덮어쓴다.
// 정확히 같은 이름이 없고 PR 미리보기 환경이면 environments.pr을 쓴다.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/turin-dev/aven/internal/store"
)

// Files는 찾는 순서다. 처음 발견한 파일 하나만 쓴다.
var Files = []string{"aven.json", "aven.toml", "keel.json", "keel.toml", "railway.json", "railway.toml"}

// PREnvironment는 PR 미리보기 환경에 쓰는 environments 키다.
const PREnvironment = "pr"

// 배포 전환 시간 상한(초). draining은 agent의 docker stop -t 상한(300)과 같다.
const (
	MaxOverlapSeconds  = 600
	MaxDrainingSeconds = 300
)

type Build struct {
	Builder         *string  `json:"builder" toml:"builder"`
	DockerfilePath  *string  `json:"dockerfilePath" toml:"dockerfilePath"`
	WatchPatterns   []string `json:"watchPatterns" toml:"watchPatterns"`
	BuildCommand    *string  `json:"buildCommand" toml:"buildCommand"`
	RailpackVersion *string  `json:"railpackVersion" toml:"railpackVersion"` // 지원하지 않는다(로그만 남긴다)
}

type Deploy struct {
	StartCommand            *string `json:"startCommand" toml:"startCommand"`
	PreDeployCommand        any     `json:"preDeployCommand" toml:"preDeployCommand"` // string | string[]
	HealthcheckPath         *string `json:"healthcheckPath" toml:"healthcheckPath"`
	HealthcheckTimeout      *int    `json:"healthcheckTimeout" toml:"healthcheckTimeout"`
	RestartPolicyType       *string `json:"restartPolicyType" toml:"restartPolicyType"`
	RestartPolicyMaxRetries *int    `json:"restartPolicyMaxRetries" toml:"restartPolicyMaxRetries"`
	NumReplicas             *int    `json:"numReplicas" toml:"numReplicas"`
	CronSchedule            *string `json:"cronSchedule" toml:"cronSchedule"`
	SleepApplication        *bool   `json:"sleepApplication" toml:"sleepApplication"`
	OverlapSeconds          *int    `json:"overlapSeconds" toml:"overlapSeconds"`
	DrainingSeconds         *int    `json:"drainingSeconds" toml:"drainingSeconds"`
	// Region은 노드 이름 또는 region=<값> 라벨이 있는 노드로 배치한다(볼륨이 없는 서비스만).
	Region *string `json:"region" toml:"region"`
	// MultiRegionConfig는 {"<region>": {"numReplicas": n}}이다. region이 하나일 때만 region/numReplicas로 쓴다.
	MultiRegionConfig map[string]*struct {
		NumReplicas *int `json:"numReplicas" toml:"numReplicas"`
	} `json:"multiRegionConfig" toml:"multiRegionConfig"`
	RequiredMountPath *string `json:"requiredMountPath" toml:"requiredMountPath"`
}

// Override는 environments.<이름> 블록이다.
type Override struct {
	Build  *Build  `json:"build" toml:"build"`
	Deploy *Deploy `json:"deploy" toml:"deploy"`
}

type File struct {
	Build        Build               `json:"build" toml:"build"`
	Deploy       Deploy              `json:"deploy" toml:"deploy"`
	Environments map[string]Override `json:"environments" toml:"environments"`
}

// Find는 dir에서 설정 파일을 찾는다. 없으면 ("", nil, nil).
func Find(dir string) (string, *File, error) {
	for _, name := range Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return name, nil, err
		}
		f, err := Parse(name, b)
		return name, f, err
	}
	return "", nil, nil
}

func Parse(name string, b []byte) (*File, error) {
	var f File
	var err error
	if strings.HasSuffix(name, ".toml") {
		_, err = toml.Decode(string(b), &f)
	} else {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &f, nil
}

// ForEnvironment는 환경 envName(preview면 PR 미리보기 환경)에 맞는 environments 블록을 최상위 값 위에 합친 설정이다.
// 쓴 블록 이름도 돌려준다(없으면 ""). f는 바꾸지 않는다.
func (f *File) ForEnvironment(envName string, preview bool) (*File, string) {
	out := *f
	out.Environments = nil
	key := ""
	if _, ok := f.Environments[envName]; ok && envName != "" {
		key = envName
	} else if _, ok := f.Environments[PREnvironment]; ok && preview {
		key = PREnvironment
	}
	if key == "" {
		return &out, ""
	}
	ov := f.Environments[key]
	if ov.Build != nil {
		mergeInto(&out.Build, ov.Build)
	}
	if ov.Deploy != nil {
		mergeInto(&out.Deploy, ov.Deploy)
	}
	return &out, key
}

// mergeInto는 src에서 값이 있는(nil이 아닌) 필드만 dst에 덮어쓴다. dst와 src는 같은 구조체 포인터다.
func mergeInto(dst, src any) {
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for i := 0; i < sv.NumField(); i++ {
		f := sv.Field(i)
		switch f.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			if !f.IsNil() {
				dv.Field(i).Set(f)
			}
		default:
			if !f.IsZero() {
				dv.Field(i).Set(f)
			}
		}
	}
}

// Region은 배치할 region 이름이다: deploy.region, 없으면 multiRegionConfig의 region이 하나일 때 그 이름.
// region이 여러 개면 ""와 경고를 돌려준다(여러 노드에 나눠 배치하는 기능은 없다).
func (f *File) Region() (string, string) {
	if r := f.Deploy.Region; r != nil && strings.TrimSpace(*r) != "" {
		return strings.TrimSpace(*r), ""
	}
	if n := len(f.Deploy.MultiRegionConfig); n == 1 {
		for k := range f.Deploy.MultiRegionConfig {
			return strings.TrimSpace(k), ""
		}
	}
	// region이 여러 개면 Regions가 placement로 다룬다.
	return "", ""
}

// RegionReplicas는 multiRegionConfig의 region 하나와 그 region의 replica 수다.
type RegionReplicas struct {
	Region   string
	Replicas int
}

// Regions는 deploy.multiRegionConfig에 region이 둘 이상일 때 이름순 목록이다(numReplicas가 없으면 1).
// deploy.region이 있으면 그 하나만 쓰므로 nil이다. 노드 배치(placement)는 호출자가 노드를 찾아 정한다.
func (f *File) Regions() ([]RegionReplicas, error) {
	if r := f.Deploy.Region; r != nil && strings.TrimSpace(*r) != "" {
		return nil, nil
	}
	if len(f.Deploy.MultiRegionConfig) < 2 {
		return nil, nil
	}
	var out []RegionReplicas
	total := 0
	for k, rc := range f.Deploy.MultiRegionConfig {
		n := 1
		if rc != nil && rc.NumReplicas != nil {
			n = *rc.NumReplicas
		}
		if n < 1 || n > 10 {
			return nil, fmt.Errorf("deploy.multiRegionConfig.%s.numReplicas: must be 1..10", k)
		}
		total += n
		out = append(out, RegionReplicas{Region: strings.TrimSpace(k), Replicas: n})
	}
	if total > 10 {
		return nil, fmt.Errorf("deploy.multiRegionConfig: replica 합계(%d)가 10을 넘습니다", total)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out, nil
}

// Warnings는 받아들였지만 적용하지 않는 키에 대한 안내다(배포 로그에 남긴다).
func (f *File) Warnings() []string {
	var out []string
	if v := f.Build.RailpackVersion; v != nil && *v != "" {
		out = append(out, fmt.Sprintf("build.railpackVersion=%s: railpack 버전은 서버 설정(KEEL_RAILPACK_BIN / KEEL_RAILPACK_FRONTEND)을 따르므로 무시합니다", *v))
	}
	if _, w := f.Region(); w != "" {
		out = append(out, w)
	}
	return out
}

// Apply는 파일의 값으로 sp를 덮어쓴다. 바뀐 키 목록을 돌려준다.
// environments 블록은 보지 않는다(먼저 ForEnvironment로 합친다). deploy.region은 배치 문제라 호출자가 처리한다.
func (f *File) Apply(sp *store.Spec) ([]string, error) {
	var changed []string
	set := func(k string) { changed = append(changed, k) }
	if b := f.Build.Builder; b != nil {
		switch strings.ToUpper(*b) {
		case "DOCKERFILE":
			sp.Builder = "dockerfile"
		case "RAILPACK", "NIXPACKS", "HEROKU", "PAKETO":
			sp.Builder = "railpack"
		case "AUTO", "":
			sp.Builder = "auto"
		default:
			return nil, fmt.Errorf("build.builder: unknown builder %q", *b)
		}
		set("build.builder")
	}
	if p := f.Build.DockerfilePath; p != nil && *p != "" {
		sp.DockerfilePath = *p
		set("build.dockerfilePath")
	}
	if f.Build.WatchPatterns != nil {
		sp.WatchPaths = f.Build.WatchPatterns
		set("build.watchPatterns")
	}
	if c := f.Build.BuildCommand; c != nil {
		v := strings.TrimSpace(*c)
		if strings.ContainsAny(v, "\x00\n\r") {
			return nil, errors.New("build.buildCommand: must be a single line")
		}
		sp.BuildCommand = &v
		if v == "" {
			sp.BuildCommand = nil
		}
		set("build.buildCommand")
	}
	d := &f.Deploy
	if d.StartCommand != nil {
		v := *d.StartCommand
		sp.StartCommand = &v
		if v == "" {
			sp.StartCommand = nil
		}
		set("deploy.startCommand")
	}
	switch v := d.PreDeployCommand.(type) {
	case nil:
	case string:
		sp.PreDeployCommand = &v
		if v == "" {
			sp.PreDeployCommand = nil
		}
		set("deploy.preDeployCommand")
	case []any:
		var parts []string
		for _, p := range v {
			s, ok := p.(string)
			if !ok {
				return nil, errors.New("deploy.preDeployCommand: must be a string or string array")
			}
			parts = append(parts, s)
		}
		cmd := strings.Join(parts, " && ")
		sp.PreDeployCommand = &cmd
		set("deploy.preDeployCommand")
	default:
		return nil, errors.New("deploy.preDeployCommand: must be a string or string array")
	}
	if d.HealthcheckPath != nil {
		sp.HealthcheckPath = *d.HealthcheckPath
		if sp.HealthcheckPath == "" {
			sp.HealthcheckPath = "/"
		}
		sp.HealthcheckType = "http"
		set("deploy.healthcheckPath")
	}
	if d.HealthcheckTimeout != nil {
		if *d.HealthcheckTimeout < 1 || *d.HealthcheckTimeout > 3600 {
			return nil, errors.New("deploy.healthcheckTimeout: must be 1..3600")
		}
		sp.HealthcheckTimeoutS = *d.HealthcheckTimeout
		set("deploy.healthcheckTimeout")
	}
	if p := d.RestartPolicyType; p != nil {
		switch strings.ToUpper(*p) {
		case "ALWAYS":
			sp.RestartPolicy = "always"
		case "ON_FAILURE":
			sp.RestartPolicy = "on_failure"
		case "NEVER":
			sp.RestartPolicy = "never"
		default:
			return nil, fmt.Errorf("deploy.restartPolicyType: unknown policy %q", *p)
		}
		set("deploy.restartPolicyType")
	}
	if n := d.RestartPolicyMaxRetries; n != nil {
		sp.RestartMaxRetries = *n
		set("deploy.restartPolicyMaxRetries")
	}
	replicas := d.NumReplicas
	if replicas == nil && len(d.MultiRegionConfig) == 1 && (d.Region == nil || strings.TrimSpace(*d.Region) == "") {
		for _, rc := range d.MultiRegionConfig {
			if rc != nil {
				replicas = rc.NumReplicas
			}
		}
	}
	if n := replicas; n != nil {
		if *n < 1 || *n > 10 {
			return nil, errors.New("deploy.numReplicas: must be 1..10")
		}
		sp.Replicas = *n
		set("deploy.numReplicas")
	}
	if c := d.CronSchedule; c != nil {
		v := strings.TrimSpace(*c)
		sp.CronSchedule = &v
		if v == "" {
			sp.CronSchedule = nil
		}
		set("deploy.cronSchedule")
	}
	if s := d.SleepApplication; s != nil {
		sp.SleepWhenIdle = *s
		set("deploy.sleepApplication")
	}
	if n := d.OverlapSeconds; n != nil {
		if *n < 0 || *n > MaxOverlapSeconds {
			return nil, fmt.Errorf("deploy.overlapSeconds: must be 0..%d", MaxOverlapSeconds)
		}
		sp.OverlapSeconds = *n
		set("deploy.overlapSeconds")
	}
	if n := d.DrainingSeconds; n != nil {
		if *n < 0 || *n > MaxDrainingSeconds {
			return nil, fmt.Errorf("deploy.drainingSeconds: must be 0..%d", MaxDrainingSeconds)
		}
		v := *n
		sp.DrainingSeconds = &v
		set("deploy.drainingSeconds")
	}
	if p := d.RequiredMountPath; p != nil {
		v := strings.TrimSpace(*p)
		if v != "" {
			if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\x00\n") {
				return nil, errors.New("deploy.requiredMountPath: must be an absolute path")
			}
			v = path.Clean(v)
		}
		sp.RequiredMountPath = v
		set("deploy.requiredMountPath")
	}
	return changed, nil
}
