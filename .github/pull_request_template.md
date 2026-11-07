## Issue

Closes #

## Ne değişti

<!-- Kısa açıklama. Neden gerekli? -->

## İlgili ADR'ler

<!-- Örn. ADR-0004. Bu PR bir kararla çelişiyor veya yeni karar gerektiriyorsa burada açıkça yaz. -->

## Hassas alan

- [ ] Bu PR `.github/CODEOWNERS`'taki bir yola dokunuyor → yalnızca platform sahibi birleştirir.
      Dokunulan alan:

## Test

<!-- Hangi testler eklendi? Yerelde nasıl doğrulandı? -->

- [ ] `make check` yerelde geçti

## Ele geçirilirse

<!-- ADR-0011: Bu PR'daki bileşen ele geçirilirse saldırgan neye ulaşır? Hasar nasıl sınırlanıyor? -->

## Güvenlik kontrol listesi

- [ ] Yetki kontrolü sunucuda yapılıyor; tayfa kapsamı ve üyelik kontrol ediliyor
- [ ] Girdi doğrulaması (Go ve veritabanı) ve boyut sınırları var
- [ ] Log, metrik ve trace'lere kişisel veri, konum veya token yazılmıyor
- [ ] Gizli bilgi koda veya repoya girmiyor
- [ ] Yetkiler en az düzeyde (servis, veritabanı kullanıcısı, token, CI izni)
- [ ] Yeni bağımlılık veya Action sabit sürümle (SHA/digest) ekleniyor
- [ ] Yeni bağımlılık eklendiyse gerekçesi yukarıda yazılı
- [ ] Veri silen veya değiştiren migration geri alma planıyla birlikte
