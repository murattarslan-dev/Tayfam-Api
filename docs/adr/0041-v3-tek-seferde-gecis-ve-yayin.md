# ADR-0041: v3'e tek seferde geçiş ve yayın

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Karar veren:** Murat Arslan (2026-11-18). Notion "Faz 3 — Katalog
  ayrıntıları ve şema ekleri (2026-11-18)" §E.6.
- **İlgili:** [ADR-0016](0016-desteklenen-istemci-surumu.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0034](0034-dev-main-dal-modeli-ve-otomatik-surum.md); Tayfam-App
  ADR-0033
- **Değiştirdiği:** ADR-0025 §8 (geriye uyumlu alanlar), §10 (aşamalı
  taşıma). ADR-0025'e revizyon notu düşüldü.

## Bağlam

ADR-0025 §10, eski ve yeni yapının bir süre birlikte yaşadığı üç aşamalı bir
taşıma öngörüyordu: önce katalog, sonra bileşen tablolarına kopyalama, uçların
iki yerden okuması, en düşük istemci sürümü v3'e çekilince eski uçların ve
tabloların kaldırılması. Bu, eski uygulama sürümlerini kullanan gerçek
kullanıcılar için gerekliydi.

Platform sahibinin kararları (2026-11-18):

- Bütün Faz 3 işleri **tek seferde** (0.3.0) yayınlanır; ara sürüm,
  özellik bayrağı ya da aşama yoktur.
- Uygulama yalnızca platform sahibinin telefonunda kuruludur; geriye
  uyumluluk dönemine gerek yoktur.

## Karar

1. **İş `dev`'de birikir.** Faz 3 PR'ları `dev`'e birleşir (ADR-0034);
   iş bitene kadar sürüm PR'ı açılmaz. Prod ve Play kapalı testi 0.2.x'te
   kalır.
2. **Geriye uyumluluk yok.** OpenAPI'de `EventType` enum'u ve türe özel
   gövdeler (`convoy`, `camp`…) **doğrudan** kaldırılır; yerine `type`
   anahtarı, `type_version`, `fields` ve bileşen uçları gelir. Yanıtlarda
   Faz 1 alanları doldurulmaz. "İki yerden okuma" yazılmaz.
3. **Taşıma tek migration zinciridir:** katalog tablolarının ve yedi türün
   eklenmesi, türe özel tablolardaki verinin bileşen tablolarına
   kopyalanması ve **aynı sürümde** türe özel tabloların, CHECK
   kısıtlarının kaldırılması. Veri silen migration hassas alandır (CODEOWNERS
   `db/*/migrations/`); geri alma planı PR'da yazılır. Prod'daki veri
   yalnızca demo ve platform sahibinin verisidir.
4. **En düşük istemci sürümü** 0.3.0 sürümüyle aynı anda `0.3.0` olur
   (`CLIENT_MIN_VERSION_ANDROID`, `CLIENT_MIN_VERSION_IOS`; ADR-0016).
   Ayarın değişmesi deploy değişikliğidir (platform sahibi listesi).
5. **Sürüm sırası:** API sürüm PR'ı önce birleşir ve prod'a çıkar; ardından
   App 0.3.0 sürüm PR'ı. Arada açık kalan eski uygulama zorunlu güncelleme
   ekranı gösterir.
6. Ajanların PR'ları `dev`'i her zaman derlenebilir ve testleri yeşil
   tutar; yarım kalan bir özellik `dev`'de görünür olabilir (yayın yok).

## Gerekçe

Geçiş dönemi yalnızca eski sürümü kullanan gerçek kullanıcı varken değer
taşır. Tek kullanıcı ve demo verisiyle, iki yapıyı birlikte yaşatmak her
bileşen işini ikiye katlardı.

## Sonuçlar

- ADR-0025 §10'un 3. maddesi ve §8'in "geriye uyumluluk" cümlesi
  geçersizdir.
- Faz 3 iş kayıtları geriye uyumluluk istemez; App iş kayıtları eski
  türe özel ekranları silebilir.
- Pilot kullanıcılar gelmeden önce bir sonraki kırıcı değişiklikte bu ADR
  emsal alınmaz; gerçek kullanıcı varken ADR-0025 §10 modeli geçerlidir.

## Değerlendirilen alternatifler

- **ADR-0025 §10'daki aşamalı taşıma:** Reddedildi; tek kullanıcı için
  gereksiz maliyet.
- **Özellik bayrakları ve aşamalı açılış:** Reddedildi (platform sahibi).
