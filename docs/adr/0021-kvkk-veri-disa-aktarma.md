# ADR-0021: KVKK veri dışa aktarma — toplama, şifreleme ve depolama

- **Durum:** Kabul edildi (platform sahibi, 2026-11-11)
- **Tarih:** 2026-11-11
- **Karar veren:** Murat Tarslan (dokuz önerinin hepsi sohbette kabul
  edildi, 2026-11-11)
- **İlgili:** [ADR-0007](0007-dosyalar-ve-medya.md), [ADR-0008](0008-kvkk-ve-platform-yonetimi.md)
  ("Veri dışa aktarma"; bu ADR onu uygular ve aşağıda belirtilen yerlerde
  ayrıntılandırır), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md)
  (`ExportUserData` imzası değişir), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0013](0013-servis-kimligi.md), [ADR-0019](0019-push-bildirimleri.md),
  [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md); issue #144
- **Kaynak:** Notion: Veri Modeli (taslak v14) "Kimlik → data_export_requests",
  "KVKK ve Dayanıklılık Kuralları (v5) → Veri dışa aktarma", "Saklama
  süreleri", "Anket → gizli oy", "Doğum Tarihi (v14) → Veri dışa aktarma",
  "Dosyalar → uploads"; KVKK Aydınlatma Metni (taslak) md. 8 ("Ayarlar →
  Verilerimi indir"); Platform Yönetim Paneli ("KVKK taleplerinin durumu")

## Bağlam

ADR-0008 ve Notion v5 ürün kararını vermiştir: kullanıcı 30 günde en çok bir
talep açar; profil, üyelikler, katılımlar, kendi içerikleri (duyuru,
etkinlik, soru, cevap, kanal mesajları, av kayıtları) ve kendi anket oyları
(gizli anket dahil yalnızca kendi oyu; v14'te ayrıca doğum tarihi ve
görünürlüğü) JSON olarak toplanır, zip'lenir, **şifrelenir**, private R2'ye
yüklenir; 7 gün geçerlidir, kısa ömürlü imzalı linkle indirilir. Birebir
mesajlar girmez (E2E). KVKK talepleri en geç 30 gün içinde yanıtlanmalıdır.
Notion v14'teki tablo: `data_export_requests (id, user_id, status
(pending|processing|ready|expired|failed), upload_id, requested_at,
ready_at, expires_at, downloaded_at)`; uploads notunda "data_export amacı
KVKK işiyle ayrıca eklenecek" yazar.

Bugünkü kodda dışa aktarmanın hiçbir parçası yoktur: `data_export_requests`
tablosu, `ExportUserData` proto'ları, gateway ucu yok; core `uploads.purpose`
yalnızca `avatar | catch_photo | cover` kabul eder (testler `data_export`'u
reddeder); `MediaService` proto'sunda "DATA_EXPORT yalnızca sunucu tarafından
yazılır" notu vardır.

Uygulamaya geçmeden önce kararlaştırılması gerekenler:

1. R2'ye kim yazar? ADR-0008 "identity" der; ADR-0020 ile R2 kimliği ve
   internete R2 çıkışı (`core-egress-r2`) yalnızca core'dadır.
2. "Şifrelenir" ne demektir; tehdit modeli.
3. Boyut, toplama biçimi (sayfalı/akışlı), zaman aşımı, tekrar deneme.
4. Medya zip'e girer mi?
5. (Bu ADR'de bulunanlar) Arka plan işinde kullanıcı token'ı yoktur: identity
   core ve chat'ten veriyi kimin adına ister? Talep ve indirmede yeniden
   kimlik doğrulama gerekir mi? 30 günlük sınır ile "7 gün sonra satır
   temizlenir" nasıl bağdaşır? Hazır bildirimi nasıl gider?

Mevcut durumdan önemli gerçekler:

- identity'nin internete 443 çıkışı zaten vardır (`identity-egress-internet`,
  Google/Apple OIDC için); identity → core ve identity → chat ağ izinleri
  vardır.
- identity, JWT imzalama anahtarını tutar. identity'nin ele geçirilmesi
  zaten her kullanıcının adına token basabilmek demektir; bu ADR identity'ye
  bundan daha geniş bir yetki vermemeli, ama identity'nin bugün **görmediği**
  veriyi (R2'deki medya, kanal mesajlarının açık metni) gereksiz yere
  ona taşımamalıdır.
- Cloudflare R2 API token'ları bucket bazında kısıtlanabilir, **önek
  (prefix) bazında kısıtlanamaz**. Aynı bucket'a ikinci bir token, tüm
  medyayı okuyabilen bir token demektir.
- identity pod'unun bellek sınırı 128 MiB'dır.

## Karar

### 1. R2'ye kim yazar: core imzalar, identity şifreli dosyayı doğrudan yükler

**Seçenekler**

| | A. identity'ye aynı bucket için ayrı R2 token'ı | B. identity'ye ayrı bucket (`tayfam-<ortam>-exports`) ve o bucket'a özel token | C. core imzalı link verir, identity şifreli dosyayı R2'ye doğrudan PUT eder (**öneri**) | D. identity zip'i core'a akıtır, core yazar |
|---|---|---|---|---|
| R2 anahtarı nerede | core + identity | core (medya) + identity (dışa aktarma) | Yalnızca core | Yalnızca core |
| identity ele geçirilirse R2'de ulaşılan | **Tüm medya** (token önekle sınırlanamaz) | Yalnızca dışa aktarma bucket'ı | Yalnızca kendi istediği imzalı linkler (kullanıcı token'ıyla, kullanıcının kendi dışa aktarması) | Aynı |
| core ele geçirilirse | Tüm bucket | Medya; dışa aktarmalar değil | Medya + dışa aktarma **şifreli metinleri** (anahtar identity'de) | Aynı, ama core akış sırasında **açık** veriyi görürdü (identity şifrelemezse) |
| Yeni secret / ağ kuralı | Yeni token, secret | Yeni bucket, lifecycle, token, secret | Yok (yalnızca `exports/` için lifecycle kuralı) | Yok |
| Ek karmaşıklık | Az | Orta (ikinci R2 istemci yapılandırması) | Core'da 4 küçük metod; identity'de host'u sabit PUT istemcisi | İstemci akışlı gRPC (4 MB sınırı), core'da tampon |
| ADR-0007/0020 ile uyum | `data_export` uploads'ta olmaz | `data_export` uploads'ta olmaz | **Uyumlu**: uploads ve R2 core'da, `purpose = data_export` | Uyumlu |

A, en az yetkiyi açıkça ihlal ettiği için elenir. D, verinin core üzerinden
geçmesini ve akışlı RPC'yi gerektirir, C'ye göre kazancı yoktur.

**Öneri: C.** Gerekçe (ADR-0011):

- R2 kimliği tek serviste kalır; platform sahibine yeni token, secret ya da
  ağ kuralı çıkmaz.
- **Anahtar ile şifreli metin ayrı servislerde durur:** şifreli dosya core'un
  R2'sinde, onu açan anahtar identity'nin veritabanında (madde 2). Biri ele
  geçirilince dışa aktarmalar açılmaz; ikisinin birlikte ele geçirilmesi
  gerekir.
- identity R2'de hiçbir şeyi listeleyemez, okuyamaz, silemez; yalnızca core'un
  o yükleme için, o boyutla imzaladığı tek bir PUT linkini kullanır.
- Ayrı bucket (B), core'un da dışa aktarma şifreli metinlerini görmemesini
  sağlardı; şifreleme bunu zaten anlamsız kıldığı için bedeline değmez.
  Trafik ya da hukukçu görüşü ayrı bucket isterse B'ye geçiş yalnızca
  core'un yapılandırmasını değiştirir.

**Akış ve kimlik:** core'un yeni metodları `MediaService` içindedir ve
**kullanıcının kendi token'ıyla** çağrılır (madde 5). core her çağrıda
yüklemenin çağıranın olduğunu, amacın `data_export` olduğunu kendisi denetler.
gateway bu metodlar için uç açmaz. Bir saldırgan çalınmış bir kullanıcı
token'ıyla gateway'i aşıp core'a doğrudan ulaşsa bile yalnızca o kullanıcı
adına, tek bir etkin dışa aktarma yuvasına, sınırlı boyutta şifreli olmayan
çöp yazabilir; hiçbir şey okuyamaz. Bu kabul edilir.

- R2 anahtarı `exports/<rastgele 128 bit>` (core belirler). `incoming/` ve
  `media/`'dan ayrı önek: lifecycle kuralı ayrı uygulanır ve medya kodunun
  hiçbir yolu bu öneki sunmaz.
- PUT linki 5 dakika, yalnızca o anahtar, `application/octet-stream` ve
  bildirilen bayt sayısı için imzalıdır (ADR-0020 madde 4 ile aynı).
- identity yalnızca `https://<hesap>.r2.cloudflarestorage.com/` biçimindeki
  linke yazar, yönlendirme izlemez, istek süresi 30 sn. Ele geçirilmiş bir
  core, identity'ye şifreli dosyayı başka bir adrese yollatamaz (yollatsa da
  şifrelidir).

### 2. Şifreleme: dışa aktarma başına anahtar, uygulama düzeyinde AES-256-GCM, çözme istemcide

**Tehdit modeli.** Korunacak varlık: bir kişinin tayfam'daki neredeyse tüm
verisi (doğum tarihi, gizli anket oyları, av yerleri, kanal mesajları) tek bir
dosyada. Saldırganlar ve yolları:

| # | Tehdit | Yalnızca R2 sunucu tarafı şifreleme (R2 her nesneyi zaten AES-256 ile saklar) | SSE-C (anahtarı her istekte R2'ye vermek) | Uygulama düzeyi, istemci çözer (**öneri**) |
|---|---|---|---|---|
| T1 | `core-r2` token'ının sızması (secret, yedek, log, ele geçirilmiş core) | **Açık metin okunur** | Anahtar yoksa okunamaz | Okunamaz |
| T2 | İmzalı indirme linkinin sızması (ekran görüntüsü, proxy log'u, paylaşma, istemci log'u) | **Okunur** (link ömrü içinde) | Okunamaz (anahtar başlıkta) | Okunamaz (anahtar ayrı gelir) |
| T3 | Cloudflare tarafı / yurt dışı aktarım (KVKK md. 9, hukukçu notu) | Cloudflare açık metni işler | Anahtar her istekte Cloudflare'e gider | **Cloudflare yalnızca şifreli metin görür** |
| T4 | identity veritabanı ya da yedeğinin sızması | Etkisiz (dosya yok) | Anahtar sarılı değilse dosyalar açılır (T1 ile birlikte) | Anahtar sarılıdır (aşağıda); veritabanı tek başına yetmez |
| T5 | Çalınmış oturum (kopyalanmış refresh token) ile talep + indirme | Korumaz | Korumaz | Korumaz → madde 6'daki yeniden doğrulama |
| T6 | identity sürecinin ele geçirilmesi | Korumaz | Korumaz | Korumaz (identity zaten token imzalayabilir; kabul edilen kalan risk) |

SSE-C'de imzalı linkin başlıkları imzaya dahil olduğundan linki imzalayan
core anahtarı görmek zorunda kalır (anahtar ile şifreli metni ayırma kazancı
kaybolur) ve anahtar Cloudflare'e gider. Yalnızca R2'nin kendi şifrelemesi
T1–T3'te hiçbir şey sağlamaz; "şifrelenir" sözünü yalnızca kâğıt üzerinde
karşılar.

**Öneri: uygulama düzeyi şifreleme.**

- identity her talep için `crypto/rand` ile 32 baytlık bir **veri anahtarı**
  üretir. Zip **tek seferde** AES-256-GCM ile mühürlenir (standart kütüphane,
  `cipher.NewGCM`; parça parça şifreleme ya da kendi çerçevemiz yok, kural 4).
  Dosya biçimi: `0x01 (sürüm) || nonce (12 bayt) || şifreli metin + etiket`;
  AAD = `"tayfam/data-export/v1/" || request_id`. Biçim, istemcinin
  çözebilmesi için OpenAPI açıklamasında belgelenir.
- Veri anahtarı identity'de `data_export_requests.wrapped_key` kolonunda
  **sarılı** durur: yeni `identity-export-key` secret'ındaki sürümlü anahtarla
  AES-256-GCM (AAD = request_id + user_id; doğum tarihi şifrelemesinin
  `ParseKeys` biçimi ve sürümleme kalıbı aynen). Böylece T4 (veritabanı ya da
  Barman yedeği) + T1 birlikte bile dosyayı açmaz; üç bağımsız sırrın
  (veritabanı, R2 token'ı, sarma anahtarı) hepsi gerekir.
- Anahtar istemciye yalnızca **indirme linkiyle birlikte**, aynı yanıtta,
  TLS üzerinden ve `Cache-Control: no-store` ile verilir. İstemci dosyayı
  indirir, cihazda çözer, açık zip'i kullanıcının seçtiği yere kaydeder
  (paylaşma menüsü). Kullanıcının elindeki dosya açık zip'tir; bu
  bilinçlidir (veri onundur).
- 7 gün dolunca (ya da hesap silinince) `wrapped_key` NULL yapılır: R2'de bir
  kopya kalsa bile (lifecycle gecikmesi, hata) açılamaz (kripto-silme).

**Değerlendirilip önerilmeyen:** istemcinin talepte bir açık anahtar (X25519)
göndermesi ve veri anahtarının ona sarılması. Sunucuda hiçbir an açılabilir
anahtar kalmaz (T6'ya karşı bile tamamlanmış dışa aktarmalar korunur), ama
istemcide anahtar saklama gerekir, cihaz değişince dışa aktarma kaybolur ve
30 günlük sınır kullanıcıyı bekletir. Pilot sonrası yeniden değerlendirilir.

### 3. Toplama: kullanıcının kendi token'ıyla, talep anında başlayan iş

**Sorun.** Notion "arka plan işi" der; ADR-0008 ve issue "kullanıcının kendi
token'ıyla" der. Erişim token'ı 15 dakikalıktır; zamanlayıcıyla saatler sonra
çalışan bir işte token yoktur.

| | A. Talep anında başlayan iş, kullanıcının token'ıyla (**öneri**) | B. Zamanlanmış arka plan işi, identity'nin servis token'ıyla (`tayfam-identity`) `ExportUserData(user_id)` | C. identity'nin işe özel kısa ömürlü kullanıcı token'ı basması |
|---|---|---|---|
| core/chat'te yeni yetki | Yok: "çağıranın kendi verisi" metodu, `/me` uçları gibi | **Her kullanıcının tüm verisini** dönen servis metodu | Yok, ama identity kullanıcı adına token basar |
| identity ele geçirilirse | Fark yok | Fark yok bugün; ama imzalama anahtarı ileride ayrı bir imzalayıcıya taşınırsa bu metod kalıcı bir "herkesi oku" kapısı olarak kalır | ADR-0013'ün reddettiği "kullanıcı adına işlem" kalıbı |
| Yeniden başlatmaya dayanıklılık | İş yarıda kalırsa talep `failed` olur, kullanıcı tekrar dener (sınırı yemez) | Tam: zamanlayıcı kaldığı yerden devam eder | Tam |
| Karmaşıklık | Az | Orta (Guard listesi, servis yolu) | Orta + yeni token türü |

**Öneri: A.** `POST /v1/me/data-exports` satırı açar, core'da dışa aktarma
yuvasını ayırır (madde 5) ve `202` döner; toplama aynı süreçte hemen başlar,
isteğin token'ını taşıyan ama isteğin iptalinden ayrılmış (detached) bir
context'le. İşin süresi `min(3 dakika, token bitişi − 10 sn)` ile sınırlıdır.
Süre biterse, identity yeniden başlarsa ya da core/chat hata dönerse talep
`failed` olur ve **30 günlük sınırı yemez**; istemci yeniden ister. İstemci
talepten hemen önce token'ını yeniler (OpenAPI'de belgelenir). identity'de
`processing`'de 10 dakikadan uzun kalan talepleri `failed` yapan saklama adımı
yeniden başlatma sonrası artığı temizler; core'daki ayrılmış yuva 24 saatlik
`pending` temizliğine girer.

Notion'daki "arka plan işi" ifadesinden sapma: iş kullanıcının isteğinden
sonra, onu bekletmeden arka planda çalışır; ama zamanlayıcıyla saatler sonra
değil, talep anında çalışır. **Notion'a not düşülmeli.**

ADR-0009'daki `ExportUserData(user_id)` imzası değişir: metod kullanıcı
kimliği almaz, yalnızca doğrulanmış çağıranın (`auth.CallerFrom`) verisini
döner (`ExportMyData`); başkası adına sorulamaz (`CheckDirectMessage`
revizyonuyla aynı ilke). ADR-0009 uygulama PR'ında güncellenir.

### 4. Boyut, akış, zaman aşımı, tekrar deneme

- **gRPC akışı:** core ve chat `ExportMyData`'yı **sunucu akışlı** RPC olarak
  sunar (`stream ExportMyDataResponse`); `auth.ForwardToken` akış yolunu zaten taşır.
  Her parça bir bölümün (`section`, ör. `core/events`) en çok 1 MiB'lık JSON
  kayıtlarıdır; gRPC'nin 4 MB mesaj sınırına takılmaz. Sayfalı unary
  çağrılar her tablo için imleç sözleşmesi gerektirirdi; akış daha sade.
- **Tutarlılık:** her servis kendi verisini tek bir `REPEATABLE READ, READ
  ONLY` transaction'ında okur (servis içi anlık görüntü; servisler arası
  anlık görüntü yoktur, bu kabul edilir).
- **Yazım:** identity parçaları gelir gelmez `archive/zip` ile bellekteki
  tampona sıkıştırır (Deflate), sonra tamponu **yerinde** mühürler
  (`Seal(buf[:0], …)`); açık metnin ikinci bir kopyası tutulmaz.
- **Üst sınır:** sıkıştırılmış zip en çok **16 MiB** (sabit; veritabanı
  CHECK'leri de aynı sınırı tutar). JSON 5–10 kat sıkıştığı için bu 80 MiB'ın
  üstünde JSON demektir; bir kullanıcının metin verisinin buna yaklaşması
  beklenmez. identity aynı anda **bir** dışa aktarma yürütür (semafor; diğerleri
  `pending`'de sırasını bekler), bellek sınırı 128 MiB'da kalır.
  Sınır aşılırsa talep `failed` (`too_large`) olur, sınırı yemez ve bir
  uyarı log'u (yalnızca talep kimliği ve boyut sınıfı) platform sahibinin
  elle ele almasını tetikler (KVKK'nın 30 günlük süresi içinde e-postayla).
  Alternatif (geçici dosya, `emptyDir`) ilk gerçek ihtiyaçta değerlendirilir.
- **Tekrar deneme:** iş içinde core ve chat çağrıları geçici hatalarda
  (`UNAVAILABLE`, `DEADLINE_EXCEEDED`) en çok 3 kez, kısa geri çekilmeyle
  denenir; R2 PUT'u bir kez daha denenir (aynı link 5 dk geçerli). Sonra
  `failed`. Otomatik yeniden zamanlama yoktur (token yok, madde 3).
- **Hız sınırı:** "30 günde bir" sınırı `failed` dışındaki talepleri sayar
  (`pending | processing | ready | expired`). Aynı kullanıcının eşzamanlı iki
  talebi `users` satırında `FOR UPDATE` ile sıraya girer.

### 5. Medya: zip'e girmez, liste girer

| | A. Yalnızca liste (**öneri**) | B. Dosyalar da zip'e | C. Liste + dosya başına 7 günlük link |
|---|---|---|---|
| Boyut | Küçük | Kullanıcı başına kota 500 MiB → bellek sınırı, geçici disk, uzun iş | Küçük |
| identity'ye etkisi | Yok | identity'nin medyayı **okuması** gerekir (core'dan GET linki + indirme): identity bugün medya görmez | Uzun ömürlü linkler ADR-0020'nin 1 saat ilkesini bozar |
| Kullanıcı erişimi | Kendi dosyaları ADR-0020'ye göre **her zaman** görünür: istemci "Fotoğraflarımı da indir" ile mevcut `POST /v1/media/urls`'ten link alıp indirir | Zip'te | Linkler 7 gün |

**Öneri: A.** (Kabul edildi; listenin KVKK md. 11 için yeterliliği **pilot
öncesi hukukçuya sorulacak**.) Zip'te `core/uploads.json`: kullanıcının yüklediği her dosya
için `upload_id`, amaç, bağlı olduğu içerik (`attached_type`, `attached_id`),
ölçüler, oluşturma zamanı. Dosyalar EXIF'siz olarak zaten kullanıcının
cihazından gelmiştir. İstemci isterse aynı ekranda kendi dosyalarını mevcut
uçtan indirir; sunucuya yeni kod gerekmez. KVKK md. 11'in "bilgi talep etme"
hakkının yalnızca listeyle karşılanıp karşılanmadığı **hukukçu sorusudur**
(Notion'daki hukukçu notlarına eklenmeli).

### 6. Yeniden kimlik doğrulama

Dışa aktarma, bir hesabın neredeyse tüm verisini tek seferde dışarı çıkarır;
çalınmış bir oturumun (T5) en değerli hedefidir.

- **Öneri:** indirme linki (anahtarla birlikte) yalnızca son 10 dakikada
  sağlayıcıyla (Google/Apple) açılmış oturuma verilir — hesap silmedeki
  `deletion.ReauthWindow` kuralının aynısı (token yenilemesi yetmez). Değilse
  `403 reauthentication-required`.
- Talep açmak yeniden doğrulama istemez: saldırgan talep açsa bile dosyayı
  alamaz; kullanıcı hazır dosyayı kendisi yeniden giriş yaparak indirir,
  yuvası boşa gitmez.
- Alternatif: talepte de istemek (daha sıkı, kullanıcıya iki giriş) ya da hiç
  istememek (T5 açık kalır).

### 7. Kapsam (zip'in içeriği)

`manifest.json` (biçim sürümü `1`, oluşturma zamanı, bölüm listesi ve kayıt
sayıları) ve bölüm başına bir JSON dosyası. Diğer kullanıcılara yalnızca
**kimlikle** referans verilir (ad yok); tayfa ve etkinlik adları içeriktir,
yer alır.

| Servis | Bölümler | Kaynak |
|---|---|---|
| identity | profil (ad, avatar `upload_id`, dil, referans kodu, kayıt zamanı), **doğum tarihi ve görünürlüğü**, onay kayıtları, cihazlar (platform, son görülme), dışa aktarma talepleri | Notion v5 + v14; onay ve cihaz **bu ADR'nin önerisi** (aydınlatma metni md. 2 bunları işlenen veri sayar) |
| core | üyelikler ve roller, katılım yanıtları, katılım geçmişi, kendi duyuruları, etkinlikleri, soruları, cevapları, kendi anketleri, **kendi oyları (gizli dahil)**, av kayıtları (paylaştığı av yeri dahil), arkadaşlıklar, engellemeler, açtığı şikâyetler (kendi yazdığı kanıt metni dahil), yüklemeler listesi | Notion v5; arkadaşlık, engelleme, şikâyet, yükleme listesi **bu ADR'nin önerisi** |
| chat | yazdığı kanal mesajları (açık metin, chat çözer), bildirim tercihleri | Notion v5; tercihler **önerisi** |

Girmeyenler: birebir mesajlar ve DM meta verisi (ADR-0008), push token'ları
(bir kimlik bilgisi; yalnızca sayı ve platform yazılır), refresh token'lar,
IP kayıtları, bildirim listesi (90 günlük geçici kayıt), denetim kayıtları
(`audit_logs`, `platform_audit_logs`), moderasyon işaretleri ve başka
kullanıcıların verisi.

Not: chat'in kanal mesajlarını açıp identity'ye vermesi, identity sürecinin
bugün görmediği açık metni (yalnızca o kullanıcının kendi mesajlarını, iş
süresince bellekte) görmesi demektir. Kabul edilir: identity zaten o
kullanıcı adına token basabilir.

### 8. Veri modeli

**identity** (`db/identity/migrations/00009_data_export.sql`):

```
data_export_requests
  id             uuid PK (UUIDv7, sunucuda)
  user_id        uuid NOT NULL REFERENCES users ON DELETE CASCADE
  status         text NOT NULL CHECK (status IN ('pending','processing','ready','expired','failed'))
  failure_reason text NULL CHECK (failure_reason IN ('unavailable','too_large','timeout','failed'))  -- yalnızca failed'da
  upload_id      uuid NULL        -- core uploads kimliği; FK yok (ADR-0002)
  wrapped_key    bytea NULL       -- sürüm || nonce || sarılı veri anahtarı; yalnızca ready'de dolu
  size_bytes     bigint NULL
  requested_at   timestamptz NOT NULL
  started_at     timestamptz NULL
  ready_at       timestamptz NULL
  expires_at     timestamptz NULL -- ready_at + 7 gün
  downloaded_at  timestamptz NULL -- ilk link verildiğinde
  CHECK: status ile zaman damgaları ve wrapped_key tutarlı
  UNIQUE (user_id) WHERE status IN ('pending','processing')  -- aynı anda tek etkin talep
  INDEX (user_id, requested_at DESC)
```

Notion v14'e göre eklenenler: `failure_reason`, `wrapped_key`, `size_bytes`,
`started_at`. **Notion'a not düşülmeli.** `identity_app` rolü bu tabloda
SELECT/INSERT/UPDATE/DELETE alır (ADR-0015).

**Saklama (ADR-0008 tablosunu ayrıntılandırır):** 7 gün dolunca satır
`expired` olur, `wrapped_key` ve `upload_id` NULL'lanır (dosya core'da
silinir). Satırın kendisi **30 gün** sonra silinir, çünkü "30 günde bir"
sınırı `requested_at`'e bakar; satırda o arada kişisel veri kalmaz (yalnızca
durum ve zamanlar). Issue'daki "7 gün sonra dosya ve satır temizleniyor"
kabul kriteri bu ayrımla okunur. Hesap silinince satırlar transaction'da
hemen silinir (CASCADE değil, silme kodunda açıkça; `users` satırı 30 gün
kaldığı için).

**core** (`db/core/migrations/00018_data_export_uploads.sql`):

- `uploads.purpose` CHECK'ine `data_export`. Bu amaçta `tayfa_id` NULL,
  varyant yoktur (tek nesne: `content_type = application/octet-stream`),
  `width/height` NULL, kotaya ve saatlik yükleme sınırına **sayılmaz**,
  bağlanmaz (`attached_*` NULL), `POST /v1/uploads` ve `/v1/media/urls` bu
  amacı **hiçbir zaman** kabul etmez ya da sunmaz (testle sabit).
- Kullanıcı başına tek etkin dışa aktarma yuvası: `UNIQUE (user_id) WHERE
  purpose = 'data_export' AND status IN ('pending','ready')`.
- Boyut ≤ 16 MiB (CHECK), `expires_at` (hazır olunca + 7 gün). `media-cleanup`
  işçisi süresi dolanı R2'den ve tablodan siler; `user.deleted` zaten tüm
  yüklemeleri siler (`media.UserDeleted`).

**chat:** yeni bildirim türü (madde 9). Tablo değişikliği yoktur.

### 9. Bildirim

- `NotificationType.NOTIFICATION_TYPE_DATA_EXPORT_READY = 21`, hedef türü
  yeni `NOTIFICATION_TARGET_TYPE_DATA_EXPORT`, `target_id` = talep kimliği,
  tayfa yok, aktör yok.
- identity, talebi `ready` yaptığı transaction'da outbox'a
  `notification.requested` yazar; chat'in `UserEventService.ApplyUserEvent`'i
  yeni bir `oneof` alanı (`DataExportReady`) kazanır (yalnızca
  `tayfam-identity`, idempotent; ADR-0009'daki "identity de
  `notification.requested` yayınlar" ilk kez burada uygulanır).
- Push çeviri anahtarları (ADR-0019 madde 4): `push_data_export_ready_title`,
  `push_data_export_ready_body`; `loc-args` yok; `data`: `notification_id`,
  `type`, `target_type`, `target_id`. Android kanalı `tayfam_general`,
  öncelik normal. Kişisel bildirim olduğu için `mentions` seviyesinde de geçer
  (ADR-0019 madde 3 listesine eklenir).
- Başarısız talep için bildirim yoktur; istemci talep ekranında durumu
  `GET` ile görür.

### 10. Log ve gizlilik

Dışa aktarma içeriği, veri anahtarı (açık ya da sarılı), sarma anahtarı,
imzalı linkler, R2 anahtarı ve SHA'lar log'a, metriğe, olaya, hata mesajına
ve denetim kaydına girmez. Log yalnızca talep kimliği, durum, hata sebebi,
bölüm adları ve sayıları, boyut sınıfı ve HTTP/gRPC durum kodu taşır. Bu,
identity, core ve chat'te tüm yolları log yakalayarak geçen testlerle
sabittir (ADR-0019 madde 10 kalıbı).

### 11. Yapılandırma ve platform sahibinin işi

- **identity:** `IDENTITY_DATA_EXPORT_KEYS` (`identity-export-key` secret'ı,
  `<sürüm>:<base64 32 bayt>,…`, isteğe bağlı). Yoksa dışa aktarma kapalıdır:
  identity başlar, uçlar `503 unavailable` döner. Bozuksa identity başlamaz.
  Repo `scripts/new-export-key.sh`'i (`new-birthdate-key.sh` kalıbı) hazırlar.
- **core:** `core-r2` yoksa dışa aktarma yuvası `UNAVAILABLE` döner; identity
  talebi açarken bunu senkron görür ve `503 unavailable` döner (satır
  açılmaz).
- **Ağ:** değişiklik yok. identity'nin 443 çıkışı zaten var; core'un R2
  çıkışı `core-egress-r2` ile R2 açık ortamda var.
- **Platform sahibinin adımları (ortam başına, R2 açılan ortamda):**
  1. R2 bucket'ına ikinci lifecycle kuralı: ad `exports-8d`, önek `exports/`,
     **8 gün** sonra sil (core'un 7 günlük silmesinin yedeği; hata varsayımı).
  2. `./scripts/new-export-key.sh <ortam>` (anahtar platform sahibinin
     bilgisayarında üretilir, doğrudan sops'a verilir).
  3. Yeni R2 token'ı, bucket ya da secret **gerekmez**.

## API sözleşmesi taslağı

**REST (gateway → identity `DataExportService`):**

| Uç | Açıklama | Başarılı | Hatalar |
|---|---|---|---|
| `POST /v1/me/data-exports` | Talep aç, toplama hemen başlar. Gövde: `{client_request_id}` (tekrar gönderimde aynı talep döner) | `202` `DataExport` | `409 data-export-in-progress` (etkin talep var), `429 data-export-limit`, `503 unavailable`. Hangi talebin sürdüğü ve bir sonraki talep zamanı listeden okunur |
| `GET /v1/me/data-exports` | Son 30 günün talepleri, yeni önce | `200 {data_exports: [DataExport], next_request_at?}` | — |
| `GET /v1/me/data-exports/{data_export_id}` | Tek talep (istemci `processing` iken yoklar) | `200 DataExport` | `404 data-export-not-found` (başkasının talebi de) |
| `POST /v1/me/data-exports/{data_export_id}/download` | 5 dakikalık indirme linki ve anahtar | `200 {url, expires_at, key (base64), size_bytes, format: "aes-256-gcm-v1"}`, `Cache-Control: no-store` | `403 reauthentication-required`, `404 data-export-not-found`, `409 data-export-not-ready`, `410 data-export-expired`, `503 unavailable` |

`DataExport`: `{id, status: pending|processing|ready|expired|failed,
failure_reason?, requested_at, ready_at?, expires_at?, downloaded_at?,
size_bytes?}`. Yeni problem türleri: `data-export-not-found`,
`data-export-in-progress`, `data-export-limit`, `data-export-not-ready`,
`data-export-expired`. Rate limit: kullanıcı başına bu uçlara dakikada 10
istek (yoklama dahil).

**gRPC:**

- `tayfam.identity.v1.DataExportService`: `CreateDataExport`,
  `ListDataExports`, `GetDataExport`, `GetDataExportDownload` (kullanıcı
  token'ı).
- `tayfam.core.v1.MediaService` (kullanıcı token'ı; identity çağırır, gateway
  uç açmaz): `ReserveExportUpload(data_export_id) → {upload_id}` (yuva
  ayırır, tekrarında aynı yuva; R2 yoksa `UNAVAILABLE`; kullanıcının eski
  etkin dışa aktarma yuvası silinir, çünkü identity aynı anda tek talep
  yürütür), `GetExportUploadUrl(upload_id, size_bytes) → {url, headers,
  expire_time}` (boyut bir kez yazılır, trigger),
  `CompleteExportUpload(upload_id) → {}` (HEAD ile boyut denetimi, `ready`,
  `expires_at`), `GetExportDownloadUrl(upload_id) → {url, expires_at}`
  (yalnızca sahibi, `ready`, süresi dolmamış).
- `tayfam.core.v1.ExportService.ExportMyData(ExportMyDataRequest) returns
  (stream ExportMyDataResponse)` ve aynısı `tayfam.chat.v1.ExportService`.
  `ExportMyDataResponse {<Servis>ExportSection section; repeated bytes
  records}`: her kayıt bir JSON
  nesnesi. Bölüm adları ve alanları sözleşmedir: proto'da sabit
  (`section` için enum) ve JSON alanları `docs/data-export-format.md`'de
  sürümlüdür. Bölüm başına tipli proto mesajı (onlarca mesaj) yerine JSON
  seçildi: dışa aktarmanın biçimi zaten JSON'dur ve alanları sahibi servis
  bilir; tipli proto'ya geçmek yalnızca proto'yu büyütür.
- `tayfam.chat.v1.UserEventService.ApplyUserEvent`: yeni `oneof` alanı
  `DataExportReady {data_export_id}`.

## Gerekçe

- Anahtar identity'de, şifreli metin core'un R2'sinde: tek bir bileşenin ele
  geçirilmesi dışa aktarmaları açmaz (ADR-0011 madde 3); hiçbir servise yeni
  R2 kimliği verilmez (madde 2).
- Uygulama düzeyi şifreleme "şifrelenir" sözünü T1–T3'te gerçekten karşılar
  ve yurt dışı aktarım (R2) için savunulabilir bir konum verir.
- Kullanıcının kendi token'ıyla toplama, core ve chat'te "herhangi bir
  kullanıcının tüm verisi" diye bir kapı açmaz.
- Medyayı dışarıda bırakmak boyutu sınırlı, işi tek geçişli ve bellekte
  tutar; kullanıcı kendi dosyalarına mevcut uçla zaten ulaşır.

## Sonuçlar

- identity: `dataexport` paketi (talep, iş, sarma), `DataExportService`,
  saklama işine iki adım (7 gün expire + anahtar silme, 30 gün satır silme,
  takılı `processing`), silme koduna talep satırları.
- core: `uploads` amacı, dört medya metodu, `ExportService`, `media-cleanup`'a
  `data_export` süresi.
- chat: `ExportService`, yeni bildirim türü ve `ApplyUserEvent` alanı.
- gateway: dört uç, problem türleri.
- ADR-0009 (`ExportUserData` → `ExportMyData`, imza), ADR-0008 saklama
  tablosu (satır 30 gün, anahtar 7 gün) ve ADR-0019 `mentions` listesi
  uygulama PR'ında güncellenir; Notion v14'e `data_export_requests` ek
  kolonları, uploads `data_export` kuralları ve "talep anında başlayan iş"
  notu düşülür.
- İstemci (Flutter): AES-256-GCM çözme (tek seferde, ≤ 16 MiB), yeniden giriş
  akışı, `push_data_export_ready_*` çeviri anahtarları, "Fotoğraflarımı da
  indir" (isteğe bağlı).
- Platform paneli "KVKK taleplerinin durumu" (Notion) ayrı bir iştir;
  `GetUserStats`'taki "dışa aktarma uygulanmadı" notu o işte kalkar.
- Uygulama sözleşme önce olmak üzere üç PR'a bölünebilir: (1) proto, OpenAPI,
  migration'lar; (2) core ve chat `ExportMyData` + core depolama metodları;
  (3) identity iş akışı, gateway, bildirim.

## Değerlendirilen alternatifler

Her kararın seçenekleri yukarıda ilgili maddededir. Özetle reddedilenler:
identity'ye aynı bucket'ta R2 token'ı (önekle sınırlanamaz), ayrı dışa
aktarma bucket'ı (şifrelemeyle kazancı küçük, platform işi büyük), zip'i core
üzerinden akıtmak, yalnızca R2'nin kendi şifrelemesi ve SSE-C, istemci açık
anahtarına sarma (pilot sonrası), servis token'ıyla zamanlanmış toplama,
identity'nin kullanıcı adına token basması, medyayı zip'e koymak ya da 7
günlük linkler, sayfalı unary toplama, bölüm başına tipli proto mesajları.
