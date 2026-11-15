# ADR-0028: Arkadaşlık puanı ve arkadaş önerisi

- **Durum:** Önerildi
- **Tarih:** 2026-11-15
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §4, issue #208). Bu ADR onların mimariye
  taşınmasıdır; belirsiz kalan ayrıntılar aşağıda gerekçesiyle seçildi ve
  "Platform sahibine sorular" bölümünde listelendi.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0013](0013-servis-kimligi.md),
  [ADR-0019](0019-push-bildirimleri.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md); issue #208

## Bağlam

Platform sahibinin kararları (Notion §4, issue #208):

- **Puan:** iki kullanıcının son 12 ayda check-in ile doğrulanmış ortak
  katılımı; eskiyen katılımın ağırlığı azalır. Kullanıcıya gösterilmez,
  hiçbir API cevabında dönmez; yalnızca sıralama sinyalidir.
- **Öneri:** arkadaş olmayan biriyle en az 3 doğrulanmış ortak katılım;
  kullanıcı başına haftada en fazla 1 öneri; engellenen, şikâyet edilen/eden
  ve reddedilen tekrar önerilmez; **18 yaş sınırının iki tarafı arasında
  öneri yapılmaz.** Bu bir özellik kısıtı değildir (Notion "Kötüye Kullanım
  ve Genç Kullanıcı Güvenliği": yaşa göre özellik kuralı yok); algoritma bu
  bağlantıyı önermez.
- **Ayarlar:** "Profilleme" (varsayılan açık) kapatılınca kişinin puanları
  silinir, öneri ve sıralama durur. "Beni önerme" ayrı ayardır (varsayılan
  kapalı).
- **Bildirim:** uygulama içi bildirim (öneri satırı + Arkadaş ekle / Kapat).
- Profilleme aydınlatma metnine eklenir (hukukçu listesi).

Kısıtlar: doğum tarihi yalnızca identity'dedir ve şifrelidir; diğer
servislere yalnızca "18 altı mı" (evet/hayır) gider (ADR-0012, Veri Modeli
v15 "Hiçbir yerde saklanmayanlar"). Katılım, arkadaşlık, engel ve şikâyet
core'dadır (ADR-0009). identity'nin `MinorService.IsMinor` metodu zaten
vardır; yalnızca core'un ServiceAccount'u servis token'ıyla çağırabilir
(ADR-0013).

## Karar

### 1. Veri sahipliği: core

Puan, öneri ve iki ayar core'dadır: girdilerin hepsi (katılım, arkadaşlık,
engel, şikâyet, istek sonucu) core'un verisidir ve öneri listesi bunlarla
her okumada yeniden süzülür. Yeni tablolar (migration `00023`):

| Tablo | Alanlar | Not |
| --- | --- | --- |
| `friend_scores` | `user_low_id`, `user_high_id`, `score` (> 0), `shared_count` (≥ 1), `last_shared_at`, `computed_at` | PK (low, high), low < high. Her gün baştan hesaplanır. Hiçbir API cevabına, olaya, log'a, metriğe girmez |
| `friend_suggestions` | `user_id`, `suggested_user_id`, `created_at`, `dismissed_at`, `source` (`suggested` / `blocked`) | PK (user, suggested); kendini öneremez. Öneri satırı ve "Kapat" kararı; `blocked` engellemede yazılan, baştan kapatılmış kayıttır (listelenmez, haftalık sınıra sayılmaz, dışa aktarmaya öneri olarak girmez) |
| `social_settings` | `user_id` PK, `profiling_enabled` (varsayılan true), `hide_from_suggestions` (varsayılan false), `updated_at` | Satır yalnızca kişi bir ayarı değiştirince açılır; yoksa varsayılanlar geçerlidir |

Kullanıcı kimlikleri identity'nindir; FK yoktur, `user.deleted` tüketicisi
temizler (ADR-0002).

### 2. Puan: günlük, baştan hesaplama

- **Formül:** ortak katılım başına ağırlık `0,5 ^ (yaş_gün / 180)`, puan
  bunların toplamıdır. Yarı ömür 180 gün: bugünkü katılım 1, 6 ay önceki
  0,5, 12 ay önceki 0,25 sayılır. 365 günden eski katılım hiç sayılmaz.
  Yaş etkinliğin başlangıcından (`events.starts_at`) ölçülür.
- **"Doğrulanmış ortak katılım":** iki kişinin aynı etkinlikte
  `event_rsvps.attendance = 'attended'` olması. Silinmiş ve iptal edilmiş
  etkinlik, gelecekteki etkinlik sayılmaz. Yöntem ayrımı yapılmaz
  (konum, online, yürüyüş çıkışı ve organizatör düzeltmesi aynı sayılır;
  organizatör düzeltmesi check-in'in resmî düzeltme yoludur, Veri Modeli v15
  "Check-in"). Bkz. soru 1.
- Profillemesi kapalı, hesabı silinmiş ya da platformda banlı kullanıcı için
  puan hesaplanmaz.
- **Hesaplama:** core'un `friend-suggestions` arka plan işi günde bir kez
  tabloyu tek transaction'da siler ve yeniden yazar. Olayla artımlı
  güncelleme seçilmedi: yaşlanma (ağırlığın her gün değişmesi), 12 aylık
  pencereden düşme, katılım düzeltmesi, etkinlik silme ve ayar değişikliği
  artımlı modelde ayrı ayrı ele alınmalıydı; baştan hesaplama bunların
  hepsini tek, idempotent bir sorguyla doğru yapar. Bugünkü ölçekte (tek
  node, pilot) maliyeti önemsizdir; büyürse kullanıcı aralıklarına bölünür.
- **Kullanım:** Puan şimdilik yalnızca öneri adaylarının sırasını belirler.
  Arkadaş listesi ve davet ekranında sıralama sinyali olarak kullanılması ayrı
  iştir (soru 4); o zaman da yalnızca sıra döner, puan dönmez.

### 3. Öneri üretimi

Aynı iş, puandan sonra:

1. **Aday:** `shared_count ≥ 3` olan çiftler, puana göre azalan sırada,
   kişi başına en çok 5 aday. Elenenler:
   - arasında arkadaşlık ya da bekleyen istek olanlar (her iki yön);
   - iki yönden herhangi birinde engel olanlar;
   - biri diğerini şikâyet etmişse (`reports.reporter_id` ↔
     `subject_user_id`, her iki yön; şikâyet saklandığı sürece, 1 yıl);
   - reddedilmiş arkadaşlık isteği olanlar (`friend_request_outcomes`
     `declined`, her iki yön; 90 gün tutulur);
   - aynı kişiye daha önce önerilmiş olanlar (satır varken) ve karşı tarafın
     bu kişiyi "Kapat"la reddettiği çiftler: **"Kapat" çift düzeyindedir**,
     A, B'yi kapattıysa B'ye de A önerilmez;
   - "Beni önerme" diyenler;
   - alıcı son 7 gün içinde öneri aldıysa alıcının bütün adayları
     (haftada en fazla 1).
2. **Yaş:** iş, alıcı ve adayları için identity'ye `IsMinor` sorar (tek
   çağrıda en çok 6 kimlik, servis token'ıyla; ADR-0013). Öneri yalnızca iki
   tarafın da cevabı **varsa ve aynıysa** yapılır (ikisi de 18 altı ya da
   ikisi de 18 ve üstü). Cevap yoksa (bilinmeyen kullanıcı, okunamayan
   tarih) ya da identity'ye ulaşılamazsa öneri yapılmaz: güvenli taraf
   "önerme"dir. Cevap yalnızca bu karar için bellekte kullanılır;
   **hiçbir tabloya, olaya, log'a yazılmaz**. Yaş ve doğum tarihi core'a hiç
   gelmez; yalnızca istenen kimlikler için evet/hayır gelir.
3. **Yazma:** uygun ilk aday için `friend_suggestions` satırı ve aynı
   transaction'da `friend_suggestion` bildirimi (`notification.requested`,
   alıcı yalnızca öneriyi alan, aktör yok, hedef önerilen kullanıcı).
   Transaction içinde haftalık sınır, engel, arkadaşlık ve "Beni önerme"
   yeniden denetlenir. Birden fazla pod aynı işi advisory kilidiyle
   güvenle çalıştırır.

Yaş sınırı üretim anında denetlenir. Açık öneri en çok 30 gün listelenir;
bu sürede birinin 18'e girmesi kabul edilen bir kenar durumdur (iki taraf
o zaman da yaşıt sayılabilecek kadar yakındır, öneri yalnızca alıcıya
görünür ve başka bir yetki açmaz).

### 4. Öneri listesi, "Arkadaş ekle" ve "Kapat"

- `GET /v1/me/friend-suggestions`: alıcının son 30 gündeki kapatılmamış
  önerileri; kişi başına ad, avatar ve öneri zamanı. Liste her okumada
  yeniden süzülür (arkadaş ya da istek olmuşsa, engel, şikâyet, karşı
  tarafın "Beni önerme"si, silinmiş/banlı hesap). **Puan, ortak katılım
  sayısı ve gerekçe dönmez** ("3 etkinlikte birlikteydiniz" de yok: sayı
  puanın bir parçasıdır).
- "Arkadaş ekle" mevcut `POST /v1/me/friend-requests` ucudur; istek
  gönderilince öneri listeden düşer.
- `DELETE /v1/me/friend-suggestions/{user_id}`: "Kapat". Satıra
  `dismissed_at` yazılır; çift bir daha önerilmez. Öneri yoksa da 204 döner
  (kim olduğu sızmaz, tekrar güvenlidir).

### 5. Ayarlar

`GET` / `PATCH /v1/me/social-settings`: `profiling_enabled` ("Profilleme",
varsayılan `true`) ve `hide_from_suggestions` ("Beni önerme", varsayılan
`false`). Ayarlar core'dadır (identity'nin hesap ayarları değil), çünkü
etkiledikleri veri core'dadır ve tek transaction'da silinmelidir.

- **Profilleme kapatılınca** aynı transaction'da: kişinin bütün puan
  satırları silinir; kişiye yapılmış ve kişinin önerildiği açık öneriler
  silinir. Sonraki hesaplamalar kişiyi dışarıda bırakır; kişi öneri almaz ve
  kimseye önerilmez. Kişinin kendi "Kapat" kayıtları silinmez: bunlar
  kişinin isteğidir ve profilleme yeniden açılırsa reddettiği kişinin geri
  gelmesini önler. Yeniden açılınca puan bir sonraki günlük hesaplamada
  oluşur.
- **"Beni önerme" açılınca** kişinin önerildiği açık öneriler silinir; kişi
  aday olmaz. Kendisi öneri almaya devam eder.

### 6. Engelleme ve hesap silme

- **Engelleme** (aynı transaction'da): çiftin puan satırı ve iki yöndeki açık
  önerileri silinir; engelleyenden engellenene bir "Kapat" kaydı yazılır.
  Böylece engel kaldırılsa da çift bir daha önerilmez ("engellenen tekrar
  önerilmez"). Engellenen kişiye hiçbir şey söylenmez.
- **Şikâyet:** şikâyet satırı aday sorgusunda ve listede doğrudan elemedir;
  şikâyet akışında ayrıca bir yazma yapılmaz.
- **Hesap silme** (`user.deleted`): kişinin puanları, iki yöndeki bütün
  öneri satırları ve ayarları silinir.

### 7. Saklama

| Veri | Süre |
| --- | --- |
| `friend_scores` | Her gün baştan yazılır; 12 ayda ortak katılımı kalmayan çift kendiliğinden düşer |
| `friend_suggestions` (açık) | 30 gün listelenir; satır 365 gün sonra günlük işle silinir (365 gün içinde aynı kişi tekrar önerilmez) |
| `friend_suggestions` (kapatılmış) | Hesap silinene kadar (kişinin "bir daha önerme" isteği; yalnızca iki kimlik ve zaman) |
| `social_settings` | Hesap silinene kadar |

### 8. Bildirim: uygulama içi, push yok

Yeni bildirim türü `friend_suggestion` (hedef `user`, tayfasız, aktörsüz).
chat bildirimi listeler ama **push göndermez**: `enqueue_push` trigger'ı bu
tür için push işi açmaz (chat migration `00016`). Gerekçe: Notion "uygulama
içi bildirim" diyor; öneri acil değildir, haftada en fazla birdir ve
algoritmik bir öneriyi kilit ekranına taşımak profillemeyi kullanıcının
haberi olmadan görünür kılar. Push istenirse yalnızca trigger değişir.

### 9. KVKK dışa aktarma (ADR-0021)

İki yeni bölüm:

- `core/social_settings.json`: tek kayıt; iki ayar, son değişiklik zamanı
  ve `scored_people_count` (kişi hakkında puan tutulan kişi sayısı).
- `core/friend_suggestions.json`: kişiye yapılan öneriler (önerilen kişinin
  kimliği, zamanı, kapatıldıysa zamanı).

**Puanın kendisi ve kiminle hesaplandığı dışa aktarılmaz.** Puan ham veri
değil, ortak katılımdan her gün yeniden türetilen bir değerdir; ham katılım
zaten `event_rsvps` bölümündedir. Kişi listesi karşı tarafların katılımı
hakkında da bilgi verir. Kişi, profillemenin yapıldığını ve kaç kişiyle
yapıldığını görür; değerler istenirse hukukçu görüşüyle eklenir (soru 3).

### 10. Log ve gizlilik

İş yalnızca sayıları log'lar (hesaplanan çift, yazılan öneri). Puan, aday,
yaş cevabı ve kimlik log'a, metriğe, trace'e girmez. Öneri bildiriminin
payload'ı yalnızca kimlik taşır.

### 11. API sözleşmesi

| Uç | gRPC (core `SocialService`) |
| --- | --- |
| `GET /v1/me/friend-suggestions` | `ListFriendSuggestions` |
| `DELETE /v1/me/friend-suggestions/{user_id}` | `DismissFriendSuggestion` |
| `GET /v1/me/social-settings` | `GetSocialSettings` |
| `PATCH /v1/me/social-settings` | `UpdateSocialSettings` |

Hepsi kullanıcının kendi erişim token'ıyla; çağıran her zaman token'ın
sahibidir. `FriendSuggestion` mesajında ve şemasında puan alanı yoktur; bir
sözleşme testi bunu sabitler.

## Gerekçe

- **Tutmadığın veri çalınamaz:** yaş bilgisi core'a gelmez, gelen evet/hayır
  saklanmaz; puan yalnızca hesaplandığı tabloda durur, dışarı çıkmaz.
  Öneri gerekçesi gösterilmez.
- **İhlal varsayımı:** core ele geçirilse bile `IsMinor` yalnızca core'un
  ServiceAccount'una açıktır ve çağrı başına 10 kimlikle sınırlıdır; yaş ya
  da tarih dönmez. Bu yüzey zaten şikâyet önceliği için vardı; kapsamı
  genişler ama dönen bilgi değişmez.
- **Güvenli taraf:** yaş cevabı eksikse öneri yok; ayar, engel ve şikâyet
  her okumada yeniden denetlenir.

## Sonuçlar

- identity'de kod değişmez; `MinorService` sözleşmesinin açıklaması
  "şikâyet önceliği ve arkadaş önerisi" olarak güncellenir.
- chat'e yeni bildirim türü (migration, proto enum) eklenir; push
  trigger'ı bu türü atlar.
- Aydınlatma metnine profilleme maddesi eklenmeden pilotta açılmamalıdır
  (hukukçu listesi).
- Veri Modeli v15'e üç tablo, bir bildirim türü, iki dışa aktarma bölümü ve
  bir saklama satırı eklenir (PR açıklamasında).

## Platform sahibine sorular

1. Organizatör düzeltmesiyle `attended` olan katılım da "doğrulanmış"
   sayılsın mı? (Seçilen: evet, check-in'in resmî düzeltme yolu olduğu
   için.)
2. Yarı ömür 180 gün uygun mu? (12 ay önceki katılım 0,25 sayılır.)
3. Dışa aktarmada puan değerleri ve kiminle hesaplandığı da verilsin mi?
   (Seçilen: hayır; yalnızca sayı. Hukukçu listesiyle birlikte.)
4. Puan arkadaş listesi ve davet ekranı sırasında da kullanılsın mı? Bu PR
   yalnızca öneri sırasında kullanıyor.
5. Push yerine yalnızca uygulama içi bildirim doğru mu? (Seçilen: push yok.)
6. "Kapat" çift düzeyinde: A, B'yi kapattıysa B'ye de A önerilmiyor. Uygun
   mu?

## Değerlendirilen alternatifler

- **Olayla artımlı puan:** yaşlanma ve pencere için yine günlük bir iş
  gerekirdi; iki yol birlikte daha çok hata yüzeyi demektir.
- **"18 altı" bilgisini core'da önbelleklemek / profil kopyasına eklemek:**
  hesaplama ucuzlar, ama yaş bilgisi identity dışına kalıcı olarak çıkar ve
  doğum gününde bayatlar. Reddedildi.
- **Yaşa göre yalnızca bandı (16–17 / 18+) identity'de hesaplatıp çiftleri
  identity'ye sormak (`SameAgeSide(a, b)`):** core'a yine evet/hayır gelir ve
  identity'ye yeni bir metod eklenir; mevcut `IsMinor` aynı bilgiyi verdiği
  için yeni yüzey açılmadı.
- **Ayarları identity'nin hesap ayarlarında tutmak:** kapatma anında puanı
  silmek için identity'den core'a olay gerekirdi; silme gecikir ve bir
  servis daha işin içine girer.
