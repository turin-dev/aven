package agentprotocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/compat"
)

// 시점 복구(PITR): Postgres WAL / MySQL binlog를 계속 백업 저장소로 보관하고 원하는 시각으로 복원한다.
//
// DBToolRequest.Action으로 고르는 helper 스크립트(서버는 스크립트를 보내지 못하고 이 표에서 고른다):
//
//   - pitr-base(db.backup): 기준 백업. postgres는 pg_basebackup -Ft -z(tar.gz, WAL 없이), mysql은 mysqldump
//     --source-data=2(binlog 위치 주석 포함, 서버가 gzip).
//   - pitr-wal(db.backup, postgres): DB 데이터 볼륨(읽기 전용 마운트)의 <mount>/keel-pitr/wal에 archive_command가
//     복사해 둔 WAL 파일을 PITR 프레임으로 내보낸다. 서버는 업로드를 확인한 뒤에만 파일을 지운다.
//   - pitr-binlog(db.backup, mysql): 닫힌 binlog(Files)를 mysqlbinlog --read-from-remote-server --raw로 받아 프레임으로 내보낸다.
//   - pitr-restore(upload db.restore): postgres는 멈춘 대상의 데이터 볼륨에 기준 백업과 WAL을 풀고 recovery 설정을 쓴다
//     (stdin = tar: base.tar.gz, wal/<name>.gz). mysql은 대상 DB에 덤프를 넣고 binlog를 목표 시각까지 재생한다
//     (stdin = PITR 프레임: base.sql.gz, <binlog>.gz...; helper가 풀어서 mysqlbinlog에 넘긴다).
//
// PITR 프레임: "KEELPITR <name> <size>\n" 뒤에 정확히 size 바이트. 이름은 PITRFileRe를 따른다.
const (
	PITRBase    = "pitr-base"
	PITRWAL     = "pitr-wal"
	PITRBinlog  = "pitr-binlog"
	PITRRestore = "pitr-restore"
)

// PITRArchiveDir는 postgres 데이터 볼륨 안에서 archive_command가 WAL을 복사해 두는 디렉터리다(볼륨 마운트 기준).
const (
	PITRArchiveDir       = "aven-pitr/wal"
	LegacyPITRArchiveDir = "keel-pitr/wal"
	pitrMagic            = "AVENPITR"
	legacyPITRMagic      = "KEELPITR"
)

// PITRMaxFiles는 helper 한 번이 내보내는 최대 파일 수다(밀린 파일은 다음 주기에 이어서 보낸다).
const PITRMaxFiles = 64

var (
	// PITRFileRe는 프레임/요청에 들어가는 파일 이름이다(경로 구분자, 앞 점 금지).
	PITRFileRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)
	// PGWALFileRe는 archive 디렉터리의 WAL 세그먼트, timeline history, backup history 파일 이름이다.
	PGWALFileRe = regexp.MustCompile(`^[0-9A-F]{8}([0-9A-F]{16}(\.partial|\.[0-9A-F]{8}\.backup)?|\.history)$`)
	// pitrMountRe는 볼륨 마운트 경로와 PGDATA 경로다(셸 스크립트와 postgres 설정에 그대로 들어간다).
	pitrMountRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)
)

// PITRSupported는 시점 복구를 지원하는 DB 종류인지 알려준다(postgres, mysql. mariadb는 binlog 형식·도구가 달라 제외).
func PITRSupported(kind string) bool { return kind == "postgres" || kind == "mysql" }

// ValidPITRPath는 PITR 스크립트에 넣을 수 있는 절대 경로인지 알려준다.
func ValidPITRPath(p string) bool {
	return pitrMountRe.MatchString(p) && p != "/" && !strings.Contains(p, "..") && !strings.Contains(p, "//") && path.Clean(p) == p
}

// PITRDataDirOK는 PGDATA가 볼륨 마운트의 하위 디렉터리인지 알려준다(archive 디렉터리를 PGDATA 밖, 같은 볼륨에 둔다).
func PITRDataDirOK(mount, dataDir string) bool {
	return ValidPITRPath(mount) && ValidPITRPath(dataDir) && strings.HasPrefix(dataDir, mount+"/") &&
		!strings.HasPrefix(dataDir, mount+"/keel-pitr") && !strings.HasPrefix(dataDir, mount+"/aven-pitr")
}

// PITRTimeLayout은 recovery_target_time / mysqlbinlog --stop-datetime에 넘기는 UTC 시각 형식이다.
const PITRTimeLayout = "2006-01-02 15:04:05"

// DBPITRBaseTool은 시점 복구의 기준 백업 도구다(stdout).
func DBPITRBaseTool(kind, host string, port int, c DBCreds) (*DBTool, error) {
	port = DBPort(kind, port)
	env := map[string]string{"DBHOST": host, "DBPORT": fmt.Sprint(port), "DBUSER": c.User}
	switch kind {
	case "postgres":
		env["PGPASSWORD"] = c.Password
		// -X none: 필요한 WAL은 archive에서 가져온다. -c fast: 바로 checkpoint.
		return &DBTool{Env: env, Ext: "tar.gz", Script: `exec pg_basebackup -h "$DBHOST" -p "$DBPORT" -U "$DBUSER" -D - -Ft -z -X none -c fast --no-password`}, nil
	case "mysql":
		env["MYSQL_PWD"] = c.Password
		return &DBTool{Env: env, Ext: "sql.gz", Gzip: true, Script: mysqlPrelude + `set -e
q() { "$MYSQL" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER" -N -B -e "$1"; }
DBS=$(q 'SHOW DATABASES' | grep -Ev '^(information_schema|performance_schema|mysql|sys)$' || true)
if [ -z "$DBS" ]; then
  # 사용자 DB가 없으면 binlog 위치만 남긴다.
  st=$(q 'SHOW BINARY LOG STATUS' 2>/dev/null || q 'SHOW MASTER STATUS')
  set -- $st
  [ -n "$1" ] || { echo "binary logging is off" >&2; exit 3; }
  printf -- "-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='%s', SOURCE_LOG_POS=%s;\n" "$1" "$2"
  exit 0
fi
exec "$DUMP" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER" --single-transaction --source-data=2 --set-gtid-purged=OFF --add-drop-database --routines --triggers --events --hex-blob --databases $DBS`}, nil
	}
	return nil, fmt.Errorf("point-in-time recovery is not supported for %q", kind)
}

// pitrFrameEmit은 현재 디렉터리의 파일 하나를 PITR 프레임으로 내보내는 셸 함수(emit <name>)다.
const pitrFrameEmit = `emit() { printf 'AVENPITR %s %s\n' "$1" "$(wc -c < "$1" | tr -d ' ')"; cat "$1"; }
`

// DBPITRWALTool은 postgres archive 디렉터리의 WAL 파일을 프레임으로 내보낸다(데이터 볼륨을 mount에 붙인다).
func DBPITRWALTool(mount string) (*DBTool, error) {
	if !ValidPITRPath(mount) {
		return nil, errors.New("invalid volume mount path")
	}
	return &DBTool{Env: map[string]string{"PITR_DIR": mount + "/" + PITRArchiveDir, "PITR_MAX": fmt.Sprint(PITRMaxFiles)}, Ext: "frames",
		Script: `set -e
base="${PITR_DIR%/aven-pitr/wal}"
base="${base%/keel-pitr/wal}"
cd "$base/aven-pitr/wal" 2>/dev/null || cd "$base/keel-pitr/wal" 2>/dev/null || exit 0
` + pitrFrameEmit + `ls -1 | grep -E '^[0-9A-F]{8}([0-9A-F]{16}(\.partial|\.[0-9A-F]{8}\.backup)?|\.history)$' | sort | head -n "$PITR_MAX" > /tmp/aven-pitr-list || true
while IFS= read -r f; do emit "$f"; done < /tmp/aven-pitr-list`}, nil
}

// DBPITRBinlogTool은 닫힌 binlog 파일들을 원격 읽기(--raw)로 받아 프레임으로 내보낸다.
func DBPITRBinlogTool(host string, port int, c DBCreds, files []string) (*DBTool, error) {
	if len(files) == 0 || len(files) > PITRMaxFiles {
		return nil, fmt.Errorf("1..%d binlog files are required", PITRMaxFiles)
	}
	for _, f := range files {
		if !PITRFileRe.MatchString(f) {
			return nil, fmt.Errorf("invalid binlog name %q", f)
		}
	}
	env := map[string]string{"DBHOST": host, "DBPORT": fmt.Sprint(DBPort("mysql", port)), "DBUSER": c.User, "MYSQL_PWD": c.Password,
		"PITR_FILES": strings.Join(files, " ")}
	return &DBTool{Env: env, Ext: "frames", Script: `set -e
BINLOG=$(command -v mysqlbinlog || command -v mariadb-binlog) || { echo "mysqlbinlog not found in image" >&2; exit 127; }
mkdir -p /tmp/aven-pitr && cd /tmp/aven-pitr
` + pitrFrameEmit + `for f in $PITR_FILES; do
  "$BINLOG" --read-from-remote-server --host="$DBHOST" --port="$DBPORT" --user="$DBUSER" --raw --result-file=/tmp/aven-pitr/ "$f" >&2
  emit "$f"
  rm -f "$f"
done`}, nil
}

// PITRRestoreOpts는 pitr-restore의 대상 정보다.
type PITRRestoreOpts struct {
	Target time.Time
	// postgres
	Mount, DataDir string
	KeepArchive    bool // 같은 서비스로 복원: 복원한 DB가 계속 WAL을 보관한다(아니면 archive_mode=off)
	// mysql
	BaseFile     string
	BasePosition uint64
}

// DBPITRRestoreTool은 pitr-restore 도구다.
func DBPITRRestoreTool(kind, host string, port int, c DBCreds, o PITRRestoreOpts) (*DBTool, error) {
	target := o.Target.UTC().Format(PITRTimeLayout)
	switch kind {
	case "postgres":
		if !PITRDataDirOK(o.Mount, o.DataDir) {
			return nil, errors.New("PGDATA must be a subdirectory of the database volume")
		}
		keep := "off"
		if o.KeepArchive {
			keep = "on"
		}
		return &DBTool{DataDir: true, Env: map[string]string{"PITR_MOUNT": o.Mount, "PITR_DATA": o.DataDir, "PITR_TARGET": target + "+00", "PITR_ARCHIVE": keep},
			Script: `set -e
if [ -d "$PITR_MOUNT/keel-pitr" ]; then PITR_ROOT="$PITR_MOUNT/keel-pitr"; else PITR_ROOT="$PITR_MOUNT/aven-pitr"; fi
R="$PITR_ROOT/restore"
rm -rf "$R" && mkdir -p "$R"
tar -C "$R" -xf -
[ -s "$R/base.tar.gz" ] || { echo "base backup is missing from the restore stream" >&2; exit 2; }
rm -rf "$PITR_DATA" && mkdir -p "$PITR_DATA"
tar -C "$PITR_DATA" -xzf "$R/base.tar.gz"
rm -f "$R/base.tar.gz" "$PITR_DATA/recovery.signal" "$PITR_DATA/standby.signal" "$PITR_DATA/postmaster.pid"
[ -f "$PITR_DATA/PG_VERSION" ] || { echo "base backup is not a PostgreSQL data directory" >&2; exit 2; }
rm -rf "$PITR_ROOT/wal" && mkdir -p "$PITR_ROOT/wal"
{
  echo "# aven point-in-time recovery"
  echo "restore_command = 'gunzip -c $R/wal/%f.gz > %p'"
  echo "recovery_target_time = '$PITR_TARGET'"
  echo "recovery_target_action = 'promote'"
  echo "recovery_end_command = 'rm -rf $R'"
  [ "$PITR_ARCHIVE" = on ] || echo "archive_mode = 'off'"
} >> "$PITR_DATA/postgresql.auto.conf"
touch "$PITR_DATA/recovery.signal"
chown -R postgres:postgres "$PITR_ROOT" "$PITR_DATA"
chmod 700 "$PITR_DATA"
echo "restored base backup; recovery to $PITR_TARGET starts on the next boot" >&2`}, nil
	case "mysql":
		if !PITRFileRe.MatchString(o.BaseFile) || o.BasePosition == 0 {
			return nil, errors.New("invalid base binlog position")
		}
		env := map[string]string{"DBHOST": host, "DBPORT": fmt.Sprint(DBPort(kind, port)), "DBUSER": c.User, "MYSQL_PWD": c.Password,
			"PITR_TARGET": target, "PITR_BASE_FILE": o.BaseFile, "PITR_BASE_POSITION": fmt.Sprint(o.BasePosition), "TZ": "UTC"}
		return &DBTool{Env: env, Script: mysqlPrelude + `set -e
BINLOG=$(command -v mysqlbinlog || command -v mariadb-binlog) || { echo "mysqlbinlog not found in image" >&2; exit 127; }
W=/tmp/aven-pitr && rm -rf "$W" && mkdir -p "$W" && cd "$W"
: > .list
while IFS=' ' read -r tag name size; do
  [ "$tag" = AVENPITR ] || [ "$tag" = KEELPITR ] || { echo "bad restore stream" >&2; exit 5; }
  case "$name" in ''|.*|*/*) echo "bad file name" >&2; exit 5 ;; esac
  head -c "$size" > "$name"
  [ "$(wc -c < "$name" | tr -d ' ')" = "$size" ] || { echo "truncated restore stream ($name)" >&2; exit 5; }
  [ "$name" = base.sql.gz ] && continue
  case "$name" in *.gz) gunzip -f "$name"; name=${name%.gz} ;; esac
  printf '%s\n' "$name" >> .list
done
[ -s base.sql.gz ] || { echo "base backup is missing from the restore stream" >&2; exit 2; }
[ "$(head -n 1 .list)" = "$PITR_BASE_FILE" ] || { echo "binlog $PITR_BASE_FILE is missing from the archive" >&2; exit 4; }
M() { "$MYSQL" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER" "$@"; }
i=0
until M -e 'SELECT 1' >/dev/null 2>&1; do i=$((i+1)); [ $i -lt 120 ] || { echo "database is not reachable" >&2; exit 6; }; sleep 1; done
for db in $(M -N -B -e 'SHOW DATABASES' | grep -Ev '^(information_schema|performance_schema|mysql|sys)$'); do
  M --init-command='SET SESSION sql_log_bin=0' -e "DROP DATABASE ` + "`$db`" + `"
done
gunzip -c base.sql.gz | M --init-command='SET SESSION sql_log_bin=0'
"$BINLOG" --start-position="$PITR_BASE_POSITION" --stop-datetime="$PITR_TARGET" $(cat .list) | M --init-command='SET SESSION sql_log_bin=0'
echo "restored to $PITR_TARGET UTC" >&2`}, nil
	}
	return nil, fmt.Errorf("point-in-time recovery is not supported for %q", kind)
}

// ---------------- 프레임 ----------------

// PITRFrame은 프레임 하나의 머리다.
type PITRFrame struct {
	Name string
	Size int64
}

// MaxPITRFrame은 파일 하나의 최대 크기다(WAL 세그먼트는 기본 16MiB, binlog는 max_binlog_size 기본 1GiB).
const MaxPITRFrame = 4 << 30

// ReadPITRFrames는 r의 프레임을 차례로 읽어 f에 넘긴다. f는 body를 정확히 Size 바이트 읽어야 한다(덜 읽으면 나머지를 버린다).
func ReadPITRFrames(r io.Reader, f func(PITRFrame, io.Reader) error) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF && line == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pitr frame header: %w", err)
		}
		fr, err := ParsePITRFrameHeader(line)
		if err != nil {
			return err
		}
		body := &countReader{r: io.LimitReader(br, fr.Size)}
		if err := f(fr, body); err != nil {
			return err
		}
		if n, err := io.Copy(io.Discard, body); err != nil {
			return err
		} else if n > 0 {
			return fmt.Errorf("pitr frame %s was not fully consumed", fr.Name)
		}
		if body.n != fr.Size {
			return fmt.Errorf("pitr frame %s is truncated (%d of %d bytes): %w", fr.Name, body.n, fr.Size, io.ErrUnexpectedEOF)
		}
	}
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ParsePITRFrameHeader는 "KEELPITR <name> <size>\n"을 읽는다.
func ParsePITRFrameHeader(line string) (PITRFrame, error) {
	parts := strings.Fields(line)
	if len(parts) != 3 || (parts[0] != pitrMagic && parts[0] != legacyPITRMagic) || !strings.HasSuffix(line, "\n") {
		return PITRFrame{}, fmt.Errorf("bad pitr frame header %q", strings.TrimSpace(line))
	}
	if parts[0] == legacyPITRMagic {
		compat.NoteLegacyResource("pitr-magic", "frame "+legacyPITRMagic, "frame "+pitrMagic)
	}
	if !PITRFileRe.MatchString(parts[1]) {
		return PITRFrame{}, fmt.Errorf("bad pitr file name %q", parts[1])
	}
	n, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || n < 0 || n > MaxPITRFrame {
		return PITRFrame{}, fmt.Errorf("bad pitr frame size %q", parts[2])
	}
	return PITRFrame{Name: parts[1], Size: n}, nil
}

// WritePITRFrame은 프레임 하나를 쓴다(body는 정확히 size 바이트여야 한다).
func WritePITRFrame(w io.Writer, name string, size int64, body io.Reader) error {
	if !PITRFileRe.MatchString(name) {
		return fmt.Errorf("bad pitr file name %q", name)
	}
	if _, err := fmt.Fprintf(w, pitrMagic+" %s %d\n", name, size); err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(body, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("pitr file %s: wrote %d of %d bytes", name, n, size)
	}
	return nil
}

var binlogPosRe = regexp.MustCompile(`(?:SOURCE|MASTER)_LOG_FILE='([A-Za-z0-9._-]+)',\s*(?:SOURCE|MASTER)_LOG_POS=(\d+)`)

// ParseBinlogPosition은 mysqldump --source-data=2 출력에서 binlog 위치를 찾는다.
func ParseBinlogPosition(head []byte) (string, uint64, bool) {
	m := binlogPosRe.FindSubmatch(head)
	if m == nil {
		return "", 0, false
	}
	pos, err := strconv.ParseUint(string(m[2]), 10, 64)
	if err != nil || pos == 0 {
		return "", 0, false
	}
	return string(m[1]), pos, true
}
