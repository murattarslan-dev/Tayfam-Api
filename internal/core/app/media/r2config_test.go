package media

import (
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
)

func TestLoadR2(t *testing.T) {
	const acct = "0123456789abcdef0123456789abcdef"
	full := map[string]string{
		EnvAccountID: acct, EnvBucket: "tayfam-dev-private",
		EnvAccessKeyID: "AKIDEXAMPLE", EnvSecretAccessKey: "gizli-deger-asla-yazilmaz",
	}
	with := func(m map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == "" {
				delete(out, kv[i])
			} else {
				out[kv[i]] = kv[i+1]
			}
		}
		return out
	}
	load := func(m map[string]string, env string) (bool, error) {
		c := config.New(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
		cfg, err := LoadR2(c, env)
		if err != nil && strings.Contains(err.Error(), "gizli-deger") {
			t.Fatal("hata mesajı gizli değeri içeriyor")
		}
		return cfg != nil, err
	}

	if on, err := load(map[string]string{}, "prod"); on || err != nil {
		t.Fatalf("yapılandırma yoksa kapalı ve hatasız olmalı: %v %v", on, err)
	}
	if on, err := load(full, "prod"); !on || err != nil {
		t.Fatalf("tam yapılandırma: %v %v", on, err)
	}
	c := config.New(func(k string) (string, bool) { v, ok := full[k]; return v, ok })
	cfg, _ := LoadR2(c, "dev")
	if cfg.Endpoint.String() != "https://"+acct+".r2.cloudflarestorage.com" || cfg.Region != "auto" {
		t.Fatalf("uç adresi koddan gelmeli: %v", cfg.Endpoint)
	}
	for name, m := range map[string]map[string]string{
		"eksik anahtar":          with(full, EnvSecretAccessKey, ""),
		"eksik bucket":           with(full, EnvBucket, ""),
		"hesap kimliği adres":    with(full, EnvAccountID, "evil.example.com/x"),
		"büyük harfli bucket":    with(full, EnvBucket, "Tayfam"),
		"yerel uç prod'da":       with(full, EnvLocalEndpoint, "http://minio:9000"),
		"yerel uç anahtarsız":    {EnvLocalEndpoint: "http://minio:9000"},
		"yerel uç bozuk (local)": with(full, EnvLocalEndpoint, "minio:9000"),
	} {
		env := "prod"
		if strings.Contains(name, "local") {
			env = "local"
		}
		if _, err := load(m, env); err == nil {
			t.Errorf("%s reddedilmeli", name)
		}
	}
	if on, err := load(with(full, EnvLocalEndpoint, "http://minio:9000", EnvAccountID, "yerel"), "local"); !on || err != nil {
		t.Fatalf("yerelde MinIO: %v %v", on, err)
	}
}
