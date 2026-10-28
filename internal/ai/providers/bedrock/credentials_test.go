package bedrock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsFromEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")
	t.Setenv("AWS_SESSION_TOKEN", "envtoken")

	creds, ok := credentialsFromEnv()
	if !ok {
		t.Fatal("expected env credentials")
	}
	if creds.AccessKeyID != "AKIENV" || creds.SessionToken != "envtoken" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestCredentialsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(`
[default]
aws_access_key_id = AKIDEFAULT
aws_secret_access_key = defaultsecret

[work]
aws_access_key_id = AKIWORK
aws_secret_access_key = worksecret
aws_session_token = worktoken
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)
	t.Setenv("AWS_PROFILE", "")

	creds, ok := credentialsFromFile("")
	if !ok || creds.AccessKeyID != "AKIDEFAULT" {
		t.Fatalf("default = %+v", creds)
	}

	creds, ok = credentialsFromFile("work")
	if !ok || creds.AccessKeyID != "AKIWORK" || creds.SessionToken != "worktoken" {
		t.Fatalf("work = %+v", creds)
	}
}

func TestDefaultCredentialsPrefersEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing"))

	d := &defaultCredentials{}
	creds, err := d.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "AKIENV" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestStaticCredentials(t *testing.T) {
	src := StaticCredentials{AccessKeyID: "A", SecretAccessKey: "B"}
	creds, err := src.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "A" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestMissingCredentialsError(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	d := &defaultCredentials{}
	_, err := d.Credentials(context.Background())
	if err == nil {
		t.Fatal("expected an error when nothing is configured")
	}
}
