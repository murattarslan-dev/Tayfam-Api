# ADR-0030: Arkadaşlık puanı ve arkadaş önerisi

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15; mimari oturumu
  aracılığıyla, PR #212 incelemesi)
- **Tarih:** 2026-11-15
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §4, issue #208). Bu ADR onların mimariye
  taşınmasıdır. İlk taslakta açık kalan ayrıntıları platform sahibi
  2026-11-15'te sohbette cevapladı ("Platform sahibinin kararları").
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0013](0013-servis-kimligi.md),
  [ADR-0019](0019-push-bildirimleri.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md); issue #208, #215 (§12)

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
her okumada yeniden süzülür. Yeni tablolar (migration `00025`):

| Tablo | Alanlar | Not |
| --- | --- | --- |
| `friend_scores` | `user_low_id`, `user_high_id`, `score` (> 0), `shared_count` (≥ 1), `last_shared_at`, `computed_at` | PK (low, high), low < high. Her gün baştan hesaplanır. Hiçbir API cevabına, olaya, log'a, metriğe girmez |
| `friend_suggestions` | `user_id`, `suggested_user_id`, `created_at`, `dismissed_at`, `withdrawn_at`, `source` (`suggested` / `blocked` / `declined` / `unfriended`) | PK (user, suggested); kendini öneremez. Öneri satırı ve çift hafızası; satır ayar değişikliğinde silinmez (§5). `dismissed_at`: "Kapat" ya da kalıcı dışlama. `withdrawn_at`: artık gösterilmez ama hafıza ve haftalık sınır için kalır. `suggested` dışındaki kaynaklar engelleme, ret ve arkadaşlığı bitirme anında yazılan, baştan kapatılmış kayıtlardır (öneri değildir: listelenmez, haftalık sınıra sayılmaz, dışa aktarmaya öneri olarak girmez) |
| `social_settings` | `user_id` PK, `profiling_enabled` (varsayılan true), `hide_from_suggestions` (varsayılan false), `updated_at` | Satır yalnızca kişi bir ayarı değiştirince açılır; yoksa varsayılanlar geçerlidir |

Bir çiftin önerilebilmesi tek yerde, `friend_suggestion_allowed(alıcı,
aday)` SQL fonksiyonunda tanımlıdır (§3'teki elemeler). Aday sorgusu,
öneriyi yazan sorgu, liste ve bildirim durumu aynı fonksiyonu çağırır;
kurallar ayrı sorgularda birbirinden kaymaz.

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
  "Check-in"; platform sahibi 2026-11-15'te onayladı).
- Profillemesi kapalı, hesabı silinmiş ya da platformda banlı kullanıcı ve
  aralarında engel olan çift için puan hesaplanmaz.
- **Hesaplama:** core'un `friend-suggestions` arka plan işi günde bir kez
  tabloyu tek transaction'da siler ve yeniden yazar. Olayla artımlı
  güncelleme seçilmedi: yaşlanma (ağırlığın her gün değişmesi), 12 aylık
  pencereden düşme, katılım düzeltmesi, etkinlik silme ve ayar değişikliği
  artımlı modelde ayrı ayrı ele alınmalıydı; baştan hesaplama bunların
  hepsini tek, idempotent bir sorguyla doğru yapar. Bugünkü ölçekte (tek
  node, pilot) maliyeti önemsizdir; büyürse kullanıcı aralıklarına bölünür.
- **Ayar ve engelle yarış:** iş, hesaplama transaction'ında özel bir
  advisory kilit alır; profilleme kapatma ve engelleme (ret, arkadaşlığı
  bitirme dahil) aynı kilidi paylaşımlı alır. Hesaplama sürerken kapatılan
  profilleme işin commit'ini bekler, sonra yazılanı siler. Ayrıca iş, aynı
  transaction'da yazdıktan sonra profillemesi kapalı kullanıcıların ve
  engelli çiftlerin satırlarını yeniden siler.
- **Kullanım (platform sahibinin kararı, 2026-11-15):** Puan öneri
  adaylarının sırasının yanında **arkadaş listesi ve davet ekranlarındaki
  kişi sırasında da** kullanılır (issue #215, §12). Orada da yalnızca sıra
  döner, puan dönmez; profillemesi kapalı kullanıcının listesi puansız
  (kimliğe göre) sırayla gelir.

### 3. Öneri üretimi

Aynı iş, puandan sonra:

1. **Aday:** `shared_count ≥ 3` olan çiftler, puana göre azalan sırada,
   kişi başına en çok 5 aday. Elenenler (`friend_suggestion_allowed`):
   - arasında arkadaşlık ya da bekleyen istek olanlar (her iki yön);
   - iki yönden herhangi birinde engel olanlar;
   - biri diğerini şikâyet etmişse (`reports.reporter_id` ↔
     `subject_user_id`, her iki yön; şikâyet saklandığı sürece, 1 yıl);
   - reddedilmiş arkadaşlık isteği ya da bitmiş arkadaşlık olanlar: ret ve
     arkadaşlığı bitirme anında kalıcı kapatma kaydı yazılır (§6), 90 günlük
     `friend_request_outcomes` silinse de çift önerilmez;
   - iki yönden birinde kapatılmış (`dismissed_at`) çiftler: **"Kapat" çift
     düzeyindedir**, A, B'yi kapattıysa B'ye de A önerilmez;
   - profillemesi kapalı olan (iki taraf), "Beni önerme" diyen (aday),
     hesabı silinmiş ya da banlı (iki taraf) kişiler.
   Alıcıya özgü iki koşul ayrıca: alıcıya daha önce önerilmiş kişi (satır
   varken, geri çekilmiş olsa da) yeniden aday olmaz; alıcı son 7 gün
   içinde öneri aldıysa hiç aday yok (haftada en fazla 1).
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
   Transaction içinde aday sorgusunun bütün koşulları (haftalık sınır ve
   `friend_suggestion_allowed`) yeniden denetlenir. Birden fazla pod aynı
   işi advisory kilidiyle güvenle çalıştırır.
4. **İzlenebilirlik:** identity'ye sorulamayan alıcı sayısı her çalışmada
   tek bir uyarı satırıyla log'lanır (yalnızca sayı; kimlik, yaş, aday
   yok), identity kesintisi fark edilir. İş başarısız sayılmaz.
5. **Sayfalama:** aday sorgusu 500 satırlık sayfalarla okunur; sayfa
   doluysa son alıcının (adayları LIMIT'le kesilmiş olabilir) satırları o
   sayfada işlenmez, sonraki sayfa o alıcıdan başlar.

Yaş sınırı üretim anında denetlenir. Açık öneri en çok 30 gün listelenir;
bu sürede birinin 18'e girmesi kabul edilen bir kenar durumdur (iki taraf
o zaman da yaşıt sayılabilecek kadar yakındır, öneri yalnızca alıcıya
görünür ve başka bir yetki açmaz).

### 4. Öneri listesi, "Arkadaş ekle" ve "Kapat"

- `GET /v1/me/friend-suggestions`: alıcının son 30 gündeki kapatılmamış ve
  geri çekilmemiş önerileri; kişi başına ad, avatar ve öneri zamanı. Liste
  her okumada `friend_suggestion_allowed` ile yeniden süzülür. **Puan, ortak katılım
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
  satırları silinir (puan silinir); kişiye yapılmış ve kişinin önerildiği
  açık öneriler **geri çekilir** (`withdrawn_at`; listelenmez, bildirimi
  `expired` olur, §8). Sonraki hesaplamalar kişiyi dışarıda bırakır; kişi
  öneri almaz ve kimseye önerilmez. Yeniden açılınca puan bir sonraki
  günlük hesaplamada oluşur.
- **"Beni önerme" açılınca** kişinin önerildiği açık öneriler geri çekilir;
  kişi aday olmaz. Kendisi öneri almaya devam eder.
- **Satırlar silinmez, geri çekilir:** öneri satırı haftalık sınırın ve
  "bu kişiye bu kişi önerildi" hafızasının kaynağıdır. Silinseydi ayarı
  kapatıp açmak haftalık sınırı sıfırlar ve her gün yeni öneri almayı
  mümkün kılardı. Geri çekilen öneri bir "Kapat" değildir: çift kalıcı
  dışlanmaz, ama aynı alıcıya aynı kişi 365 gün içinde yeniden önerilmez.
  Kişinin kendi "Kapat" kayıtları da kalır.

### 6. Engelleme ve hesap silme

- **Kalıcı dışlama** — engelleme, gelen arkadaşlık isteğini reddetme ve
  arkadaşlığı bitirme (aynı transaction'da): çiftin puan satırı silinir;
  karşı tarafın bu kişiye açık önerisi geri çekilir; bu kişinin karşı
  tarafa önerisi kapatılır, yoksa `source` = `blocked` / `declined` /
  `unfriended` ile baştan kapatılmış bir kayıt yazılır. Böylece engel
  kaldırılsa ya da istek sonucu 90 gün sonra silinse de çift bir daha
  önerilmez ("engellenen ve reddedilen tekrar önerilmez"). Karşı tarafa
  hiçbir şey söylenmez. Kendi isteğini geri çekmek ret değildir, dışlamaz.
- **Şikâyet:** şikâyet satırı aday sorgusunda ve listede doğrudan elemedir;
  şikâyet akışında ayrıca bir yazma yapılmaz.
- **Hesap silme** (`user.deleted`): kişinin puanları, iki yöndeki bütün
  öneri satırları ve ayarları silinir.

### 7. Saklama

| Veri | Süre |
| --- | --- |
| `friend_scores` | Her gün baştan yazılır; 12 ayda ortak katılımı kalmayan çift kendiliğinden düşer |
| `friend_suggestions` (kapatılmamış: açık ya da geri çekilmiş) | 30 gün listelenir; satır 365 gün sonra günlük işle silinir (365 gün içinde aynı kişi aynı kişiye tekrar önerilmez) |
| `friend_suggestions` (kapatılmış) | Hesap silinene kadar (kişinin "bir daha önerme" isteği; yalnızca iki kimlik ve zaman) |
| `social_settings` | Hesap silinene kadar |

### 8. Bildirim: uygulama içi, push yok

Yeni bildirim türü `friend_suggestion` (hedef `user`, tayfasız, aktörsüz).
chat bildirimi listeler ama **push göndermez**: `enqueue_push` trigger'ı bu
tür için push işi açmaz (chat migration `00016`). Gerekçe: Notion "uygulama
içi bildirim" diyor; öneri acil değildir, haftada en fazla birdir ve
algoritmik bir öneriyi kilit ekranına taşımak profillemeyi kullanıcının
haberi olmadan görünür kılar. Push istenirse yalnızca trigger değişir.
Platform sahibi push olmamasını 2026-11-15'te onayladı.

**Bildirimin geri çekilmesi.** core'un chat'teki tek bir bildirimi silmesi
için mevcut bir yol yoktur (chat yalnızca tayfa kalıcı silinince ve hesap
silinince toplu siler). Yeni bir outbox olayı ve tüketici eklemek yerine
#192'deki **bildirim durumu** yolu kullanılır: chat bildirim listesini
dönerken `friend_suggestion` bildirimleri için de core'a
`GetRequestResolutions` (`REQUEST_KIND_FRIEND_SUGGESTION`) sorar. core
`pending` (öneri hâlâ gösteriliyor), `accepted` (arkadaş oldular ya da
istek gönderildi), `declined` (kişi kapattı) ya da `expired` (geri
çekildi, engel, şikâyet, ayar, silinmiş hesap, 30 gün; neden ayırt
edilmez) döner. İstemci `pending` olmayan öneri bildirimini göstermez.
Gerekçe: bildirim içerik taşımaz (yalnızca hedef kişinin kimliği); push
gitmediği için kilit ekranında da hiçbir şey kalmaz; durum her listede
core'dan, kullanıcının kendi token'ıyla hesaplanır ve bildirimin kendisi
90 gün sonra silinir. Engelleyenin kim olduğu da bu yoldan sızmaz.

### 9. KVKK dışa aktarma (ADR-0021)

İki yeni bölüm:

- `core/social_settings.json`: tek kayıt; iki ayar, son değişiklik zamanı
  ve `scored_people_count` (kişi hakkında puan tutulan kişi sayısı).
- `core/friend_suggestions.json`: kişiye yapılan öneriler (önerilen kişinin
  kimliği, zamanı, kapatıldıysa ya da geri çekildiyse zamanı).

**Puanın kendisi ve kiminle hesaplandığı dışa aktarılmaz.** Puan ham veri
değil, ortak katılımdan her gün yeniden türetilen bir değerdir; ham katılım
zaten `event_rsvps` bölümündedir. Kişi listesi karşı tarafların katılımı
hakkında da bilgi verir. Kişi, profillemenin yapıldığını ve kaç kişiyle
yapıldığını görür (platform sahibi 2026-11-15'te onayladı; değerlerin
eklenmesi gerekirse hukukçu görüşüyle ayrı karar).

### 10. Log ve gizlilik

İş yalnızca sayıları log'lar (hesaplanan çift, yazılan öneri, identity'ye
sorulamayan alıcı). Puan, aday, yaş cevabı ve kimlik log'a, metriğe,
trace'e girmez. Öneri bildiriminin
payload'ı yalnızca kimlik taşır.

### 11. API sözleşmesi

| Uç | gRPC (core `SocialService`) |
| --- | --- |
| `GET /v1/me/friend-suggestions` | `ListFriendSuggestions` |
| `DELETE /v1/me/friend-suggestions/{user_id}` | `DismissFriendSuggestion` |
| `GET /v1/me/social-settings` | `GetSocialSettings` |
| `PATCH /v1/me/social-settings` | `UpdateSocialSettings` |
| Bildirim listesindeki `resolution` (chat → core) | `GetRequestResolutions` (`REQUEST_KIND_FRIEND_SUGGESTION`) |

Hepsi kullanıcının kendi erişim token'ıyla; çağıran her zaman token'ın
sahibidir. `FriendSuggestion` mesajında ve şemasında puan alanı yoktur; bir
sözleşme testi bunu sabitler.

### 12. Arkadaş listesi ve davet sırası (issue #215)

Ek bölüm, 2026-11-16 (platform sahibinin 2026-11-15 kararı, §2 "Kullanım";
Notion §9). Migration yoktur: `friend_scores`, `social_settings` ve
`friendships` yeterlidir.

- **Kapsam:** `GET /v1/me/friends` (`ListFriends`). Tayfa davetindeki kişi
  seçimi istemcide bu listeyle yapılır; sunucuda ayrı bir "davet adayı"
  ucu yoktur. Anlık etkinlik davetinin (#209) kişi listesi de arkadaş
  listesiyse aynı sıra geçerlidir; ayrı bir aday ucu eklenirse aynı sorgu
  kuralını (aşağıda) kullanır. Keşfet sırası #210'dadır. Arkadaşlık istek
  listeleri (`ListFriendRequests`) puanla sıralanmaz.
- **Sıra:** etkin puana göre azalan, eşitlikte karşı tarafın kimliğine göre
  artan (bugünkü sıra). Puanı olmayan arkadaşların etkin puanı 0'dır; bu
  yüzden puanlılar önde, puansızlar bugünkü sırayla arkadadır.
- **Etkin puan:** `friend_scores.score`, yalnızca arkadaşlık kabul
  edilmişse ve **iki tarafın da** profillemesi açıksa; aksi halde 0.
  Profilleme kapatılınca puan zaten silinir (§5); okumadaki bu koşul,
  hesaplama işiyle yarışta geride kalabilecek bir satıra karşı savunma
  derinliğidir. Çağıranın profillemesi kapalıysa bütün puanlar 0 olur ve
  liste tamamen kimliğe göre gelir.
- **Puan dışarı çıkmaz:** puan sorgunun sonuç sütunu değildir; yalnızca
  `ORDER BY` ve `WHERE` içinde kullanılır, Go'ya, cevaba, log'a gelmez.
  `Friend` mesajında ve şemasında puan alanı yoktur (sözleşme testleri).

**Sayfalama imleci: sunucuda yeniden okunan konum.** İmleç bugünkü
biçimindedir: liste adı ve sayfanın son kişisinin kimliği (`{m, u}`,
base64url JSON). Puan, sıra numarası ya da konum taşımaz. Sonraki sayfada
sorgu o kişinin etkin puanını aynı kurallarla veritabanından yeniden okur
ve `(etkin puan, kimlik)` anahtarıyla devam eder.

- **Uydurma imleç kâhin olamaz:** imleçteki kişi çağıranın kabul edilmiş
  arkadaşı değilse (ya da iki taraftan birinin profillemesi kapalıysa)
  puanı 0 sayılır. Böylece biri, arkadaş olmadığı birinin kimliğini
  imlece yazarak o çiftin puanını arkadaşlarınınkiyle karşılaştıramaz;
  arkadaşları için de sıra zaten listede görünen bilgiden fazlasını
  vermez. İmleç başka listede kabul edilmez (liste adı).
- **Seçilmeyen yollar:** imlece puanı koyup AES-GCM ile şifrelemek yeni bir
  anahtar (SOPS secret, döndürme, ortam değişkeni) ve anahtar sızarsa
  geçmiş imleçlerden puanın okunması demekti; ofset (sıra numarası)
  sayfalama sıra kayınca kişi atlar ya da tekrarlar ve büyük ofsette
  yavaştır; sunucuda saklanan imleç durumu (tablo ya da önbellek) yeni bir
  tablo, saklama süresi ve temizlik işi demekti. Konumu yeniden okumak
  hiçbir yeni sır ya da tablo eklemez.
- **Bilinen sınır:** sayfalar arasında puan değişirse (günlük hesaplama,
  profilleme kapatma, arkadaşlığın bitmesi) bir kişi atlanabilir ya da iki
  kez görünebilir; sıra anahtarı değişen her anahtar-imleçli listede olduğu
  gibi. İstemci listeyi kimliğe göre tekilleştirir; tam listeyi yenilemek
  sırayı düzeltir. Hesaplama günde birdir, etki küçüktür.
- **Sıranın kendisi:** sıra, kimlerle yakın zamanda birlikte etkinliğe
  katıldığını kaba bir şekilde yansıtır. Bu, platform sahibinin istediği
  özelliğin kendisidir ve yalnızca kişinin kendisine görünür (kendi
  katılımları zaten kendisine açıktır); puanın değeri ve ortak katılım
  sayısı çıkarılamaz.

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
  trigger'ı bu türü atlar; bildirim listesi bu türün durumunu da core'a
  sorar.
- Puan arkadaş listesi ve davet ekranı sırasında da kullanılır (issue
  #215, §12); imleç puan taşımaz.
- Aydınlatma metnine profilleme maddesi eklenmeden pilotta açılmamalıdır
  (hukukçu listesi).
- Veri Modeli v15'e üç tablo, bir bildirim türü, iki dışa aktarma bölümü ve
  bir saklama satırı eklenir (PR açıklamasında).

## Platform sahibinin kararları (2026-11-15, sohbette)

İlk taslakta açık kalan ayrıntılar; cevaplar yukarıdaki bölümlere işlendi.

1. Organizatörün işaretlediği katılım "doğrulanmış" sayılır (§2).
2. Yarı ömür 180 gün (§2).
3. Dışa aktarmada puan değerleri ve kişiler değil, yalnızca sayı (§9).
4. Puan arkadaş listesi ve davet ekranlarındaki kişi sırasında da
   kullanılacak; ayrı issue (§2).
5. Push yok, yalnızca uygulama içi bildirim (§8).
6. "Kapat" çift düzeyinde (§3).

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
- **Ayar değişikliğinde öneri satırlarını silmek:** basit, ama haftalık
  sınırı ve "önerildi" hafızasını sıfırlar (§5). Reddedildi.
- **Öneri bildirimini chat'te silmek için yeni bir core → chat olayı:**
  gerçek silme sağlar ama yeni bir olay türü, tüketici ve yetki yüzeyi
  ekler; bildirim içerik ve push taşımadığı için mevcut durum yolu yeterli
  görüldü (§8).
- **Ayarları identity'nin hesap ayarlarında tutmak:** kapatma anında puanı
  silmek için identity'den core'a olay gerekirdi; silme gecikir ve bir
  servis daha işin içine girer.
