package api

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/selfhost"
)

// 서버 설정: aven-server 자신의 env 파일(KEEL_*)을 대시보드에서 보고 고친다(서버 owner 전용, docs/selfhost.md).
// 고친 값은 파일에만 쓰고, aven-server(self 서비스)를 다시 배포할 때 적용된다. 교체가 실패하면 파일도 되돌린다.

type serverConfigView struct {
	File     string                 `json:"file"`
	Writable bool                   `json:"writable"`
	Pending  int                    `json:"pending"` // 다음 배포에 적용될 키 수
	Entries  []selfhost.ConfigEntry `json:"entries"`
	Server   *selfInfo              `json:"server"`
}

func (s *Server) serverConfigFile() string {
	if s.Deploy == nil {
		return selfhost.DefaultEnvFile
	}
	return s.Deploy.SelfEnvFile()
}

func (s *Server) serverConfigView() (*serverConfigView, error) {
	f, err := selfhost.ReadEnvFile(s.serverConfigFile())
	if err != nil {
		return nil, err
	}
	v := &serverConfigView{File: f.Path, Writable: selfhost.Writable(f.Path), Entries: f.Entries(func(key string) (string, bool) {
		return compat.LookupEither(os.LookupEnv, key)
	}), Server: s.selfInfo()}
	for _, e := range v.Entries {
		if e.Pending {
			v.Pending++
		}
	}
	return v, nil
}

func (s *Server) getServerConfig(w http.ResponseWriter, r *http.Request) {
	v, err := s.serverConfigView()
	if err != nil {
		writeErr(w, 500, "서버 설정 파일을 읽지 못했습니다: "+err.Error())
		return
	}
	writeJSON(w, 200, v)
}

// putServerConfig는 env 파일을 고친다. set의 빈 값은 빈 값으로 저장한다(비밀값을 그대로 두려면 보내지 않는다).
func (s *Server) putServerConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Set   map[string]string `json:"set"`
		Unset []string          `json:"unset"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Set) == 0 && len(in.Unset) == 0 {
		writeErr(w, 400, "바꿀 설정이 없습니다")
		return
	}
	path := s.serverConfigFile()
	if !selfhost.Writable(path) {
		writeErr(w, 409, "이 서버는 "+path+"를 고칠 수 없습니다(읽기 전용). aven-server를 한 번 다시 배포하면 고칠 수 있습니다")
		return
	}
	f, err := selfhost.ReadEnvFile(path)
	if err != nil {
		writeErr(w, 500, "서버 설정 파일을 읽지 못했습니다: "+err.Error())
		return
	}
	// 처음 고칠 때는 지금 파일(지금 서버가 쓰는 설정)을 되돌릴 기준으로 남긴다.
	if dataDir := s.dataDir(); dataDir != "" && !selfhost.HasGoodEnv(dataDir) {
		if err := selfhost.SaveGoodEnv(dataDir, path); err != nil {
			s.Log.Warn("server config: save snapshot", "err", err)
		}
	}
	if err := f.Apply(in.Set, in.Unset); err != nil {
		if errors.Is(err, selfhost.ErrConfig) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if err := f.Write(); err != nil {
		writeErr(w, 500, "서버 설정 파일을 쓰지 못했습니다: "+err.Error())
		return
	}
	// 감사 기록에는 키 이름만 남긴다(값은 비밀일 수 있다).
	var keys []string
	for k := range in.Set {
		keys = append(keys, k)
	}
	for _, k := range in.Unset {
		keys = append(keys, "-"+k)
	}
	sort.Strings(keys)
	s.audit(r, "owner.server_config", "", "서버 설정을 고쳤습니다(다음 aven-server 배포에 적용): "+strings.Join(keys, ", "))
	v, err := s.serverConfigView()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
