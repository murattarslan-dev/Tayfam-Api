package media

import (
	"errors"
	"net/url"
	"regexp"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// Environment variables of the R2 configuration (ADR-0020 item 6). All four
// come from the optional core-r2 secret (deploy/apps/<ortam>/secrets, SOPS).
const (
	EnvAccountID       = "MEDIA_R2_ACCOUNT_ID"
	EnvBucket          = "MEDIA_R2_BUCKET"
	EnvAccessKeyID     = "MEDIA_R2_ACCESS_KEY_ID"
	EnvSecretAccessKey = "MEDIA_R2_SECRET_ACCESS_KEY" //nolint:gosec // the variable name, not a value
	// EnvLocalEndpoint replaces the R2 address with an S3-compatible one
	// (MinIO) and is accepted only in the local environment.
	EnvLocalEndpoint = "MEDIA_S3_ENDPOINT"
)

var (
	accountRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	bucketRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

// LoadR2 reads the R2 configuration. With none of the four values it returns
// nil: media is off and core runs without it. With some of them missing or
// malformed it returns an error and core does not start. The endpoint is
// built in code from the account ID (https://<account>.r2.cloudflarestorage.com):
// configuration cannot send the access key to another address. No value is
// ever part of an error.
func LoadR2(c *config.Loader, env string) (*objstore.Config, error) {
	vals := map[string]string{}
	set := 0
	for _, k := range []string{EnvAccountID, EnvBucket, EnvAccessKeyID, EnvSecretAccessKey} {
		vals[k] = c.String(k, "")
		if vals[k] != "" {
			set++
		}
	}
	local := c.String(EnvLocalEndpoint, "")
	if set == 0 {
		if local != "" {
			return nil, errors.New(EnvLocalEndpoint + " R2 erişim bilgisi olmadan verilmiş")
		}
		return nil, nil
	}
	if set != 4 {
		return nil, errors.New("R2 yapılandırması eksik: " + EnvAccountID + ", " + EnvBucket + ", " + EnvAccessKeyID + " ve " + EnvSecretAccessKey + " birlikte verilmeli")
	}
	if !bucketRe.MatchString(vals[EnvBucket]) {
		return nil, errors.New(EnvBucket + " geçersiz")
	}
	if len(vals[EnvAccessKeyID]) > 128 || len(vals[EnvSecretAccessKey]) > 256 {
		return nil, errors.New("R2 erişim anahtarı geçersiz")
	}
	endpoint := &url.URL{Scheme: "https", Host: vals[EnvAccountID] + ".r2.cloudflarestorage.com"}
	switch {
	case local != "":
		if env != "local" {
			return nil, errors.New(EnvLocalEndpoint + " yalnızca yerel ortamda kullanılabilir")
		}
		u, err := url.Parse(local)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New(EnvLocalEndpoint + " geçersiz")
		}
		endpoint = &url.URL{Scheme: u.Scheme, Host: u.Host}
	case !accountRe.MatchString(vals[EnvAccountID]):
		return nil, errors.New(EnvAccountID + " geçersiz (32 hex karakter)")
	}
	cfg := &objstore.Config{
		Endpoint: endpoint, Bucket: vals[EnvBucket], Region: "auto",
		AccessKeyID: vals[EnvAccessKeyID], SecretAccessKey: vals[EnvSecretAccessKey],
	}
	if _, err := objstore.New(*cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
