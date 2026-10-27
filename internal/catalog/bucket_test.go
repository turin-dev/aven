package catalog

import (
	"regexp"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestBucketServiceDefinition(t *testing.T) {
	def, ok := DatabaseService(BucketKind)
	if !ok {
		t.Fatal("bucket kind missing")
	}
	if def.SourceType != "database" || def.DatabaseKind != "bucket" || def.Image != Templates["minio"].Services()[0].Image || def.Port != 9000 {
		t.Errorf("def: %+v", def)
	}
	if def.HealthcheckType != "http" || def.HealthcheckPath != "/minio/health/live" || !def.Domain || len(def.Volumes) != 1 || def.Volumes[0] != "/data" {
		t.Errorf("health/domain/volume: %+v", def)
	}
	if !strings.Contains(def.StartCommand, `mc mb --ignore-existing "aven/$BUCKET"`) || strings.Contains(def.StartCommand, def.GeneratedPassword) {
		t.Errorf("start command: %s", def.StartCommand)
	}
	v := def.Variables
	pw := def.GeneratedPassword
	if v["SECRET_ACCESS_KEY"] != pw || v["REGION"] != "us-east-1" || v["ENDPOINT"] != "http://${{AVEN_PRIVATE_DOMAIN}}:9000" || v["PUBLIC_ENDPOINT"] != "https://${{AVEN_PUBLIC_DOMAIN}}" {
		t.Errorf("vars: %v", v)
	}
	if !regexp.MustCompile(`^bucket-[0-9a-f]{10}$`).MatchString(v["BUCKET"]) || !regexp.MustCompile(`^AVEN[0-9A-F]{16}$`).MatchString(v["ACCESS_KEY_ID"]) {
		t.Errorf("generated names: %s %s", v["BUCKET"], v["ACCESS_KEY_ID"])
	}
	if strings.Contains(v["ACCESS_KEY_ID"], pw) || strings.Contains(v["BUCKET"], strings.ToLower(pw[:8])) {
		t.Error("derived values leak the secret")
	}
	for k, want := range map[string]string{
		"MINIO_ROOT_USER": "${{ACCESS_KEY_ID}}", "MINIO_ROOT_PASSWORD": "${{SECRET_ACCESS_KEY}}",
		"AWS_ACCESS_KEY_ID": "${{ACCESS_KEY_ID}}", "AWS_SECRET_ACCESS_KEY": "${{SECRET_ACCESS_KEY}}",
		"AWS_REGION": "${{REGION}}", "AWS_DEFAULT_REGION": "${{REGION}}", "AWS_ENDPOINT_URL": "${{ENDPOINT}}", "AWS_S3_BUCKET_NAME": "${{BUCKET}}",
	} {
		if v[k] != want {
			t.Errorf("%s = %q, want %q", k, v[k], want)
		}
	}
	// 같은 비밀번호면 같은 값(결정적), 다른 비밀번호면 다른 값
	if BucketName(pw) != v["BUCKET"] || BucketName(pw+"x") == v["BUCKET"] || BucketAccessKey(pw+"x") == v["ACCESS_KEY_ID"] {
		t.Error("derivation not deterministic per password")
	}
}

func TestBucketCredentialKeysAndOtherEnvironment(t *testing.T) {
	keys := CredentialKeys(BucketKind)
	for _, k := range []string{"SECRET_ACCESS_KEY", "ACCESS_KEY_ID", "BUCKET", "MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_S3_BUCKET_NAME"} {
		if !keys[k] {
			t.Errorf("%s should be a credential key", k)
		}
	}
	for _, k := range []string{"REGION", "ENDPOINT", "PUBLIC_ENDPOINT", "AWS_REGION", "AWS_ENDPOINT_URL"} {
		if keys[k] {
			t.Errorf("%s should not be a credential key", k)
		}
	}
	// 기존 DB 종류는 그대로
	pg := CredentialKeys("postgres")
	if len(pg) != 2 || !pg["POSTGRES_PASSWORD"] || !pg["PGPASSWORD"] {
		t.Errorf("postgres credential keys changed: %v", pg)
	}

	def, _ := DatabaseService(BucketKind)
	other := def.ForAnotherEnvironment()
	o := other.Variables
	if o["SECRET_ACCESS_KEY"] == def.Variables["SECRET_ACCESS_KEY"] || o["ACCESS_KEY_ID"] == def.Variables["ACCESS_KEY_ID"] || o["BUCKET"] == def.Variables["BUCKET"] {
		t.Errorf("other environment reuses credentials: %v", o)
	}
	if o["ACCESS_KEY_ID"] != BucketAccessKey(other.GeneratedPassword) || o["BUCKET"] != BucketName(other.GeneratedPassword) || o["REGION"] != "us-east-1" {
		t.Errorf("other environment vars: %v", o)
	}

	in := &store.Instance{}
	in.Spec.Defaults()
	def.ApplyToInstance(in)
	if in.HealthcheckType != "http" || in.HealthcheckPath != "/minio/health/live" || in.StartCommand == nil || *in.Port != 9000 {
		t.Errorf("instance spec: %+v", in.Spec)
	}
}

func TestBucketTemplateSource(t *testing.T) {
	d, err := ParseDefinition([]byte(`{"version":1,"key":"b","name":"B","services":[{"name":"files","source":{"database":"bucket"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defs, _, err := d.Instantiate(nil)
	if err != nil || len(defs) != 1 || defs[0].DatabaseKind != "bucket" || defs[0].Variables["SECRET_ACCESS_KEY"] == "" {
		t.Fatalf("instantiate: %+v %v", defs, err)
	}
}
