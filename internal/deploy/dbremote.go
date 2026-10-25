package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

// ErrAgentNoDBBackup은 원격 agent가 db.backup / db.restore를 모를 때다(옛 agent).
var ErrAgentNoDBBackup = errors.New("이 노드의 aven-agent가 원격 DB 백업/복원(db.backup)을 지원하지 않습니다. agent를 업그레이드하세요")

// ErrNodeOffline은 작업에 필요한 원격 노드(볼륨이나 데이터베이스가 있는 노드)가 연결돼 있지 않을 때다.
var ErrNodeOffline = errors.New("the node holding this data is offline; it must be connected for backups and restores")

// oldAgentDBErr는 옛 agent의 거부 응답을 ErrAgentNoDBBackup으로 바꾼다.
func oldAgentDBErr(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	if strings.Contains(s, `operation "`+agentprotocol.OpDBBackup+`" is not allowed`) ||
		strings.Contains(s, `upload kind "`+agentprotocol.UploadDBTool+`" is not allowed`) ||
		strings.Contains(s, `unknown field "db"`) {
		return ErrAgentNoDBBackup
	}
	return err
}

// chunkSink는 DataChunk stream을 순서대로 w에 쓰는 콜백을 만든다. total과 werr는 콜백이 채운다.
func chunkSink(what string, w io.Writer, total *int64, werr *error) func(json.RawMessage) {
	next := 0
	return func(b json.RawMessage) {
		if *werr != nil {
			return
		}
		var c agentprotocol.DataChunk
		if err := json.Unmarshal(b, &c); err != nil {
			*werr = err
			return
		}
		if c.Seq != next {
			*werr = fmt.Errorf("%s: chunk %d out of order (want %d)", what, c.Seq, next)
			return
		}
		next++
		n, err := w.Write(c.Data)
		*total += int64(n)
		*werr = err
	}
}

// dbBackup은 agent가 DB 컨테이너를 덤프해 보낸 chunk를 순서대로 w에 쓴다.
func (r *remoteDriver) dbBackup(ctx context.Context, req agentprotocol.DBToolRequest, w io.Writer) error {
	ctx, cancel := withLongDeadline(ctx, 6*time.Hour)
	defer cancel()
	var res agentprotocol.DBBackupResult
	var total int64
	var werr error
	err := r.stream(ctx, agentprotocol.OpDBBackup, req, &res, chunkSink("db backup", w, &total, &werr))
	if werr != nil {
		return werr
	}
	if err != nil {
		return oldAgentDBErr(err)
	}
	if res.Bytes != total {
		return fmt.Errorf("db backup incomplete: received %d bytes, expected %d", total, res.Bytes)
	}
	return nil
}

// dbRestore는 src를 agent에 올린 뒤 노드의 DB 컨테이너에 복원 도구로 넣는다.
func (r *remoteDriver) dbRestore(ctx context.Context, req agentprotocol.DBToolRequest, src io.Reader) error {
	return oldAgentDBErr(r.upload(ctx, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, src, "", nil))
}

// VolumeSizes는 노드의 aven 볼륨 크기다(metrics가 5분마다 부른다). 옛 agent는 오류를 돌려준다.
func (r *remoteDriver) VolumeSizes(ctx context.Context) (map[string]int64, error) {
	var res agentprotocol.VolumeSizesResult
	if err := r.call(ctx, agentprotocol.OpVolumeSizes, struct{}{}, &res); err != nil {
		return nil, err
	}
	if res.Sizes == nil {
		res.Sizes = map[string]int64{}
	}
	return res.Sizes, nil
}

// dbContainer는 DB 인스턴스가 nodeID에서 돌리는 현재 배포의 컨테이너다(없으면 "").
func dbContainer(d *store.Deployment, nodeID string) string {
	if d == nil {
		return ""
	}
	for _, g := range d.Groups() {
		if nodeKey(g.NodeID) == nodeKey(nodeID) && len(g.Containers) > 0 {
			return g.Containers[0]
		}
	}
	return ""
}

// dbToolRequest는 원격 노드의 agent에 보낼 덤프/복원 대상이다. 스크립트와 접속 주소는 agent가 정한다.
func (dt *dbTarget) toolRequest(job string, vol *store.Volume) (agentprotocol.DBToolRequest, error) {
	c := dbContainer(dt.cur, dt.node)
	if c == "" {
		return agentprotocol.DBToolRequest{}, fmt.Errorf("database has no container on node %s (deploy it first)", dt.node)
	}
	cr := dbCreds(dt.kind, dt.vars)
	req := agentprotocol.DBToolRequest{Kind: dt.kind, Container: c, Network: EnvNetwork(dt.t.Env.ID), Port: dt.port, Image: dt.image,
		Job: job, User: cr.User, Database: cr.Database, Password: cr.Password}
	if vol != nil {
		req.Volume, req.MountPath = vol.DockerName, vol.MountPath
	}
	return req, nil
}
