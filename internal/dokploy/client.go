// Package dokploy는 Dokploy 설치를 읽어(GET만) aven로 옮길 계획을 만든다.
// 클라이언트는 Dokploy REST API(x-api-key)만 쓴다. Dokploy DB의 env는 `enc:v1:`로 암호화되어 있어
// 복호화된 값은 API로만 얻을 수 있기 때문이다. 이 패키지는 Dokploy에 아무것도 쓰지 않는다.
package dokploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Domain은 Dokploy 도메인 행이다(application 또는 compose 소속).
type Domain struct {
	Host            string `json:"host"`
	Port            *int   `json:"port"`
	HTTPS           bool   `json:"https"`
	Path            string `json:"path"`
	ServiceName     string `json:"serviceName"`
	CertificateType string `json:"certificateType"`
	StripPath       bool   `json:"stripPath"`
	InternalPath    string `json:"internalPath"`
	Middlewares     any    `json:"middlewares"`
	ForwardAuth     bool   `json:"forwardAuthEnabled"`
	Enabled         *bool  `json:"enabled"`
}

// Mount는 volume | bind | file 마운트다. file이면 Content가 파일 내용이다.
type Mount struct {
	Type       string `json:"type"`
	VolumeName string `json:"volumeName"`
	HostPath   string `json:"hostPath"`
	FilePath   string `json:"filePath"`
	Content    string `json:"content"`
	MountPath  string `json:"mountPath"`
}

type Application struct {
	ID              string   `json:"applicationId"`
	Name            string   `json:"name"`
	AppName         string   `json:"appName"`
	Env             string   `json:"env"`
	BuildArgs       string   `json:"buildArgs"`
	BuildSecrets    string   `json:"buildSecrets"`
	WatchPaths      []string `json:"watchPaths"`
	MemoryLimit     any      `json:"memoryLimit"`
	CPULimit        any      `json:"cpuLimit"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	SourceType      string   `json:"sourceType"` // github | git | docker | drop | gitlab | gitea | bitbucket
	Repository      string   `json:"repository"`
	Owner           string   `json:"owner"`
	Branch          string   `json:"branch"`
	BuildPath       string   `json:"buildPath"`
	AutoDeploy      bool     `json:"autoDeploy"`
	CustomGitURL    string   `json:"customGitUrl"`
	CustomGitBranch string   `json:"customGitBranch"`
	CustomGitPath   string   `json:"customGitBuildPath"`
	CustomGitSSHKey *string  `json:"customGitSSHKeyId"`
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	DockerImage     string   `json:"dockerImage"`
	RegistryURL     string   `json:"registryUrl"`
	Dockerfile      string   `json:"dockerfile"`
	DockerContext   string   `json:"dockerContextPath"`
	DockerStage     string   `json:"dockerBuildStage"`
	Replicas        int      `json:"replicas"`
	Status          string   `json:"applicationStatus"`
	BuildType       string   `json:"buildType"`
	ServerID        *string  `json:"serverId"`
	BuildServerID   *string  `json:"buildServerId"`
	Domains         []Domain `json:"domains"`
	Mounts          []Mount  `json:"mounts"`
	Redirects       []any    `json:"redirects"`
	Security        []any    `json:"security"`
	Ports           []any    `json:"ports"`
}

type Compose struct {
	ID          string   `json:"composeId"`
	Name        string   `json:"name"`
	AppName     string   `json:"appName"`
	Env         string   `json:"env"`
	ComposeFile string   `json:"composeFile"`
	SourceType  string   `json:"sourceType"`  // raw | github | git ...
	ComposeType string   `json:"composeType"` // docker-compose | stack
	Status      string   `json:"composeStatus"`
	Isolated    bool     `json:"isolatedDeployment"`
	ServerID    *string  `json:"serverId"`
	Domains     []Domain `json:"domains"`
	Mounts      []Mount  `json:"mounts"`
}

// Database는 postgres/redis/mysql/mariadb/mongo 공통 필드다.
type Database struct {
	Kind         string  `json:"-"`
	ID           string  `json:"-"`
	Name         string  `json:"name"`
	AppName      string  `json:"appName"`
	DatabaseName string  `json:"databaseName"`
	DatabaseUser string  `json:"databaseUser"`
	Password     string  `json:"databasePassword"`
	RootPassword string  `json:"databaseRootPassword"`
	DockerImage  string  `json:"dockerImage"`
	Command      string  `json:"command"`
	Args         any     `json:"args"`
	Env          string  `json:"env"`
	ExternalPort *int    `json:"externalPort"`
	Status       string  `json:"applicationStatus"`
	ServerID     *string `json:"serverId"`
	Mounts       []Mount `json:"mounts"`
	Backups      []any   `json:"backups"`
}

type Server struct {
	ID        string `json:"serverId"`
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress"`
	Type      string `json:"serverType"` // deploy | build
}

type Environment struct {
	ID           string
	Name         string
	Env          string
	Applications []*Application
	Composes     []*Compose
	Databases    []*Database
}

type Project struct {
	ID           string
	Name         string
	Env          string
	Environments []*Environment
}

// Snapshot은 Dokploy 설치 전체다. 비밀 값을 담고 있으므로 디스크에 쓰지 않는다.
type Snapshot struct {
	Version  string
	Projects []*Project
	Servers  []*Server
}

// Client는 Dokploy REST API를 읽는다.
type Client struct {
	URL  string // 예: http://127.0.0.1:3000
	Key  string
	HTTP *http.Client
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(c.URL, "/") + "/api/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.Key)
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("dokploy %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("dokploy %s: %w", path, err)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("dokploy %s: %d (check DOKPLOY_API_KEY)", path, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(body, &e)
		return fmt.Errorf("dokploy %s: %d %s", path, resp.StatusCode, e.Message)
	}
	return json.Unmarshal(body, out)
}

type listProject struct {
	ProjectID    string `json:"projectId"`
	Name         string `json:"name"`
	Env          string `json:"env"`
	Environments []struct {
		ID           string `json:"environmentId"`
		Name         string `json:"name"`
		Env          string `json:"env"`
		Applications []struct {
			ID string `json:"applicationId"`
		} `json:"applications"`
		Compose []struct {
			ID string `json:"composeId"`
		} `json:"compose"`
		Postgres []struct {
			ID string `json:"postgresId"`
		} `json:"postgres"`
		Redis []struct {
			ID string `json:"redisId"`
		} `json:"redis"`
		MySQL []struct {
			ID string `json:"mysqlId"`
		} `json:"mysql"`
		MariaDB []struct {
			ID string `json:"mariadbId"`
		} `json:"mariadb"`
		Mongo []struct {
			ID string `json:"mongoId"`
		} `json:"mongo"`
	} `json:"environments"`
}

// Fetch는 프로젝트, 서비스 상세, 서버 목록을 읽는다.
func (c *Client) Fetch(ctx context.Context) (*Snapshot, error) {
	if c.URL == "" || c.Key == "" {
		return nil, errors.New("dokploy URL and API key are required")
	}
	var list []listProject
	if err := c.get(ctx, "project.all", nil, &list); err != nil {
		return nil, err
	}
	snap := &Snapshot{}
	var ver string
	if err := c.get(ctx, "settings.getDokployVersion", nil, &ver); err == nil {
		snap.Version = ver
	}
	if err := c.get(ctx, "server.all", nil, &snap.Servers); err != nil {
		return nil, err
	}
	for _, lp := range list {
		p := &Project{ID: lp.ProjectID, Name: lp.Name, Env: lp.Env}
		for _, le := range lp.Environments {
			e := &Environment{ID: le.ID, Name: le.Name, Env: le.Env}
			for _, a := range le.Applications {
				var app Application
				if err := c.get(ctx, "application.one", url.Values{"applicationId": {a.ID}}, &app); err != nil {
					return nil, err
				}
				e.Applications = append(e.Applications, &app)
			}
			for _, x := range le.Compose {
				var cp Compose
				if err := c.get(ctx, "compose.one", url.Values{"composeId": {x.ID}}, &cp); err != nil {
					return nil, err
				}
				e.Composes = append(e.Composes, &cp)
			}
			dbs := []struct {
				kind, param string
				ids         []string
			}{
				{"postgres", "postgresId", idsOf(le.Postgres)},
				{"redis", "redisId", idsOf(le.Redis)},
				{"mysql", "mysqlId", idsOf(le.MySQL)},
				{"mariadb", "mariadbId", idsOf(le.MariaDB)},
				{"mongo", "mongoId", idsOf(le.Mongo)},
			}
			for _, d := range dbs {
				for _, id := range d.ids {
					var db Database
					if err := c.get(ctx, d.kind+".one", url.Values{d.param: {id}}, &db); err != nil {
						return nil, err
					}
					db.Kind, db.ID = d.kind, id
					e.Databases = append(e.Databases, &db)
				}
			}
			p.Environments = append(p.Environments, e)
		}
		snap.Projects = append(snap.Projects, p)
	}
	return snap, nil
}

func idsOf[T any](xs []T) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		b, _ := json.Marshal(x)
		var m map[string]string
		json.Unmarshal(b, &m)
		for _, v := range m {
			out = append(out, v)
		}
	}
	return out
}
