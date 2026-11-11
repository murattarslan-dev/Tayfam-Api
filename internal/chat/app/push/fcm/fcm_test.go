package fcm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push/fcm"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/push/fcm/fcmtest"
)

var ctx = context.Background()

func client(t *testing.T, s *fcmtest.Server, now func() time.Time) *fcm.Client {
	t.Helper()
	a, err := fcm.ParseServiceAccount(s.Account(t))
	if err != nil {
		t.Fatal(err)
	}
	c, err := fcm.New(fcm.Config{Account: a, Transport: s.Transport(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func msg(platform, token string) push.Message {
	return push.Message{
		Token: token, Platform: platform, TitleKey: "push_dm_message_title", BodyKey: "push_dm_message_body",
		Data: map[string]string{"type": "dm_message", "target_id": "c"}, Group: "c", Channel: push.ChannelMessages, High: true,
		TTL: time.Hour,
	}
}

func TestParseServiceAccount(t *testing.T) {
	s := fcmtest.New(t)
	good := map[string]any{}
	if err := json.Unmarshal(s.Account(t), &good); err != nil {
		t.Fatal(err)
	}
	a, err := fcm.ParseServiceAccount(s.Account(t))
	if err != nil || a.ProjectID() != fcmtest.Project {
		t.Fatalf("geçerli hesap: %v", err)
	}
	if strings.Contains(a.String(), "PRIVATE") || strings.Contains(a.String(), "push@") {
		t.Fatalf("String sızdırıyor: %s", a)
	}
	for field, v := range map[string]any{
		"type": "user", "project_id": "Kötü Proje", "client_email": "", "token_uri": "https://kotu.example/token",
		"private_key": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		bad := map[string]any{}
		for k, x := range good {
			bad[k] = x
		}
		bad[field] = v
		raw, _ := json.Marshal(bad)
		_, err := fcm.ParseServiceAccount(raw)
		if err == nil {
			t.Errorf("%s kabul edildi", field)
			continue
		}
		// The error names the field, never a value.
		if !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("%s hatası: %v", field, err)
		}
	}
	if _, err := fcm.ParseServiceAccount([]byte("{")); err == nil {
		t.Error("bozuk JSON kabul edildi")
	}
}

func TestSendAndroidAndIOS(t *testing.T) {
	s := fcmtest.New(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	c := client(t, s, func() time.Time { return now })
	if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); err != nil {
		t.Fatal(err)
	}
	ios := msg(push.PlatformIOS, "tok:i")
	ios.TimeSensitive = true
	if err := c.Send(ctx, ios); err != nil {
		t.Fatal(err)
	}
	sent := s.Sent()
	if len(sent) != 2 || s.OAuthCalls() != 1 {
		t.Fatalf("gönderim %d, oauth %d", len(sent), s.OAuthCalls())
	}
	// Only the two fixed Google hosts are ever called.
	for h := range s.Hosts() {
		if h != "oauth2.googleapis.com" && h != "fcm.googleapis.com" {
			t.Fatalf("beklenmeyen adres: %s", h)
		}
	}
	a, _ := json.Marshal(sent[0])
	want := `{"android":{"notification":{"body_loc_key":"push_dm_message_body","channel_id":"tayfam_messages","tag":"c","title_loc_key":"push_dm_message_title"},"priority":"HIGH","ttl":"3600s"},"data":{"target_id":"c","type":"dm_message"},"token":"tok:a"}`
	if string(a) != want {
		t.Fatalf("android:\n%s\nbeklenen:\n%s", a, want)
	}
	i, _ := json.Marshal(sent[1])
	want = `{"apns":{"headers":{"apns-collapse-id":"c","apns-expiration":"` + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `","apns-priority":"10","apns-push-type":"alert"},"payload":{"aps":{"alert":{"loc-key":"push_dm_message_body","title-loc-key":"push_dm_message_title"},"interruption-level":"time-sensitive","sound":"default","thread-id":"c"}}},"data":{"target_id":"c","type":"dm_message"},"token":"tok:i"}`
	if string(i) != want {
		t.Fatalf("ios:\n%s\nbeklenen:\n%s", i, want)
	}
}

func TestAccessTokenIsCachedAndRenewed(t *testing.T) {
	s := fcmtest.New(t)
	now := time.Now().UTC()
	c := client(t, s, func() time.Time { return now })
	for range 3 {
		if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.OAuthCalls(); n != 1 {
		t.Fatalf("oauth = %d", n)
	}
	// Close to expiry: a new one.
	now = now.Add(56 * time.Minute)
	if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); err != nil {
		t.Fatal(err)
	}
	if n := s.OAuthCalls(); n != 2 {
		t.Fatalf("yenileme: oauth = %d", n)
	}
	// Refused access token: transient, and the next send fetches a new one.
	s.RejectAccessTokens()
	var se *push.SendError
	if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); !errors.As(err, &se) || se.Kind != push.Transient {
		t.Fatalf("reddedilen erişim token'ı: %v", err)
	}
	if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); err != nil || s.OAuthCalls() != 3 {
		t.Fatalf("yeniden alım: %v %d", err, s.OAuthCalls())
	}
}

func TestErrorsAreClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		r     fcmtest.Response
		kind  push.ErrorKind
		code  string
		after time.Duration
	}{
		"kayıtsız":         {fcmtest.Unregistered, push.InvalidToken, "UNREGISTERED", 0},
		"geçersiz token":   {fcmtest.BadToken, push.InvalidToken, "INVALID_ARGUMENT", 0},
		"bozuk içerik":     {fcmtest.BadPayload, push.Rejected, "INVALID_ARGUMENT", 0},
		"başka gönderen":   {fcmtest.Mismatch, push.InvalidToken, "SENDER_ID_MISMATCH", 0},
		"geçici":           {fcmtest.Unavailable, push.Transient, "UNAVAILABLE", 2 * time.Minute},
		"kota":             {fcmtest.Quota, push.Transient, "QUOTA_EXCEEDED", 0},
		"APNs kimliği":     {fcmtest.APNsAuth, push.Rejected, "THIRD_PARTY_AUTH_ERROR", 0},
		"gövdesiz 500":     {fcmtest.Response{Status: 500}, push.Transient, "", 0},
		"metin yansıtması": {fcmtest.Response{Status: 400, Body: `{"error":{"status":"tok:secret yansıdı"}}`}, push.Rejected, "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			s := fcmtest.New(t)
			c := client(t, s, nil)
			s.Answer("tok:secret", tc.r)
			err := c.Send(ctx, msg(push.PlatformAndroid, "tok:secret"))
			var se *push.SendError
			if !errors.As(err, &se) {
				t.Fatalf("hata = %v", err)
			}
			if se.Kind != tc.kind || se.Code != tc.code || se.RetryAfter != tc.after || se.Status != tc.r.Status {
				t.Fatalf("sınıf = %+v", se)
			}
			// Neither the token nor the access token appears in the error.
			if strings.Contains(err.Error(), "tok:") || strings.Contains(err.Error(), "fake-access") {
				t.Fatalf("hata sızdırıyor: %v", err)
			}
		})
	}
}

func TestNetworkErrorIsTransient(t *testing.T) {
	s := fcmtest.New(t)
	a, err := fcm.ParseServiceAccount(s.Account(t))
	if err != nil {
		t.Fatal(err)
	}
	c, err := fcm.New(fcm.Config{Account: a, Transport: failing{}})
	if err != nil {
		t.Fatal(err)
	}
	var se *push.SendError
	if err := c.Send(ctx, msg(push.PlatformAndroid, "tok:a")); !errors.As(err, &se) || se.Kind != push.Transient {
		t.Fatalf("ağ hatası: %v", err)
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("bağlantı yok")
}
