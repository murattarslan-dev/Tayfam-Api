# ADR-0008: KVKK ve platform yönetimi

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md)
- **Kaynak:** Notion: Proje Planı (Güvenlik Tedbirleri), Platform Yönetim
  Paneli, KVKK Aydınlatma Metni (taslak), Veri Modeli v13 (v5 KVKK kuralları,
  v13 platform yönetimi)

## Bağlam

KVKK kapsamındaki ürün kararları ve platform yönetim panelinin tasarımı
Notion'daki planda alınmıştır. Bu ADR o kararları değiştirmez; mikroservis
mimarisinde (ADR-0001, ADR-0002) nasıl uygulanacaklarını ve Notion'da
"mimari aşamasında karar verilecek" diye bırakılan noktaları belirler. Veri
sorumlusu gerçek kişi olarak Murat Tarslan'dır.

## Karar

### Hesap silme

- Kullanıcı hesabını uygulamadan siler. Geri alma süresi yoktur.
- **Hemen:** identity tüm oturumları iptal eder, giriş kimliklerini
  (`auth_identities`) ve cihazları siler, ad ve avatarı temizler,
  `user.deleted` olayını yayınlar.
- Olayı alan servisler kendi verilerinde (ADR-0009):
  - Kullanıcıya ait kayıtları siler: üyelikler, katılım yanıtları, oylar,
    arkadaşlıklar, engellemeler, katılım geçmişi, cihaz anahtarları, DM arşivi,
    bildirimler.
  - Yazdığı içeriği anonimleştirir: yazar alanı `null` olur, içerik kalır ve
    "Silinmiş kullanıcı" olarak gösterilir.
  - Son yönetici korumasını uygular: tayfanın son yöneticisi silinirse en eski
    moderatör, yoksa en eski üye yönetici olur.
- **30 gün sonra:** `users` satırı kalıcı silinir.

### Veri dışa aktarma (KVKK md. 11)

- Kullanıcı 30 günde en fazla bir talep oluşturabilir. Talebin sahibi
  identity'dir (`data_export_requests`).
- identity, core ve chat'in `ExportUserData` gRPC metodlarını çağırarak
  kullanıcının verisini toplar: profil, üyelikler, katılımlar, kendi içerikleri
  (duyuru, etkinlik, soru, cevap, kanal mesajları, av kayıtları) ve kendi anket
  oyları.
- Sonuç JSON olarak zip'lenir, şifrelenir ve private R2 bucket'ına yüklenir
  (ADR-0007). İndirme kısa ömürlü imzalı linkle yapılır; dosya 7 gün sonra
  silinir.
- Birebir mesajlar sunucuda okunamadığı için sunucu dışa aktarımına girmez;
  istemci, şifreli arşivden cihazda dışa aktarma sunar.

### Onay ve yaş beyanı

- Aydınlatma metni ve kullanım koşulları sürümlüdür. Onaylar identity'de
  `consent_records`'a yalnızca eklenerek yazılır; geri çekme `withdrawn_at` ile
  işaretlenir.
- Yeni sürüm yayınlanınca kullanıcı bir sonraki girişte onay ekranı görür.
  Güncel sürümü onaylamamış kullanıcının token'ı yalnızca onay uçlarına
  erişebilir.
- Kayıtta doğum tarihi zorunludur; 16 yaşından küçükler kayıt olamaz. Doğum
  tarihi yalnızca identity'de, şifreli tutulur ve diğer servislere
  gönderilmez ([ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md); bu madde
  önceki "16 yaş beyanı, doğum tarihi saklanmaz" kararının yerini alır).

### Saklama süreleri

Notion'daki tablolar geçerlidir; her servis kendi verisinin saklama işini
çalıştırır.

| Veri | Süre | Servis |
|------|------|--------|
| Soft delete edilen içerik | 30 gün, sonra kalıcı silme | core, chat |
| Silinen hesabın `users` satırı | 30 gün (doğum tarihi hesap silinince hemen temizlenir) | identity |
| Yasal bekletmedeki içerik (ADR-0012) | Bekletme kaldırılana kadar; üst sınır hukukçuyla | core, chat |
| `audit_logs`, çözülmüş şikâyetler | 1 yıl | core |
| `platform_audit_logs` | 1 yıl | identity |
| Bildirimler | 90 gün | chat |
| 90 gündür görünmeyen cihaz | Cihaz, oturumları, anahtarları ve bekleyen zarfları silinir | identity → chat |
| Kullanılmış veya süresi dolmuş refresh token'lar | 30 gün | identity |
| Konvoy plakası | Etkinlikten 30 gün sonra temizlenir | core |
| Teslim edilemeyen DM zarfları | 30 gün | chat |
| Veri dışa aktarma dosyası | 7 gün | identity, R2 |
| Bekleyen yüklemeler | 24 saat | core, R2 |
| Uygulama logları ve trace'ler | 30 gün | gözlem stack'i |
| Postgres yedekleri | 30 gün | R2 |
| Onay kayıtları (hesap silindikten sonra) | Hukukçuyla belirlenecek | identity |

### Loglar ve gözlemlenebilirlik

- Log, metrik ve trace'lere kişisel içerik yazılmaz: mesaj metni, konum,
  e-posta, token, gizli anket oyları.
- Kullanıcı konumu (check-in, yakındaki tayfalar) hiçbir log veya trace'e
  yazılmaz.
- **Kullanıcı konumu ile paylaşılan yer ayrımı (2026-11-09):** Cihazın o
  anki konumu (check-in, yakındaki tayfalar araması) yalnızca istek
  sırasında kullanılır; veritabanına, log'a, denetim kaydına, olaylara ve
  hata mesajlarına hiçbir zaman girmez. Kullanıcının bilerek girip
  paylaştığı bir yer (etkinlik buluşma noktası, konvoy rotası, av yeri)
  içeriktir ve saklanabilir; görünürlüğü içeriğin kendi kuralıyla sınırlanır
  (av yeri `self` ise yalnızca sahibine döner) ve bu koordinatlar da log'a
  ve denetim kaydına yazılmaz. CLAUDE.md kesin kural 3 bu ayrımla okunur.
- IP adresleri yalnızca güvenlik amaçlı kayıtlarda (giriş, rate limit ihlali)
  tutulur.
- OpenTelemetry Collector'da, kişisel veri taşıyabilecek alanları temizleyen
  bir işlem adımı bulunur; servis kodundaki kurala ek bir güvenlik katmanıdır.
- Yedekler 30 gün tutulduğu için silinen veri yedeklerde en geç 30 gün daha
  kalır. Bu durum aydınlatma metninde belirtilir.

### Tayfa yaşam döngüsü

- **Temizlik:** Haftalık iş; açıldıktan sonraki 7 gün içinde 3 üyeye
  ulaşamayan ya da 30 gün hareketsiz kalan tayfa uyarılır (`warned`), uyarıdan
  sonra 7 gün içinde aktivite olmazsa **arşivlenir** (`archived`). Silinmez.
- **Silme:** Yönetici talebinden sonra 72 saat beklenir; bu sürede herhangi bir
  yönetici iptal edebilir. Süre dolunca soft delete uygulanır, 30 gün sonra
  kalıcı silinir. `tayfa.deleted` olayı chat'e iletilir.
- **Askıya alma:** Platform tarafından askıya alınan tayfa salt okunur olur.

### Platform yönetim paneli

Erişim güvenliği Notion'daki tasarımla aynıdır; katmanlar bağımsızdır:

1. **Ayrı adres:** `admin.tayfam.app`. Ana uygulamada bağlantısı yoktur.
2. **Cloudflare Access:** Bu adrese ulaşmadan önce Cloudflare Zero Trust
   kimlik doğrulaması istenir; yalnızca platform sahibinin hesabına izin
   verilir.
3. **Sunucu tarafı doğrulama:** gateway, admin adresine gelen her istekte
   Cloudflare Access'in imzalı JWT'sini (`Cf-Access-Jwt-Assertion`) Cloudflare'in
   açık anahtarlarıyla doğrular. Başlık yoksa veya imza geçersizse istek
   reddedilir.
4. **Uygulama içi yetki:** Kullanıcı identity'deki `platform_admins`
   tablosunda olmalıdır. Bu tabloya API üzerinden kayıt eklenemez; yalnızca
   identity binary'sinin bir komutuyla eklenir.
5. **Ayrı oturum:** Panel girişinde identity ayrı türde bir **admin token'ı**
   verir (`aud` farklı); ömrü 8 saattir. Uygulama token'ı panelde, panel
   token'ı uygulamada geçmez.
6. **Yıkıcı işlemlerde tekrar doğrulama:** Ban, tayfa silme ve plan değişikliği
   gibi işlemler, son 5 dakika içinde yeniden giriş yapılmış olmasını (token'daki
   `auth_time`) ve bir gerekçe yazılmasını gerektirir.
7. **Denetim kaydı:** Her işlem gerekçesiyle `platform_audit_logs`'a yazılır;
   panelden silinemez.

Panelin göremedikleri de Notion'daki gibidir: birebir mesaj içerikleri, gizli
anket oyları, konum, üyesi olunmayan tayfaların içerikleri (şikâyet edilen
içerik hariç). Kullanıcı adına işlem yapma (kimliğe bürünme) yoktur.

**Notion'da mimariye bırakılan iki karar:**

- **Panel teknolojisi:** Sunucu tarafında render edilen sade bir Go
  uygulaması (`html/template` + htmx). Ayrı bir Flutter web uygulaması
  kurulmaz.
- **Panel API'sinin yeri:** gateway içinde, yalnızca `admin.tayfam.app`
  adresinden gelen istekleri karşılayan ayrı bir yönlendirici. İç servisler
  panel işlemleri için ayrı gRPC servisleri sunar (örneğin
  `tayfam.core.v1.AdminService`); bu servisleri yalnızca geçerli bir admin
  token'ı taşıyan gateway istekleri çağırabilir.

### Hukuki yapılacaklar

Bu ADR hukuki görüş değildir. Notion'daki aydınlatma metni taslağında
listelenen konular yayından önce bir hukukçuya danışılarak netleştirilir:
yurt dışı aktarım dayanağı (Cloudflare, Google, Apple, FCM), VERBİS kaydı
yükümlülüğü, 16–18 yaş arası kullanıcılar, onay kayıtlarının saklama süresi,
meşru menfaat denge testi ve kullanım koşulları metni.

## Gerekçe

- KVKK işlemleri (silme, dışa aktarma) birden fazla servise yayıldığı için
  identity bir orkestratör olarak davranır; her servis kendi verisinden
  sorumludur.
- Panel için sunucu tarafı render; ayrı bir istemci uygulaması, derleme ve
  dağıtım hattı gerektirmez, saldırı yüzeyini küçük tutar ve sıkı bir içerik
  güvenlik politikası (CSP) uygulamayı kolaylaştırır.
- Panelin gateway'de ayrı bir yönlendirici olarak çalışması, ADR-0002'deki dört
  servis sınırını korur. Cloudflare Access ile admin token'ı birbirinden
  bağımsız iki katmandır.

## Sonuçlar

- identity; onay, dışa aktarma, platform admin, yaptırım ve platform denetim
  kaydı tablolarını içerir.
- Her servis, `user.deleted` olayını işleyen idempotent bir metod ve
  `ExportUserData` metodu sunar.
- Cloudflare'de `admin.tayfam.app` için bir Access uygulaması ve politikası
  tanımlanır.
- Aydınlatma metni, yedeklerin 30 gün saklanması ve R2'deki kapak görsellerinin
  herkese açık olabilmesi (ADR-0007) ile güncellenir.

## Değerlendirilen alternatifler

- **Hesap silmede 30 gün geri alma süresi:** Kullanıcı hatasına karşı koruma
  sağlar, ama kişisel verilerin hemen temizlenmesi ilkesiyle çelişir.
- **Tayfa temizliğinde silme:** Daha az veri tutar, ama Notion'da arşivleme
  kararı alınmıştır; yanlışlıkla temizlenen bir tayfa geri getirilebilir.
- **Panel için ayrı Flutter web uygulaması:** İstemci kodunu yeniden kullanır,
  ama ayrı bir dağıtım ve daha büyük bir saldırı yüzeyi getirir.
- **Panel için ayrı bir admin servisi:** Daha net izolasyon, ama ADR-0002'deki
  servis sayısını artırır; Cloudflare Access ve ayrı token türü yeterli
  izolasyonu sağlar.
