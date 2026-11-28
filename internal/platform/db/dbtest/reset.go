package dbtest

import (
	"context"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// ResetSpec describes a service's pre-pilot reset script
// (db/<service>/reset/pilot-oncesi-sifirlama.sql, ADR-0046) for CheckReset.
type ResetSpec struct {
	Service    string
	Migrations fs.FS
	// Script is the path of the service's reset script, OtherScript another
	// service's script that must refuse to run on this database.
	Script, OtherScript string
	// Fixtures fills the database as a superuser before every case. It must
	// put rows into every table of Kept that the migrations leave empty and
	// into the tables of Filled.
	Fixtures string
	// BeforeDelete runs before a "sil" run that should succeed (core: the
	// media cleanup emptying uploads).
	BeforeDelete string
	// Kept are the tables the script must leave exactly as they were.
	Kept []string
	// Partial are tables the script empties only partly: the row count
	// expected afterwards.
	Partial map[string]int64
	// Filled are tables the fixtures fill and the script must empty: a
	// guard against a fixture that tests nothing.
	Filled []string
	// AtThreshold brings the real-account count to exactly the threshold
	// (deleting is allowed); OneMore adds one more (deleting is refused).
	AtThreshold, OneMore string
}

// Settings for a deleting run in the given environment.
func resetSettings(mode, env, confirm string) map[string]string {
	s := map[string]string{"tayfam.mod": mode}
	if env != "" {
		s["tayfam.ortam"] = env
	}
	if confirm != "" {
		s["tayfam.onay"] = confirm
	}
	return s
}

// CheckReset pins what a reset script deletes and keeps and that its locks
// refuse: the scope (every table not kept is empty afterwards, kept tables
// are unchanged), the dry run, the confirmation phrase, the database owner
// check, the table classification and the real-account threshold.
func CheckReset(t *testing.T, s ResetSpec) {
	t.Helper()
	open := func(t *testing.T) DB {
		t.Helper()
		d := Open(t, s.Service, s.Migrations)
		execFixture(t, d, s.Fixtures)
		return d
	}
	sil := resetSettings("sil", "prod", "PROD-SIFIRLA")

	t.Run("DryRunChangesNothing", func(t *testing.T) {
		d := open(t)
		before := TableCounts(t, d.Admin)
		notices, err := RunScript(t, d, s.Script, resetSettings("kuru", "", ""))
		if err != nil {
			t.Fatalf("kuru çalışma: %v", err)
		}
		if after := TableCounts(t, d.Admin); !maps.Equal(before, after) {
			t.Fatalf("kuru çalışma veriyi değiştirdi:\nönce  %v\nsonra %v", before, after)
		}
		for _, table := range s.Filled {
			line := regexp.MustCompile(`^silinecek\s+` + regexp.QuoteMeta(table) + `\s+[1-9][0-9]*$`)
			if !slices.ContainsFunc(notices, line.MatchString) {
				t.Errorf("kuru çalışma %s için satır sayısı yazmadı: %q", table, notices)
			}
		}
	})

	t.Run("DeletesEverythingButKept", func(t *testing.T) {
		d := open(t)
		execFixture(t, d, s.BeforeDelete)
		before := TableCounts(t, d.Admin)
		for _, table := range s.Filled {
			if before[table] == 0 {
				t.Fatalf("fixture %s tablosunu doldurmuyor", table)
			}
		}
		for _, table := range s.Kept {
			if _, ok := before[table]; !ok {
				t.Fatalf("korunan tablo %s şemada yok", table)
			}
		}
		if _, err := RunScript(t, d, s.Script, sil); err != nil {
			t.Fatalf("sil: %v", err)
		}
		after := TableCounts(t, d.Admin)
		for table, n := range after {
			switch want, partial := s.Partial[table]; {
			case slices.Contains(s.Kept, table):
				if n != before[table] {
					t.Errorf("korunan %s değişti: %d → %d", table, before[table], n)
				}
			case partial:
				if n != want {
					t.Errorf("%s: %d satır kaldı, beklenen %d", table, n, want)
				}
			case n != 0:
				t.Errorf("%s boşalmadı: %d satır", table, n)
			}
		}
		// TRUNCATE yasakları yeniden açık.
		var disabled int
		if err := d.Admin.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgenabled = 'D'`).Scan(&disabled); err != nil {
			t.Fatal(err)
		}
		if disabled != 0 {
			t.Errorf("%d trigger kapalı kaldı", disabled)
		}
		// Tekrar çalıştırmak zararsız.
		if _, err := RunScript(t, d, s.Script, sil); err != nil {
			t.Fatalf("ikinci sil: %v", err)
		}
	})

	t.Run("LocksRefuse", func(t *testing.T) {
		d := open(t)
		execFixture(t, d, s.BeforeDelete)
		before := TableCounts(t, d.Admin)
		cases := []struct {
			name     string
			settings map[string]string
			code     string
		}{
			{"mod yok", map[string]string{}, "TS001"},
			{"bilinmeyen mod", resetSettings("hepsi", "prod", "PROD-SIFIRLA"), "TS001"},
			{"onay yok", resetSettings("sil", "prod", ""), "TS002"},
			{"ortam yok", resetSettings("sil", "", "PROD-SIFIRLA"), "TS002"},
			{"başka ortamın onayı", resetSettings("sil", "prod", "DEV-SIFIRLA"), "TS002"},
			{"küçük harf onay", resetSettings("sil", "prod", "prod-sifirla"), "TS002"},
			{"bilinmeyen ortam", resetSettings("sil", "staging", "STAGING-SIFIRLA"), "TS002"},
		}
		for _, c := range cases {
			_, err := RunScript(t, d, s.Script, c.settings)
			if got := SQLState(err); got != c.code {
				t.Errorf("%s: hata kodu %q, beklenen %s (%v)", c.name, got, c.code, err)
			}
		}
		_, err := RunScript(t, d, s.OtherScript, sil)
		if got := SQLState(err); got != "TS003" {
			t.Errorf("başka servisin betiği: hata kodu %q, beklenen TS003 (%v)", got, err)
		}
		if after := TableCounts(t, d.Admin); !maps.Equal(before, after) {
			t.Fatalf("reddedilen çalıştırma veriyi değiştirdi:\nönce  %v\nsonra %v", before, after)
		}
	})

	t.Run("UnclassifiedTableRefused", func(t *testing.T) {
		d := open(t)
		execFixture(t, d, "CREATE TABLE sifirlama_yeni_tablo (id int)")
		for _, mode := range []string{"kuru", "sil"} {
			_, err := RunScript(t, d, s.Script, resetSettings(mode, "prod", "PROD-SIFIRLA"))
			if got := SQLState(err); got != "TS004" {
				t.Errorf("%s: hata kodu %q, beklenen TS004 (%v)", mode, got, err)
			}
		}
	})

	t.Run("Threshold", func(t *testing.T) {
		at := open(t)
		execFixture(t, at, s.AtThreshold)
		execFixture(t, at, s.BeforeDelete)
		if _, err := RunScript(t, at, s.Script, sil); err != nil {
			t.Fatalf("eşikte sil reddedildi: %v", err)
		}

		over := open(t)
		execFixture(t, over, s.AtThreshold)
		execFixture(t, over, s.OneMore)
		execFixture(t, over, s.BeforeDelete)
		before := TableCounts(t, over.Admin)
		notices, err := RunScript(t, over, s.Script, resetSettings("kuru", "", ""))
		if err != nil {
			t.Fatalf("eşik aşılınca kuru çalışma da çalışmalı: %v", err)
		}
		if !slices.ContainsFunc(notices, func(n string) bool { return strings.Contains(n, "EŞİK AŞILDI") }) {
			t.Errorf("kuru çalışma eşik aşımını yazmadı: %q", notices)
		}
		_, err = RunScript(t, over, s.Script, sil)
		if got := SQLState(err); got != "TS005" {
			t.Fatalf("eşik aşılınca sil: hata kodu %q, beklenen TS005 (%v)", got, err)
		}
		if after := TableCounts(t, over.Admin); !maps.Equal(before, after) {
			t.Fatal("eşik reddi veriyi değiştirdi")
		}
	})
}

func execFixture(t *testing.T, d DB, sql string) {
	t.Helper()
	if strings.TrimSpace(sql) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := d.Admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Fixtures may switch off triggers and FKs for their own connection
	// (session_replication_role); the connection is not reused.
	pc := conn.Hijack()
	defer func() { _ = pc.Close(ctx) }()
	if _, err := pc.Exec(ctx, sql); err != nil {
		t.Fatalf("fixture: %v", err)
	}
}
