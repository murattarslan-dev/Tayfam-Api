# ADR-0014: WebSocket bağlantısı kullanıcının erişim token'ıyla

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-09
- **Revizyon:** 2026-11-10, web istemcisi kaldırıldı
  ([ADR-0017](0017-yalnizca-mobil-istemci.md), issue #124): bağlantı
  yalnızca mobilin `Authorization: Bearer` başlığıyla açılır; çerez yolu
  kalktı. 5. maddenin yerine ADR-0017 geçti: `Origin` taşıyan her istek,
  kaynağı ne olursa olsun gateway'de ve chat'te reddedilir
  (`forbidden-origin`); chat'te izinli kaynak listesi yoktur. 1., 2. ve 4.
  maddelerdeki ve "Sonuçlar"daki web/çerez ifadeleri tarihçe için duruyor.
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md) ("WebSocket kimlik doğrulaması" bölümünün yerine geçer), [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md) ("Bağlantı" bölümünü değiştirir), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0013](0013-servis-kimligi.md)

## Bağlam

ADR-0004 ve ADR-0005, WebSocket'in 30 saniyelik tek kullanımlık bir biletle
açılmasını ve bağlantının erişim token'ının süresi dolduktan sonra da açık
kalmasını öngörüyordu. Bilet issue #82'nin ilk adımında uygulandı (PR #91).

İkinci adımda (kanal aboneliği ve WebSocket'ten gönderim) şu görüldü: kanal
izinlerini core'daki tek izin tablosu verir (`AuthorizeChannelAction`) ve core
bu soruyu yalnızca kullanıcının kendi token'ıyla kabul eder; servis kimliği
kullanıcının başlattığı işlerde kullanılmaz (ADR-0013). Biletle açılan ve token
süresinden uzun yaşayan bir bağlantıda chat'in elinde kullanıcı token'ı yoktur;
abonelikte, gönderimde ve rol değişince yeniden denetimde core'a soru
sorulamaz.

## Karar

1. **Bağlantı token'la açılır.** İstemci `GET /v1/realtime`'ı erişim
   token'ıyla açar: mobil `Authorization: Bearer` başlığıyla (yerel istemciler
   WebSocket'te başlık koyabilir), web `tayfam_at` çereziyle (tarayıcı aynı
   site isteğinde kendisi gönderir). Token URL'de taşınmaz. **Bilet
   kaldırılır** (`realtime_tickets` tablosu silinir; `RealtimeService`
   sözleşmede geriye uyumluluk için kullanımdışı olarak kalır, sunulmaz).
2. **gateway doğrular, chat yeniden doğrular.** gateway Origin'i ve token'ı
   doğrular, isteği chat'in HTTP portuna aktarırken token'ı `Authorization`
   başlığına koyar; çerezi iletmez. chat token'ın imzasını kendisi doğrular
   (ADR-0011, CLAUDE.md kural 8).
3. **Token yalnızca bağlantının belleğinde tutulur**, log'a ve veritabanına
   yazılmaz. Bağlantının kullanıcısı için core'a sorulan her soru (abonelik,
   gönderim, üyelik değişince yeniden denetim) bu token'la, `auth.ForwardToken`
   deseniyle sorulur. Servis kimliği istisnası yoktur; core değişmez.
4. **Bağlantı en geç token'ın `exp` anında kapanır** (4003 `token_expired`).
   İstemci token'ı yenileyip (mobil: refresh sonrası yeni başlık, web: yenilenen
   çerez) yeniden bağlanır ve kaçırdıklarını REST ile çeker. Ayrı bir yeniden
   kimlik doğrulama çerçevesi yoktur. Bağlantı ömrü böylece ≤ 15 dakikadır.
5. ~~**Çerezle bağlantıda Origin zorunludur.**~~ (2026-11-10: yerine
   ADR-0017 geçti; `Origin` taşıyan her istek reddedilir.) Tarayıcılar WebSocket'te her zaman
   `Origin` gönderir; çerezle gelen ve `Origin` taşımayan ya da izinli listede
   olmayan istek reddedilir (siteler arası WebSocket ele geçirmesine karşı).
   `Origin`'siz istek yalnızca `Authorization` başlığıyla kabul edilir.
6. Oturum iptali, yaptırım ve hesap silme bağlantıları yine anında kapatır
   (`session.revoked`, `user.sanctioned`, `user.deleted`; ADR-0009).

## Gerekçe

- Kullanıcının başlattığı her işin kullanıcının token'ıyla yapılması kuralı
  (ADR-0013) WebSocket'te de korunur; chat'e core'da yeni bir yetki verilmez.
- Bilet, token'ın URL'de taşınmaması için vardı. Başlık ve çerez bunu zaten
  sağlıyor; bilet ek bir tablo, temizlik işi ve gidiş dönüş getiriyordu.
- Bağlantının token'la sınırlı ömrü, iptal olayı kaçsa bile en fazla 15
  dakikalık bir pencere bırakır; REST ile aynı kabul edilmiş pencere
  (ADR-0004).
- Yeniden kimlik doğrulama çerçevesi web'de çalışmazdı (token HttpOnly
  çerezde); yeniden bağlanmak her istemcide aynı yoldur.

## Sonuçlar

- İstemciler en geç 15 dakikada bir yeniden bağlanır; WebSocket yalnızca canlı
  iletim olduğu için (ADR-0005) kaçırılanlar REST ile tamamlanır.
- Web'de çerezli kimlik doğrulama yalnızca Origin kontrolüne ve `SameSite`
  çerezlere dayanır; izinli origin listesi (`CORS_ALLOWED_ORIGINS`) hem gateway
  hem chat'te kullanılır.
- chat bağlantı başına bir erişim token'ını bellekte tutar; chat'in belleğini
  okuyabilen biri açık bağlantıların token'larını (her biri ≤ 15 dk geçerli)
  görebilir. Bu, gRPC isteklerinde token'ların zaten chat'ten geçmesiyle aynı
  düzeydedir.

## Değerlendirilen alternatifler

- **Bileti korumak, core'a servis kimliğiyle kanal izni sormak:** Bağlantı
  token'dan bağımsız uzun yaşardı, ama ADR-0013'e istisna ve core'da yeni bir
  metod gerekirdi; ele geçirilen chat herhangi bir kullanıcının iznini
  sorgulayabilirdi.
- **WebSocket'i yalnızca iletime bırakmak, abonelik ve gönderimi REST'e
  taşımak:** ADR-0013'e uyar, ama üyelik değişince yeniden denetim için yine
  token gerekir ve ADR-0005'teki tek gönderim yolu (REST ve WebSocket) bozulur.
- **Bağlantıda token yenileme çerçevesi:** Mobilde çalışır, web'de çalışmaz.
- **Bileti token'la birlikte istemek:** Çerezli web bağlantısında ek bir
  CSRF/CSWSH katmanı olurdu, ama Origin kontrolü aynı korumayı sağlıyor.
