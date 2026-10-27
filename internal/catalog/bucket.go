package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Bucket(S3 호환 객체 저장소): minio 템플릿과 같은 이미지를 database_kind "bucket"으로 쓴다.
// 비밀번호(SECRET_ACCESS_KEY) 하나만 만들고, access key와 bucket 이름은 그 비밀번호에서 HMAC으로 만든다
// (환경마다 비밀번호가 다르므로 셋 다 환경마다 다르다. 만든 값에서 비밀번호를 거꾸로 알 수는 없다).

const (
	BucketKind   = "bucket"
	BucketImage  = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z"
	BucketPort   = 9000
	BucketRegion = "us-east-1"
)

// BucketStart는 MinIO를 띄우고, 뜨면 기본 bucket($BUCKET)을 만든다(이미 있으면 그대로). 이미지에 든 mc를
// 컨테이너 안(127.0.0.1)에서 쓰므로 로컬/원격 노드 모두 같다. 자격 증명은 환경변수(MC_HOST_*)로만 넘긴다(argv에 없음).
// sh(PID 1)가 SIGTERM을 minio에 넘기고 minio의 종료 코드로 끝난다.
const BucketStart = `minio server /data --console-address :9001 & p=$!; trap 'kill -TERM $p' TERM INT; ` +
	`(i=0; until MC_CONFIG_DIR=/tmp/.mc MC_HOST_aven="http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@127.0.0.1:9000" mc mb --ignore-existing "aven/$BUCKET" >/dev/null 2>&1; ` +
	`do i=$((i+1)); if [ $i -ge 60 ]; then echo "aven: bucket $BUCKET를 만들지 못했습니다" >&2; break; fi; sleep 1; done) & ` +
	`wait $p; trap - TERM INT; wait $p`

// bucketDerive는 비밀번호에서 label 용도의 값을 만든다(hex).
func bucketDerive(pw, label string) string {
	m := hmac.New(sha256.New, []byte(pw))
	m.Write([]byte("aven-bucket:" + label))
	return hex.EncodeToString(m.Sum(nil))
}

// BucketAccessKey와 BucketName은 비밀번호에서 만든 access key ID와 bucket 이름이다.
func BucketAccessKey(pw string) string {
	return "AVEN" + strings.ToUpper(bucketDerive(pw, "access-key")[:16])
}
func BucketName(pw string) string { return "bucket-" + bucketDerive(pw, "name")[:10] }

// BucketVariables는 bucket 서비스의 변수다. PUBLIC_ENDPOINT는 공개 도메인이 있을 때만 의미가 있다
// (서비스를 만들 때 도메인이 없으면 뺀다).
func BucketVariables(pw string) map[string]string {
	return map[string]string{
		"BUCKET":                BucketName(pw),
		"ACCESS_KEY_ID":         BucketAccessKey(pw),
		"SECRET_ACCESS_KEY":     pw,
		"REGION":                BucketRegion,
		"ENDPOINT":              "http://${{AVEN_PRIVATE_DOMAIN}}:9000",
		"PUBLIC_ENDPOINT":       "https://${{AVEN_PUBLIC_DOMAIN}}",
		"MINIO_ROOT_USER":       "${{ACCESS_KEY_ID}}",
		"MINIO_ROOT_PASSWORD":   "${{SECRET_ACCESS_KEY}}",
		"MINIO_SITE_REGION":     "${{REGION}}",
		"AWS_ACCESS_KEY_ID":     "${{ACCESS_KEY_ID}}",
		"AWS_SECRET_ACCESS_KEY": "${{SECRET_ACCESS_KEY}}",
		"AWS_REGION":            "${{REGION}}",
		"AWS_DEFAULT_REGION":    "${{REGION}}",
		"AWS_ENDPOINT_URL":      "${{ENDPOINT}}",
		"AWS_S3_BUCKET_NAME":    "${{BUCKET}}",
	}
}

func init() {
	Databases[BucketKind] = DatabaseDef{
		Kind: BucketKind, Image: BucketImage, Port: BucketPort, DataPath: "/data", Start: BucketStart,
		HealthcheckPath: "/minio/health/live", Domain: true, Variables: BucketVariables,
	}
}
