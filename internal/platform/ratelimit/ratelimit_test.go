package ratelimit

import (
	"testing"
	"time"
)

func TestBurstAndRefill(t *testing.T) {
	l := New(60, 3) // 1/s, 3 at once
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := range 3 {
		if !l.Allow("ip") {
			t.Fatalf("istek %d reddedildi", i)
		}
	}
	if l.Allow("ip") {
		t.Fatal("kova boşken izin verildi")
	}
	if !l.Allow("baska-ip") {
		t.Fatal("anahtarlar birbirini etkiliyor")
	}
	now = now.Add(time.Second)
	if !l.Allow("ip") || l.Allow("ip") {
		t.Fatal("saniyede bir dolmalı")
	}
}

func TestEvery(t *testing.T) {
	l := Every(time.Hour, 1)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	if !l.Allow("ip") || l.Allow("ip") {
		t.Fatal("saatte bir olmalı")
	}
	now = now.Add(59 * time.Minute)
	if l.Allow("ip") {
		t.Fatal("bir saat dolmadan izin verildi")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("ip") {
		t.Fatal("bir saat sonra izin verilmedi")
	}
}

func TestSweepAndCap(t *testing.T) {
	l := New(60, 1)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.maxKeys = 2
	l.Allow("a")
	l.Allow("b")
	if l.Allow("c") {
		t.Fatal("anahtar sınırı aşıldı")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("c") {
		t.Fatal("dolan kovalar temizlenmedi")
	}
}

func TestPerWindow(t *testing.T) {
	l := NewPer(10, time.Hour)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := range 10 {
		if !l.Allow("u") {
			t.Fatalf("deneme %d reddedildi", i)
		}
	}
	if l.Allow("u") {
		t.Fatal("saatte 10'dan fazlasına izin verildi")
	}
	now = now.Add(5 * time.Minute)
	if l.Allow("u") {
		t.Fatal("6 dakikada bir dolmalı")
	}
	now = now.Add(time.Minute)
	if !l.Allow("u") || l.Allow("u") {
		t.Fatal("6 dakika sonra tek deneme açılmalı")
	}
}
