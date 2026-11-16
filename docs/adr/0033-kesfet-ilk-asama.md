# ADR-0033: Keşfet ilk aşama — arkadaş ağı ve güvenlik ağı (v2)

- **Durum:** Önerildi (platform sahibinin kararlarına dayanır; ayrıntı
  seçimleri "Platform sahibine sorular" bölümündedir)
- **Tarih:** 2026-11-16
- **Karar veren:** Ürün kararları platform sahibinindir (Notion "Kötüye
  Kullanım ve Genç Kullanıcı Güvenliği" → "Keşfet tasarımı" 2026-11-12 ve
  §5; "Faz 2 (v2) — Medya ve Sosyal" §6, §9; issue #210; 2026-11-15/16
  sohbet kararları, mimari oturumunun aktarımıyla). Bu ADR onların
  mimariye taşınmasıdır.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0026](0026-tayfa-istatistikleri-ve-vitrin.md),
  [ADR-0028](0028-etkinlik-fotograf-albumu.md),
  [ADR-0030](0030-arkadaslik-puani-ve-arkadas-onerisi.md); issue #210

## Bağlam

Platform sahibinin kararları:

- **İlke:** Keşfet güven vermez, bilgi verir. "Güvenilir" ya da "onaylı"
  alanı veya ifadesi yoktur; yalnızca olgular gösterilir (kuruluş tarihi,
  etkinlik sayısı, doğrulanmış katılım sayısı).
- **İlk aşama arkadaş ağıdır:** arkadaşların katıldığı ya da katılacağı,
  Keşfet'e açık tayfaların etkinlikleri ("Ayşe ve 2 arkadaşın bu
  etkinlikte"). Yabancı tayfaların açık listelenmesi yoktur.
- **Tayfanın Keşfet'e çıkması:** yönetici açar (varsayılan kapalı), en az 5
  üye, platform panelinde onay (panel yalnızca meta veriyi denetler).
  Anlık tayfalar (#209) hiçbir zaman çıkmaz.
- Keşfet'te yalnızca **halka açık yerde buluşan** etkinlikler; özel adres
  Keşfet'te yoktur.
- **Güvenlik ağı:** Keşfet'ten gelen üye yöneticiye etiketli görünür; bu
  üyelerden gelen şikâyet tayfayı askıya almadan önce Keşfet'ten düşürür;
  etkinlik sonrası "Her şey yolunda mıydı?" geri bildiriminde ciddi
  olumsuz yanıt tayfayı inceleme bitene kadar düşürür; **güvenlik kişisi**
  (katılımcı etkinliği bir arkadaşıyla paylaşır, "döndüm" olmazsa ona
  haber gider; yürüyüş alarmının genellenmiş hali).
- **Bayrak:** kod bu turda tamamlanır, sunucuda kapalı gelir; açma kararı
  hukukçu görüşmesinden sonra platform sahibindedir. Bayrak açılmadan
  panel onayı ve ayarlar çalışabilir.
- **2026-11-16 ek kararı:** sıralamada gizli arkadaşlık puanı
  (ADR-0030) kullanılabilir; puan hiçbir cevapta ya da imleçte dönmez;
  profillemesi kapalı kullanıcı için puan kullanılmaz. Arkadaşın
  katılımını göstermek onun katılım geçmişi görünürlük ayarına uyar.

Mevcut şema: `tayfas.visibility` (`private` / `listed`), `events.discoverable`
ve `memberships.joined_via = 'discover'` Faz 1'den beri vardır ve "yalnızca
saklanır" durumdadır. Vitrin istatistikleri v3'tür (ADR-0026); bu ADR
yalnızca olgu sayılarını döndürür.

## Karar

### 1. Bayrak: `DISCOVER_ENABLED`, kapı core'da

- core ortam değişkeni `DISCOVER_ENABLED` (`true` / `false`, varsayılan
  `false`; başka değer servisi başlatmaz). Bayrağı **verinin sahibi core**
  denetler; gateway bilmez (ADR-0011: bir katman aşılsa da kapı durur).
- Bayrak kapalıyken kullanıcıya dönük Keşfet uçları **hiçbir veri okumadan**
  `ERROR_REASON_FEATURE_DISABLED` döner; REST'te **404,
  `type: feature-disabled`**. 404 seçildi: kapalı bir özelliğin kaynağı
  yoktur ve istemci ayrı `type` sayesinde girişi gizler; ayrı bir 403 ya da
  boş liste, istemcinin "henüz kimse yok" ile "özellik kapalı"yı
  ayırmasını engellerdi.
- **Bayraktan bağımsız çalışanlar** (karar: "panel onayı ve ayarlar
  çalışabilir"): tayfa yöneticisinin Keşfet ayarı ve başvurusu, organizatörün
  etkinlikteki `discoverable` işareti, panel kuyruğu (onay/ret), üyeliğin
  Keşfet etiketi ve otomatik düşürme kuralları.
- **Bayrağa bağlı olanlar:** Keşfet akışı, Keşfet'ten katılım isteği,
  "Her şey yolunda mıydı?" geri bildirimi ve güvenlik kişisi (seçme,
  döndüm, paylaşılanlar listesi).
- **Alarm işi bayraktan bağımsızdır:** bayrak kapalıyken yeni güvenlik
  kişisi seçilemez, ama bayrak sonradan kapatılırsa önceden seçilmiş
  kişilere alarm yine gider (bir güvenlik sinyali yarıda kesilmez).

### 2. Veri (core, migration `00028`)

Bütün girdiler (tayfa, üyelik, etkinlik, katılım, arkadaşlık, puan,
şikâyet) core'dadır; yeni veri de core'dadır (ADR-0009). Yeni tablolar:

| Tablo | Alanlar | Not |
| --- | --- | --- |
| `tayfa_discover` | `tayfa_id` PK FK (CASCADE), `status` (`pending` / `approved` / `rejected` / `dropped`), `drop_reason` (`member_report` / `negative_feedback` / `member_removed`; yalnızca `dropped`'da), `requested_at`, `requested_by` 🔗, `reviewed_at`, `reviewed_by` 🔗 (panel yöneticisi), `status_changed_at`, `masked_status` (`pending` / `approved`), `masked_until`, `created_at`, `updated_at` | Başvuru ve panel kararı. Satır ilk başvuruda açılır, tayfa silinene kadar durur. `masked_*` yalnızca `dropped`'da (§7) |
| `discover_joins` | `tayfa_id` FK (CASCADE), `user_id` 🔗, `joined_at`, `left_at` | PK (tayfa, user). Keşfet'ten gelip aktif üye olanın kalıcı işareti (§7); trigger yazar |
| `event_feedback` | `event_id` FK (CASCADE), `user_id` 🔗, `answer` (`ok` / `not_ok` / `serious`), `created_at`, `updated_at` | PK (event, user). Serbest metin **yok** |
| `event_safety_contacts` | `event_id` FK (CASCADE), `user_id` 🔗, `contact_user_id` 🔗, `grace_minutes` (15–1440, varsayılan 60), `returned_at`, `alert_sent_at`, `created_at`, `updated_at` | PK (event, user); kişi kendini seçemez. **Konum alanı yok** |

Mevcut alanların anlamı netleşir:

- `tayfas.visibility = 'listed'`: **yöneticinin Keşfet isteği**. Tek başına
  listelemez; panel onayı da gerekir.
- `events.discoverable`: **organizatörün işareti**: "bu etkinlik halka
  açık bir yerde buluşur ve Keşfet'te görünebilir". Yalnızca yüz yüze
  (`in_person`) etkinlikte anlamlıdır; çevrim içi etkinlik işaretli olsa da
  listelenmez.
- `memberships.joined_via = 'discover'`: Keşfet'ten gelen katılım isteği
  (§5).

### 3. Listelenebilirlik: tek fonksiyon

Bir tayfanın Keşfet'te görünebilmesi tek yerde, `tayfa_discover_listable(
tayfa_id)` SQL fonksiyonunda tanımlıdır; "görünür arkadaş" kuralı (§4) da
tek yerde, `discover_friend_visible(izleyici, arkadaş, etkinlik, an)`
fonksiyonundadır. Akış, katılım isteği ve veritabanı trigger'ı aynı
fonksiyonları çağırır (ADR-0030'daki
`friend_suggestion_allowed` gibi kurallar sorgular arasında kaymaz):

- `visibility = 'listed'` (yönetici açmış);
- `tayfa_discover.status = 'approved'` (panel onaylamış, düşürülmemiş);
- tayfa canlı ve yazılabilir: `status` `active` ya da `warned`, askıda
  değil, silme bekleme süresinde değil, silinmemiş;
- **en az 5 aktif üye** (listeleme anında; üyeler ayrılırsa tayfa
  kendiliğinden düşer, yeniden onay gerekmez).
- **Anlık tayfa hiçbir zaman** (§12).

Bir etkinliğin akışa girmesi ayrıca: `discoverable`, `mode = 'in_person'`,
`status = 'scheduled'`, silinmemiş, **bitmemiş** (`ends_at > now`) ve
**30 gün içinde başlıyor**.

### 4. Akış: `ListDiscoverFeed`

Tek sayfa, en çok 50 öğe; **imleç yoktur** (sıralama puandan türediği için
imleç puanı sızdırabilirdi; 2026-11-16 kararı). Bir etkinlik akışa yalnızca
izleyicinin **en az bir görünür arkadaşı** varsa girer.

**Görünür arkadaş** (izleyiciye göre): kabul edilmiş arkadaşlık, iki yönden
de engel yok, hesap silinmemiş, etkinliğe `going` demiş ya da katılımı
`attended`, o tayfanın aktif üyesi ve **katılım geçmişi görünürlüğü
`private` değil**. Gerekçe: ProfileService'in tablosunda arkadaşlar
`tayfa` ve `public` ayarında geçmişin tamamını görür, `private`'ta hiçbir
şey görmez; Keşfet aynı kuralı uygular. Görünmeyen arkadaş **sayılmaz da**
("ve 2 arkadaşın" sayısı yalnızca görünenlerdir); sayıya katmak gizli
arkadaşın katılımını sızdırırdı.

İzleyicinin aktif, bekleyen, davetli ya da banlı üyesi olduğu tayfaların
etkinlikleri akışta yoktur (keşif değildir; banlıya tayfanın varlığı
sızmaz).

**Sıralama:** (1) görünür arkadaşlarla arkadaşlık puanlarının toplamı,
azalan; (2) görünür arkadaş sayısı, azalan; (3) başlangıç, artan;
(4) etkinlik kimliği. Puan **yalnızca izleyicinin ve ilgili arkadaşın
profillemesi açıksa** kullanılır (`social_settings`); değilse o çiftin
katkısı 0'dır. Puan, toplam ya da sıra numarası hiçbir alanda dönmez.

**Öğe içeriği:**

- Etkinlik: kimlik, tür, başlık, başlangıç/bitiş, il/ilçe. **Açıklama,
  buluşma noktası adı ve koordinat dönmez** (özel adres Keşfet'te yok;
  açıklama serbest metindir ve adres taşıyabilir). Tayfaya katılınca
  mevcut uçlardan okunur.
- Tayfa: kimlik, ad, açıklama (panelin denetlediği meta veri), il,
  **olgular**: kuruluş tarihi, etkinlik sayısı ve doğrulanmış katılım
  sayısı. Sayılar ADR-0026 §1'deki **donmuş** etkinliklerden (bitişinden
  7 gün geçmiş, iptal edilmemiş, silinmemiş) her istekte hesaplanır;
  düzeltme penceresi sayıyı oynatmaz. Üye sayısı, kapak, vitrin
  istatistiği yoktur (v3).
- Arkadaşlar: en çok 3 görünür arkadaşın kimliği, adı, avatarı (sıra:
  puan, sonra ad; puan dönmez) ve görünür arkadaş sayısı. **Not:** öğe
  içindeki arkadaş sırası puandan türer; izleyici birden çok öğeyi
  karşılaştırarak arkadaşlarının kabaca sırasını çıkarabilir. Bu, arkadaş
  listesinin (ADR-0030) zaten verdiği sıranın aynısıdır, yeni bilgi
  değildir; puanın kendisi, toplamı ya da sıra numarası dönmez.

**Etkinlik modülü:** tayfanın `events` modülü kapalıysa etkinlikleri
akışta yoktur (`discover_friend_visible` `tayfa_modules`'a bakar). Modül
kapalıyken üyeler de etkinliği göremez; Keşfet daha fazlasını göstermez.

**Etkinlik başlığı ve kötüye kullanım filtresi:** akış etkinlik başlığını
döndürür; başlık panelin onayladığı meta veri değildir ve yeniden
incelemeyi tetiklemez (her etkinlik başlığı için panel incelemesi
ölçeklenmez). Şemada henüz kötüye kullanım sözlüğü (`moderation_terms`,
Notion "Kötüye Kullanım ve Genç Kullanıcı Güvenliği") yoktur; o tablo
geldiğinde başlık Keşfet'e girmeden filtreden geçer. **Bayrak açılmadan
önceki ön koşuldur** (Sonuçlar); o zamana kadar riski sınırlayan:
listelenen tayfa panelden geçmiştir, öğe yalnızca arkadaşı olan izleyiciye
görünür, başlık uzunluğu sınırlıdır ve şikâyet + düşürme akışı açıktır.

### 5. Keşfet'ten katılım: `JoinFromDiscover`

- Yalnızca tayfa **o anda izleyicinin akışında** ise (aynı SQL koşulu;
  listelenebilir tayfa + uygun etkinlik + görünür arkadaş). Değilse
  `TAYFA_NOT_FOUND`: listelenen tayfalar yabancılara açık değildir; kimliği
  bilen biri doğrudan istek gönderemez.
- **Her zaman bekleyen istek** (`pending`, `joined_via = 'discover'`), tayfa
  `open` olsa da. Yönetici isteği kaynağıyla görür ve onaylar. Bir
  `BEFORE INSERT` trigger'ı bunu veritabanında da zorlar: `discover`
  katılımı yalnızca `pending` ve yalnızca listelenebilir tayfaya eklenir.
- Mevcut katılım isteği bildirimi (`join_request`) gider. Hız sınırı:
  kullanıcı başına saatte 10 (kodla katılımla aynı).

### 6. Etiket

`Membership.joined_via_discover` (REST `Member.joined_via_discover`):
yalnızca çağıran tayfanın moderatörü ya da yöneticisiyse doldurulur
(susturma bitişi gibi); diğer üyeler kimin Keşfet'ten geldiğini görmez.
Bekleyen istekler zaten `JOIN_SOURCE_DISCOVER` taşır.

### 7. Otomatik düşürme ve yeniden inceleme (veritabanında)

Kurallar trigger'dadır; hangi uçtan gelirse gelsin atlanamaz:

- **Keşfet işareti (`discover_joins`):** `joined_via = 'discover'` üyelik
  aktif olunca trigger işareti açar (yeniden katılımda tazeler); aktif
  üyelik bitince (ayrılma, çıkarılma, ban, hesap silme) `left_at` yazar.
  İşaret, üyelikten bağımsızdır: üyelik satırı silinse de durur. Gerekçe
  (2026-11-16 incelemesi): yalnızca üyeliğe bakılsaydı yönetici Keşfet
  üyesini çıkararak hem şikâyetini hem geri bildirimini engelleyebilir,
  güvenlik ağını kapatabilirdi.
- **Eski Keşfet üyesi:** üyeliği bitmiş, işareti olan kişi `left_at`'ten
  sonraki **30 gün** boyunca yalnızca (a) tayfanın kendisini şikâyet
  edebilir (`target_type = tayfa`, doğrudan platforma gider) ve
  (b) katıldığı (`attended`) etkinliğe §9'daki pencerede geri bildirim
  verebilir. Tayfanın başka hiçbir içeriğine erişemez; bu iki yol üyelik
  kontrolüne değil işarete dayanır (IDOR'a karşı: kimlik, işaret ve
  katılım satırıyla doğrulanır). Kodla ya da davetle gelip ayrılan eski
  üye bu yolu kullanamaz.
- **Keşfet üyesinden şikâyet:** `reports` satırı eklendiğinde şikâyet edenin
  o tayfada işareti varsa (hâlâ üye ya da son 30 günde ayrılmış), tayfanın
  `approved` ya da `pending` başvurusu `dropped` (`member_report`) olur.
  Şikâyetin kendisi normal akışına devam eder; askı ayrı bir panel
  kararıdır. Böylece düşürme askıdan **önce** gelir.
- **Ciddi olumsuz geri bildirim:** `event_feedback.answer = 'serious'`
  yazıldığında (ekleme ya da değişiklik) aynı düşürme (`negative_feedback`).
- **Keşfet üyesinin çıkarılması:** tayfa yöneticisi (ya da moderatörü)
  Keşfet'ten gelmiş bir üyeyi, o üyenin geri bildirim penceresi açıkken
  (`going` ya da `attended` olduğu bir etkinliğin bitişi son 7 gün içinde
  ya da gelecekte) çıkarır ya da banlarsa tayfa düşer (`member_removed`).
  Bu, üyelik servisinde çıkarma/ban ile aynı transaction'dadır (trigger
  ayrılma ile çıkarmayı ayırt edemez). Kişinin kendi isteğiyle ayrılması
  ya da penceresi kapanmış bir üyenin çıkarılması düşürmez.
- **Meta veri değişikliği:** onaylı tayfanın adı, açıklaması, ili ya da
  ilçesi değişirse başvuru `pending`'e döner (panel meta veriyi onaylamıştı;
  değişen meta veri onaysızdır).
- Düşürülen tayfa **yalnızca panel kararıyla** döner (§8). Yöneticinin
  Keşfet'i kapatıp açması `dropped`'ı ya da `rejected`'ı silmez; `rejected`
  bir tayfanın yöneticisi yeniden açarsa başvuru `pending` olur.
- **Tayfa yöneticisine düşürme nedeni söylenmez:** yönetici durumu
  "incelemede" görür. Keşfet'ten gelen üyeler azdır; "Keşfet üyesi şikâyet
  etti" demek şikâyet edeni ele verirdi.
- **Gecikmeli görünürlük (maske):** nedeni söylemek kadar **zamanı** da ele
  verir: etkinlikten bir saat sonra "incelemede" görmek, o etkinliğe
  katılan birkaç kişiyi işaret eder. Bu yüzden geri bildirim ve Keşfet
  üyesi şikâyetiyle düşürmede **listeden çıkış hemen**, yöneticilerin
  gördüğü durum ise gecikmelidir: trigger önceki durumu `masked_status`'a,
  açılma anını `masked_until`'a yazar; o ana kadar yönetici önceki durumu
  ("listede" ya da "inceleme bekliyor") görür. `masked_until`:
  - geri bildirimde: etkinliğin geri bildirim penceresinin kapanışı
    (bitiş + 7 gün), en erken ertesi UTC gün dönümünden sonraki gün dönümü;
  - Keşfet üyesi şikâyetinde: ertesi UTC gün dönümünden sonraki gün dönümü
    (24–48 saat; günlük toplu iş gibi). Şikâyetin kendisi zaten
    platforma gider, tayfa yöneticisi görmez; maske yalnızca listeden
    düşme zamanını bulanıklaştırır.
  Panel gerçeği hemen görür (kuyrukta `dropped`). Panel kararı (onay/ret)
  maskeyi kaldırır. Yöneticinin Keşfet'i kapatıp açması maskeyi açmaz.
  `member_removed` maskelenmez: yönetici kendi işlemini zaten bilir; hemen
  "incelemede" görmesi bu kuralı öğretir. Kalan sızıntı: yönetici olmayan
  bir hesapla akışa bakan biri tayfanın akıştan düştüğünü görebilir; bu
  hesabın tayfada üyeliği olmamalı ve izleyicinin arkadaşı etkinliğe
  gidiyor olmalı (§4), bu yüzden kabul edildi.

### 8. Platform paneli

Mevcut panel uçları ve sayfaları altında ("Keşfet" sekmesi):

- **Kuyruk** (`ListDiscoverQueue`): `pending` ve `dropped` başvurular,
  eskiden yeniye. Yalnızca meta veri: ad, açıklama, il/ilçe, kuruluş,
  aktif üye sayısı, Keşfet'ten gelen üye sayısı, açık platform şikâyeti
  sayısı, son 90 günün `not_ok` / `serious` geri bildirim **sayıları**
  (kimin verdiği yok), durum, düşürme nedeni, başvuru zamanı. Tayfa içeriği
  görünmez (ADR-0008, "Platform Yönetim Paneli").
- **Onay / ret** (`ApproveDiscover`, `RejectDiscover`): diğer panel
  değişiklikleri gibi gerekçe (10–1000) ve son 5 dakikada giriş ister;
  değişiklik, tayfa denetim kaydı (yönetici kimliği olmadan) ve platform
  denetim kaydı tek transaction'dadır. Onay `pending` ya da `dropped`'tan,
  ret her durumdan yapılır ("keşfetten çıkarma" = ret). Panel tayfa
  detayındaki "keşfet görünürlüğü" bu durumu gösterir.
- Onay yalnızca meta veri incelemesidir; arayüz ve sözleşme bunu "onaylı"
  diye kullanıcıya **yansıtmaz** (ilke: güven vermez).

### 9. "Her şey yolunda mıydı?"

- Kim: etkinliğe katılımı `attended` olan (organizatör işaretlemesi
  dahil), `discoverable` yüz yüze bir etkinliğin katılımcısı; tayfanın
  aktif üyesi ya da eski Keşfet üyesi (§7). Etkinlik modülünün açık olması
  aranmaz: geri bildirim bir güvenlik sinyalidir, modülü kapatmak onu
  susturmamalı.
- Ne zaman: bitişten bitiş + 7 güne kadar (düzeltme penceresiyle aynı);
  bu sürede cevap değiştirilebilir.
- Cevap: `ok` / `not_ok` / `serious`. Serbest metin yoktur (tutmadığın veri
  çalınamaz); anlatmak isteyen mevcut şikâyet akışını kullanır, istemci
  `serious`'ta şikâyet ekranını önerir.
- Kim görür: yalnızca kişinin kendisi. Panel yalnızca sayıları görür (§8);
  tayfa yöneticisi hiç görmez.
- `serious` tayfayı düşürür (§7).

### 10. Güvenlik kişisi

- **Seçme:** etkinliğe `going` demiş aktif üye, yüz yüze, iptal edilmemiş,
  **bitmemiş** bir etkinlik için **bir** kabul edilmiş arkadaşını (iki
  yönden engel yok) seçer; `grace_minutes` 15–1440 (varsayılan 60).
  Alarm gitmeden önce değiştirilebilir ya da kaldırılabilir; **alarm
  gittikten sonra kaldırılamaz** (`SAFETY_CONTACT_CLOSED`): güvenlik kişisi
  alarma göre davranıyordur, paylaşım onun listesinden kaybolmamalı.
  Etkinliğin Keşfet'te olması gerekmez (yürüyüş alarmının
  genellemesidir); seçme bayrağa bağlıdır.
- **"Döndüm":** kişi `returned_at`'i işaretler; alarm durur.
- **Kapatma bayrağa bağlı değildir:** "döndüm" ve kaldırma (alarm
  gitmeden) bayrak kapalıyken, kişi tayfadan ayrıldıktan sonra ya da
  etkinlik modülü kapalıyken de çalışır. Yalnızca çağıranın kendi
  satırına, etkinlik bu tayfanınsa, dokunurlar; başka hiçbir şey okumaz ve
  yetki vermezler. Gerekçe: kurulmuş bir güvenlik ağı her zaman
  kapatılabilmeli, yoksa güvenlik kişisine yanlış alarm gider.
- **Alarm** (`safety-alerts` işi, dakikada bir; yürüyüş alarmının modeli):
  `returned_at` yok, `alert_sent_at` yok ve `ends_at + grace_minutes`
  geçmişse **yalnızca seçilen kişiye** `safety_alert` bildirimi gider ve
  `alert_sent_at` aynı transaction'da yazılır (bir kez). Gönderim anında
  ikisi hâlâ arkadaş, aralarında engel yok ve katılımcı tayfanın **aktif
  üyesiyse** gider; değilse gitmez. Planlı etkinlikte katılımcının hâlâ
  `going` olması gerekir. Etkinlik iptal ya da silindiyse ve kişi
  check-in yapmadıysa gitmez; check-in yaptıysa yine gider (kişi dışarıda
  olabilir). Check-in alarmı durdurmaz: varmak dönmek değildir.
- **Bildirim:** yeni `safety_alert` türü (chat migration `00018`): tayfasız,
  aktörsüz, hedef `user` = katılımcı. **İçerik ve konum taşımaz.** Push
  gider ve susturma uygulanmaz (yürüyüş alarmı gibi).
- **Güvenlik kişisinin gördüğü** (`ListSafetyShares`): kendisini seçen
  kişilerin, bitişinden 24 saat geçmemiş ya da alarmı son 7 günde gitmiş
  etkinlikleri: katılımcının adı ve avatarı, tayfa adı, etkinlik başlığı,
  başlangıç/bitiş, buluşma noktası **adı**, il/ilçe, `returned_at`,
  `alert_sent_at`. **Koordinat dönmez.** Paylaşım yalnızca hâlâ geçerliyse
  listelenir: ikisi arkadaş, engel yok, katılımcı tayfanın aktif üyesi ve
  `going`; etkinlik silinmemiş ve iptal edilmemiş; tayfa silinmemiş ya da
  askıda değil. Alarm gitmişse ve kişi check-in yaptıysa etkinlik
  sonradan iptal ya da silinmiş olsa da görünür (alarmın bağlamı).
  Katılımcı bu paylaşımı bilerek
  yapar; güvenlik kişisi tayfa üyesi olmadığı için diğer bilgiler
  (açıklama, katılımcılar, kanal) ona açılmaz.
- Seçilen kişiye seçildiği anda bildirim gitmez (kötüye kullanımla
  bildirim yağdırılmasın; liste yeterli).

### 11. Konum ve log

- Cihaz konumu hiçbir tabloya, olaya, bildirime, log'a girmez (ADR-0008).
  Akış koordinat döndürmez; güvenlik kişisi yalnızca buluşma noktasının
  **adını** görür (içerik, katılımcının paylaşımı).
- İşler yalnızca sayı log'lar (gönderilen alarm sayısı). Kimlik, cevap,
  puan log'a, metriğe, trace'e girmez.

### 12. Anlık tayfalar (#209, ADR-0032)

Anlık tayfa hiçbir zaman listelenmez. Kural üç katmandadır:

1. `tayfa_discover_listable` `tayfas.kind = 'standard'` ister (ADR-0032'nin
   alanı, migration 00027).
2. 00027'deki `tayfas_instant_private` CHECK'i anlık tayfayı
   `visibility = 'private'` tutar; yöneticinin Keşfet isteği ona yazılamaz.
3. `GetTayfaDiscover` ve `SetTayfaDiscover` anlık tayfada `TAYFA_NOT_FOUND`
   döner.

"Bu ekiple tayfa kur" ile kalıcıya dönen tayfa standart olur ve normal
kurallara (yönetici açar, panel onaylar, en az 5 üye) girer.
`TestInstantTayfaNeverListed` tür alanı tek başına (CHECK düşürülmüş olsa
bile) tayfayı dışarıda tuttuğunu sabitler.

### 13. Saklama, hesap silme, dışa aktarma

| Veri | Süre |
| --- | --- |
| `tayfa_discover` | Tayfa kalıcı silinene kadar (CASCADE). Hesap silinince `requested_by` / `reviewed_by` NULL |
| `event_feedback` | 365 gün (günlük `retention` işi); etkinlik kalıcı silinince CASCADE |
| `event_safety_contacts` | Etkinlik bitişinden 30 gün sonra silinir (günlük `retention` işi) |
| `discover_joins` | Üyelik sürerken durur; üyelik bitişinden (`left_at`) 30 gün sonra silinir (günlük `retention` işi). Tayfa kalıcı silinince CASCADE |

- **Hesap silme** (`user.deleted`): kişinin geri bildirimleri, iki yöndeki
  güvenlik kişisi satırları (seçtiği ve seçildiği) ve Keşfet işaretleri
  silinir.
- **KVKK dışa aktarma** (ADR-0021): yeni bölüm `CORE_EXPORT_SECTION_DISCOVER
  = 23`, `core/discover.json`: kişinin geri bildirimleri (etkinlik kimliği,
  cevap, zamanlar) ve seçtiği güvenlik kişileri (etkinlik, kişi kimliği,
  tolerans, döndüm ve alarm zamanları) ve Keşfet işaretleri (tayfa,
  katılış ve ayrılış zamanı). Başkasının kişiyi seçtiği satırlar
  o başkasının verisidir, girmez.

### 14. İzinler (ADR-0004, tek izin tablosu)

| İşlem | En düşük rol | Susturulmuşken |
| --- | --- | --- |
| `tayfa.discover_manage` (Keşfet ayarı ve başvuru durumu) | admin | — |
| `event.feedback` (kendi geri bildirimi) | member | evet (hiçbir şey yayımlamaz, güvenlik sinyali) |
| `event.safety_contact` (kendi güvenlik kişisi, döndüm) | member | evet (güvenlik sinyali asla reddedilmez) |

Keşfet etiketini görme mevcut `member.list_pending` iznine (moderatör)
bağlıdır.

### 15. API sözleşmesi

| Uç | gRPC | Bayrak |
| --- | --- | --- |
| `GET /v1/discover/events` | `DiscoverService.ListDiscoverFeed` | evet |
| `POST /v1/discover/tayfas/{tayfa_id}/join` | `JoinFromDiscover` | evet |
| `GET` / `PUT /v1/tayfas/{tayfa_id}/discover` | `GetTayfaDiscover` / `SetTayfaDiscover` | hayır |
| `GET` / `PUT /v1/tayfas/{tayfa_id}/events/{event_id}/feedback` | `GetEventFeedback` / `SetEventFeedback` | evet |
| `GET` / `PUT` / `DELETE /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact` | `GetSafetyContact` / `SetSafetyContact` / `ClearSafetyContact` | evet |
| `POST /v1/tayfas/{tayfa_id}/events/{event_id}/safety-contact/return` | `MarkReturned` | evet |
| `GET /v1/me/safety-shares` | `ListSafetyShares` | evet |
| Panel "Keşfet" sekmesi | `AdminService.ListDiscoverQueue` / `ApproveDiscover` / `RejectDiscover` | hayır |

Yeni hata kodları: `FEATURE_DISABLED` (404 `feature-disabled`),
`DISCOVER_TOO_FEW_MEMBERS` (409; yönetici 5 üyeden azken açmaya çalışırsa),
`FEEDBACK_CLOSED` (409; pencere dışı ya da Keşfet'e uygun olmayan
etkinlik), `SAFETY_CONTACT_CLOSED` (409; bitmiş, iptal, çevrim içi
etkinlik ya da alarmı gitmiş kayıt), `SAFETY_CONTACT_NOT_FOUND` (404;
güvenlik kişisi seçilmeden "döndüm"). Mevcutlar: `NOT_ATTENDED`,
`EVENT_NOT_GOING`, `NOT_FRIENDS`, `ALREADY_MEMBER`, `ALREADY_PENDING`,
`TAYFA_NOT_FOUND`.

Tayfa yöneticisinin gördüğü durum: `off` (kapalı), `pending` (incelemede),
`listed` (onaylı ve açık), `under_review` (düşürüldü; neden yok),
`rejected`. "approved" kelimesi istemciye gitmez.

## Gerekçe

- **Bayrak core'da:** gateway aşılsa ya da yanlış yapılandırılsa bile veri
  sahibi kapalı özelliği çalıştırmaz.
- **Kurallar veritabanında:** listelenebilirlik tek fonksiyonda, düşürme
  trigger'da; yeni bir uç ya da iş kuralı atlayamaz (ADR-0011).
- **Arkadaş zorunluluğu katılımda da:** "yabancı tayfa açık listelenmez"
  kararı yalnızca listede değil katılım isteğinde de uygulanır.
- **Az veri:** geri bildirimde metin yok, güvenlik alarmında konum ve
  içerik yok, akışta adres ve koordinat yok, düşürme nedeni yöneticiye
  söylenmez.

## Sonuçlar

- core'a dört tablo (`discover_joins` dahil), iki fonksiyon/trigger grubu,
  bir iş, bir servis; chat'e bir bildirim türü; panele bir sekme eklenir.
- `DISCOVER_ENABLED` dağıtımda tanımlanmaz (varsayılan kapalı); açmak tek
  bir ortam değişkenidir.
- Topluluk Kuralları'na "Keşfet'teki etkinlik halka açık yerde buluşur"
  maddesi eklenmeli (Notion; platform sahibi).
- Aydınlatma metnine Keşfet (arkadaşın katılımının arkadaşlara gösterilmesi,
  güvenlik kişisi, geri bildirim) eklenmeden bayrak açılmamalıdır
  (hukukçu listesi).
- Kötüye kullanım sözlüğü (`moderation_terms`) gelip Keşfet'teki etkinlik
  başlığı ondan geçmeden bayrak açılmamalıdır (§4).

## Platform sahibine sorular

Aşağıdakiler bu ADR'de gerekçeyle seçildi; platform sahibi farklı karar
verirse ilgili bölüm değişir.

1. **Arkadaşın görünmesi:** `activity_visibility` `private` değilse
   (`tayfa` ya da `public`) arkadaş Keşfet'te görünür (§4). Daha sıkı
   seçenek: yalnızca `public`. Varsayılan `tayfa` olduğu için sıkı seçenek
   akışı neredeyse boşaltır.
2. **Akış penceresi:** bitmemiş ve 30 gün içinde başlayan etkinlikler;
   geçmiş etkinlikler ("katıldığı") akışa girmez, arkadaşın `attended`
   olması yalnızca süren etkinlikte anlam taşır.
3. **Keşfet katılımı her zaman onaylı** (açık katılımlı tayfada da) (§5).
4. **Güvenlik kişisi bayrağa bağlı** ve Keşfet dışı etkinliklerde de
   kullanılabilir (§10). Bayraktan ayrı açılması istenirse ayrı bir bayrak
   eklenir.
5. **Güvenlik kişisi yalnızca buluşma noktası adını görür**, koordinatı
   görmez (§10). Koordinat istenirse ADR-0008 ayrımına göre (paylaşılan yer
   içeriktir) eklenebilir, ama veri modelindeki "koordinat yalnızca aktif
   üyelere" kuralının istisnası olur.
6. **Tek alarm:** "döndüm" yoksa bitiş + tolerans; check-in yokluğunda
   ayrı bir "varmadı" alarmı yok (yanlış alarm riski).
7. **Reddedilen başvuru için bekleme süresi yok;** yönetici yeniden
   açabilir (kuyruğa yeniden düşer).
8. **Yöneticiye bildirim yok:** onay, ret ve düşürme için yeni bildirim
   türü açılmadı; durum Keşfet ayarında görünür.
9. **Geri bildirim yalnızca `discoverable` etkinliklerde;** tüm
   etkinliklere genişletilmesi ayrı karar.
10. **Eski Keşfet üyesinin süresi 30 gün** ve yalnızca tayfayı şikâyet +
    katıldığı etkinliğe geri bildirim (§7). Daha uzun bir süre ya da
    tayfanın içeriğini (ör. bir mesajı) şikâyet etme ayrı karar.
11. **Maskenin süresi:** geri bildirimde pencere kapanışı, şikâyette 24–48
    saat (§7). Panelin isteği varsa günlük sabit saat de olabilir.
12. **Etkinlik başlığı kötüye kullanım filtresi** `moderation_terms`
    gelince; o zamana kadar bayrak kapalı kalır (§4, Sonuçlar).

## Hukukçu listesi

- **Panel onayı ve yer sağlayıcı konumu:** onay yalnızca meta veri
  incelemesidir (ad, açıklama, il/ilçe; içerik okunmaz). Bunun 5651
  kapsamında yer sağlayıcı konumunu etkileyip etkilemediği (ön denetim =
  içerik sağlayıcıya yaklaşma riski) sorulmalı.
- **16–17 yaş:** yaşa göre özellik kuralı yoktur (ürün ilkesi) ve bu ADR
  yaş kullanmaz. Değerlendirilecek hafif önlemler: organizatörün "18+"
  işareti, Keşfet'ten gelen 18 yaş altı isteğinde yöneticiye uyarı
  (identity `IsMinor` evet/hayır, saklanmadan). 5651 ek m. 4 yaş doğrulama
  yönetmeliğiyle birlikte görüşülmeli; bayrak bu netleşmeden açılmaz.
- **Aydınlatma metni:** arkadaşın katılımının Keşfet'te gösterilmesi,
  sıralamada arkadaşlık puanı, güvenlik kişisine paylaşılan bilgiler, geri
  bildirim.

## Değerlendirilen alternatifler

- **Yalnızca `events.discoverable` + `tayfas.visibility`, ayrı tablo yok:**
  panel kararını, düşürme nedenini ve yeniden incelemeyi tutacak yer olmaz.
- **İmleçli sayfalama:** puan sırasını taşıyan imleç puanı ele verebilirdi;
  ilk aşamada 50 öğe yeterli.
- **Düşürmeyi Go'da yapmak:** yeni bir şikâyet yolu (ör. birebir mesaj ya da
  profil şikâyeti) kuralı atlayabilirdi; trigger her yolu kapsar.
- **Bayrak kapalıyken boş liste:** testte "hiçbir şey dönmüyor" sağlanırdı
  ama istemci kapalı özelliği "kimse yok" diye gösterirdi.
- **Güvenlik alarmında etkinlik bilgisini bildirime koymak:** bildirim
  içerik taşımaz (notify paketi kuralı); bilgi yetkiyle `ListSafetyShares`'tan
  okunur.
