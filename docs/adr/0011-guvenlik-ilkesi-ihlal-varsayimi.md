# ADR-0011: Güvenlik ilkesi — ihlal varsayımı ve hasar sınırlama

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0008](0008-kvkk-ve-platform-yonetimi.md), [ADR-0010](0010-repo-kurallari-ve-is-akisi.md)

## Bağlam

Projenin amaçlarından biri güvenlik pratiklerini gerçek bir sistemde
uygulamaktır. Platform sahibinin hedefi: **sisteme sızmak olabildiğince zor
olmalı; sızılsa bile verilebilecek hasar en aza inmeli.**

Ev cluster'ı kurulduktan sonra yapılan değerlendirmede şu zayıf noktalar
görüldü: WSL ile Windows arasındaki geçirgen sınır, otomatik dağıtımın tedarik
zincirini saldırı yoluna çevirmesi, hesap ele geçirme riski, uygulandığı
doğrulanmamış NetworkPolicy'ler ve diskte şifresiz duran secret'lar. Gerçek
kullanıcı verisi henüz yoktur; ilk kez identity ile girecektir.

## Karar

### İlkeler

Her tasarım ve kod kararı bu ilkelere göre değerlendirilir. Bir ADR veya PR
bunlardan birini bilerek zayıflatıyorsa gerekçesini yazar.

1. **İhlal varsayımı.** Her katman, diğer katmanların aşılmış olabileceğini
   varsayarak tasarlanır. "Bu istek zaten gateway'den geçti" ya da "bu ağ
   içeride" güvenlik gerekçesi sayılmaz.
2. **En az yetki.** Her servis, pod, veritabanı kullanıcısı, token, CI işi ve
   hesap yalnızca işi için gereken yetkiye sahiptir; yetkiler zamanla
   sınırlıdır (kısa ömürlü token'lar).
3. **Hasar alanını sınırlama.** Bir bileşen ele geçirildiğinde ulaşabileceği
   veri ve sistemler mümkün olduğunca dardır: servis başına veritabanı ve
   kullanıcı, varsayılan olarak yasaklı ağ, ortamlar ve secret'lar arası
   ayrım, ev ile prod ayrımı.
4. **Tutulmayan veri çalınamaz.** Kişisel veri en azda tutulur, saklama
   süreleri dolunca silinir (ADR-0008). Tutulan hassas veri katmanlı
   korunur: birebir mesajlar uçtan uca, kanal mesajları uygulama seviyesinde
   şifreli; yedekler ve secret'lar şifreli.
5. **Tedarik zincirine güvenme, doğrula.** Cluster'da çalışan her şeyin
   kaynağı doğrulanabilir olmalıdır: sabitlenmiş bağımlılıklar, imzalı
   image ve manifestler, imza doğrulaması.
6. **Fark et.** Şüpheli olaylar (token yeniden kullanımı, admin girişi,
   yetki reddi artışı) kaydedilir ve bildirilir. Fark edilmeyen ihlal en
   pahalısıdır.
7. **Geri dönebil.** Yedekten geri yükleme ve anahtar/token yenileme
   adımları yazılıdır ve denenmiştir; bir sızıntıdan sonra dakikalar içinde
   uygulanabilir.

### Bu ilkeyle değişen kararlar

- **Servisler arası kimlik (ADR-0004'ü günceller):** İç servisler, gateway'in
  ilettiği kullanıcı kimliğine düz metadata olarak güvenmez. gateway
  kullanıcının erişim token'ını (JWT) olduğu gibi iletir; her servis imzayı
  identity'nin açık anahtarlarıyla **kendisi doğrular**. Böylece cluster
  ağına giren bir saldırgan başka bir kullanıcı adına istek uyduramaz.
  Servisten servise çağrılar (chat → core) de çağrıyı başlatan kullanıcının
  token'ını taşır. Kullanıcısız arka plan çağrıları (outbox relay) servis
  kimliğiyle, ayrı ve kısa ömürlü servis token'larıyla yapılır.

### Sertleştirme listesi

| # | Önlem | Durum |
|---|-------|-------|
| 1 | WSL izolasyonu: `C:` sürücüsünün bağlanması ve Windows programlarını çalıştırma kapalı | Uygulanıyor |
| 2 | GitHub ve Cloudflare hesaplarında 2FA, mümkünse passkey veya güvenlik anahtarı | Platform sahibi |
| 3 | GitHub Actions'ların commit SHA'sına sabitlenmesi | İş paketi |
| 4 | Image'ların ve manifest artifact'ının cosign ile imzalanması, Flux'ta imza doğrulaması | İş paketi |
| 5 | NetworkPolicy'lerin uygulandığını doğrulayan test (yasaklı bağlantının reddedildiği) | İş paketi |
| 6 | k3s'te secret'ların diskte şifrelenmesi (`secrets-encryption`), ardından tunnel token'ının yenilenmesi | İş paketi |
| 7 | Servislerin JWT'yi kendilerinin doğrulaması (yukarıda) | identity ile |
| 8 | Postgres'te servis başına en az yetkili kullanıcı (sahiplik ve şema değişikliği yalnızca migration kullanıcısında) | identity ile |
| 9 | Tayfa kapsamını veritabanında da zorlayan Row Level Security | Değerlendirilecek (core ile) |
| 10 | Şüpheli olaylar için bildirim (token yeniden kullanımı, admin girişi) | Gözlem işiyle |
| 11 | R2 erişim anahtarlarının bucket ve işlem bazında ayrılması (yedek yazan anahtar dosyaları okuyamaz) | CloudNativePG ile |
| 12 | Ev bilgisayarında disk şifrelemesi (BitLocker / cihaz şifrelemesi) | Platform sahibi |

1–6 numaralı önlemler, ilk gerçek kullanıcı verisinin gireceği identity iş
paketlerinden **önce** tamamlanır.

## Gerekçe

- Tek bir güçlü katmana güvenmek, o katmandaki tek bir hatayı tam bir ihlale
  çevirir. Katmanlar birbirinden bağımsız olduğunda saldırganın birden çok
  bağımsız hatayı birlikte bulması gerekir.
- Otomatik dağıtım (ADR-0006) hız kazandırır ama dağıtım hattını en değerli
  hedef yapar; imza doğrulaması bu hattı bir saldırı yolu olmaktan çıkarır.
- Servislerin JWT'yi kendilerinin doğrulaması az maliyetlidir (yerel imza
  kontrolü) ve ağ içindeki sahteciliği tamamen kapatır.

## Sonuçlar

- `CLAUDE.md` bu ilkeleri kesin kural olarak içerir; PR şablonu "bu bileşen
  ele geçirilirse ne olur?" sorusunu sorar.
- Sertleştirme listesi bu ADR'de tutulur; maddeler tamamlandıkça durumu
  güncellenir.
- Bazı önlemler (imzalama, SHA sabitleme) bakım yükü getirir; Dependabot SHA
  ile sabitlenmiş Actions'ları da güncelleyebildiği için yük kabul edilebilir.

## Değerlendirilen alternatifler

- **Ağ sınırına güvenmek (içerisi güvenli):** Daha basit, ama tek bir pod'un
  ele geçirilmesi tüm servislere erişim demektir.
- **Servisler arası mTLS (service mesh) ile başlamak:** Taşıma katmanını da
  korur, ama 4 GB'lık ev cluster'ı için ağır; JWT doğrulaması kimlik
  sahteciliğini daha ucuza kapatır. İleride yeniden değerlendirilir.
