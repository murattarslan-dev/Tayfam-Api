# ADR-0032: Anlık (bireysel) etkinlik — gizli, tek etkinlikli tayfa (v2)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15; kararlar sohbette,
  mimari oturumunun aktarımıyla). Açık kalan ayrıntılar aşağıda "Platform
  sahibine sorular"da; cevap gelene kadar bu ADR'deki seçim geçerlidir.
- **Tarih:** 2026-11-16
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §5, Özellik Haritası "Sosyal"; issue #209).
  Bu ADR onların mimariye taşınmasıdır.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0024](0024-kanal-kitlesi.md),
  [ADR-0028](0028-etkinlik-fotograf-albumu.md),
  [ADR-0029](0029-bosum-bos-vakit.md)

## Bağlam

Kullanıcı bir tayfaya bağlı olmadan arkadaşlarıyla tek seferlik bir etkinlik
düzenleyebilmelidir; istemci bu akışı "Boşum" rozetindeki "Bir şey yapalım"
düğmesinden (belli bir gün için) de açar (ADR-0029). Platform sahibinin
kararları (2026-11-15):

- Arka planda **gizli, tek etkinlikli bir tayfa** açılır: organizatör
  yönetici, davetliler üye. Moderasyon, sohbet (etkinlik kanalı), check-in
  ve albüm mevcut altyapıyla çalışır.
- **Davet:** arkadaşlara doğrudan ve davet linkiyle; link sahibi katılım
  isteği gönderir, organizatör onaylar.
- Etkinlik bitince (iptalde iptal anından) **7 gün sonra arşivlenir.** Pasif
  tayfa temizliğindeki "7 günde 3 üye" kuralı bu tayfalara uygulanmaz.
- Kişi başı aynı anda en fazla **3 açık anlık etkinlik**.
- **"Bu ekiple tayfa kur":** organizatör anlık tayfayı kalıcı tayfaya
  dönüştürür (ad, ilgi alanları); dönüşen tayfa normal tayfa kurallarına
  girer.
- Anlık tayfa **Keşfet'e hiçbir zaman çıkmaz** ve tayfa listesinde ayrı
  işaretle döner.

## Karar

### 1. Tayfa türü: `tayfas.kind`

`tayfas`'a iki kolon eklenir (core, migration 00027):

| Alan | Tür | Kısıt | Not |
| --- | --- | --- | --- |
| `kind` | text | `standard` / `instant`, varsayılan `standard` | Var olan tayfalar `standard` |
| `converted_at` | timestamptz | NULL ya da dolu; doluysa `kind = standard` | Anlık tayfanın kalıcıya dönüştüğü an |

Anlık tayfa ayrı bir tablo değil, bir tayfa türüdür: üyelik, roller, izin
tablosu, sohbet, check-in, albüm, şikâyet ve denetim kaydı hiç değişmeden
çalışır. Ayrı bir "etkinlik + katılımcılar" modeli bunların hepsini
yeniden yazmak demekti.

Veritabanı türün değişmezlerini ayrıca korur (ihlal varsayımı, ADR-0011):

- `instant` tayfada `visibility = 'private'` ve `join_policy = 'approval'`
  (CHECK). Keşfet (#210) hangi sorguyu yazarsa yazsın anlık tayfa
  listelenemez: listelenme `visibility = 'listed'` ister.
- `kind` yalnızca `instant → standard` yönünde ve `converted_at` ile
  birlikte değişir; `converted_at` bir kez yazılır (trigger
  `tayfas_kind_guard`).
- Anlık tayfada en fazla bir etkinlik vardır (trigger
  `events_instant_single`, silinmiş satırlar dahil sayılır). Dönüşen tayfada
  bu sınır kalkar.
- Ad sınırı türe göre: `standard` 3–50 karakter (değişmedi), `instant` 1–120
  (etkinlik başlığıyla aynı; §2).

### 2. Oluşturma: tek transaction

`POST /v1/instant-events` (`TayfaService.CreateInstantEvent`) tek bir
transaction'da:

1. Kullanıcı başına advisory kilit alır (§5) ve `client_request_id` ile
   tekrar gönderimi denetler: aynı istek ilk oluşturulan tayfayı ve
   etkinliği döndürür.
2. Açık anlık etkinlik sayısını denetler (§5).
3. Tayfayı açar: `kind = instant`, ad = etkinlik başlığı, katılım
   politikası `approval`, organizatör `admin` (`joined_via = creator`).
   Modüllerden yalnızca `events` açıktır; duyuru, soru-cevap ve anket
   kapalıdır ve açılamaz (§3). İlgi alanı yoktur. Katılım kodu (kolon NOT
   NULL) üretilir ama hiçbir zaman gösterilmez ve kodla katılım reddedilir
   (§4).
4. Etkinliği mevcut etkinlik kurallarıyla açar (tür, zaman, yer, kontenjan,
   türe özel alanlar). Tekrarlayan etkinlik (`recurrence`) ve
   `discoverable` kabul edilmez; kapak tayfa henüz yokken yüklenemediği
   için sonra etkinlik düzenlemesiyle eklenir. Etkinlik sonradan da
   Keşfet'e (`discoverable`) açılamaz.
5. İstekteki arkadaşları (`invitee_user_ids`, en fazla 10) doğrudan davet
   eder (§4). Biri davet edilemiyorsa hiçbir şey oluşmaz.
6. Denetim kayıtları ve outbox olayları (`tayfa.created` yükünde
   `kind: instant`, `event.created`, `tayfa.invited`) aynı transaction'da
   yazılır.

**Ad:** Anlık tayfanın adı etkinlik başlığıdır; organizatör başlığı
değiştirince tayfa adı aynı transaction'da güncellenir. Ad bildirimlerde,
davet listesinde ve üye ekranlarında anlamlı kalır; API Türkçe sabit metin
döndürmez (CLAUDE.md "Arayüz metinleri"). Başlığa sınırı veritabanı da
koruduğu için ad 1–120 olabilir.

Etkinlik kanalını (ADR-0024) istemci mevcut uçla (`POST
/v1/tayfas/{id}/channels`, `visibility: event`) açar; organizatör
`caller_can_manage` olduğu için açabilir. Sunucu kanal açmaz: chat'e yeni bir
olay ve iki servis arası yan etki eklemek bu iş için gereksizdi.

### 3. İzinler: tek izin tablosu, tür ayarı

`authz.Settings`'e `Instant` eklenir; anlık tayfada şu işlemlerin en düşük
rolü değişir (`minRole`). Varsayılan cevap yine "hayır"dır; bir ayar yalnızca
adını verdiği işlemi değiştirir.

| İşlem | Kalıcı tayfa | Anlık tayfa | Neden |
| --- | --- | --- | --- |
| `TayfaUpdateSettings` | admin | **kimse** | Ad başlıktan gelir; ayarlar dönüşümle |
| `TayfaRotateJoinCode` | admin | **kimse** | Kod yoktur (§4) |
| `TayfaManageModules` | admin | **kimse** | Yalnızca etkinlik modülü |
| `EventCreate` | moderatör (ayarla üye) | **kimse** | Tek etkinlik |
| `MemberChangeRole` | admin | **kimse** | Tek yönetici organizatördür; "organizatör onaylar" |
| `InviteCreate` | üye | **admin** | Davet linkini organizatör paylaşır |
| `InviteDirect` | üye | **admin** | Arkadaşlarını organizatör davet eder |
| `TayfaConvert` (yeni) | **kimse** | admin | "Bu ekiple tayfa kur" |

Diğer her işlem kalıcı tayfadaki gibidir: üyeler etkinliği görür, cevap
verir, check-in yapar, albüme yükler, kanala yazar, şikâyet eder;
organizatör (yönetici) katılım isteklerini onaylar, üye susturur, çıkarır,
banlar, şikâyet kuyruğunu görür, etkinliği düzenler ve iptal eder, tayfayı
silebilir. Moderatör olmadığı için onay yalnızca organizatördedir.

Tür ayarı, izin tablosunu okuyan bütün yollara gider: `access.Settings`
`kind`'i taşır; tayfa, üyelik ve davet servisleri artık `AuthorizeIn` ile
tayfanın ayarlarını verir (etkinlik ve kanal servisleri zaten veriyordu).
Tablo, her rol ve işlem için iki türde de testle korunur.

### 4. Davet

- **Doğrudan davet:** mevcut uç (`POST /v1/tayfas/{id}/invitations`) ve
  oluşturma isteğindeki `invitee_user_ids`. Davet edilen kişinin
  organizatörün **kabul edilmiş arkadaşı** olması, aralarında iki yönden
  engel olmaması ve hesabın canlı olması şarttır (mevcut kural; arkadaş
  olmayan `not-friends`). Davet eden yönetici olduğu için kabul eden
  doğrudan etkin üye olur. Günlük 10 doğrudan davet sınırı (v14) bütün
  tayfalarla ortaktır.
- **Davet linki:** yalnızca organizatör oluşturur. Kalıcı tayfada yönetimin
  linkiyle gelen hemen üye olur (v14); **anlık tayfada linkle gelen her
  zaman katılım isteği** (`pending`) bırakır ve organizatöre katılım isteği
  bildirimi gider; organizatör onaylar ya da reddeder. Link önizlemesi
  değişmez (ad, üye sayısı).
- **Katılım kodu:** anlık tayfada kodla katılım, kodun bilinmesi ihtimaline
  karşı da reddedilir (`invalid-join-code`; kod tahmininde ayrım yapılmaz).

### 5. Sınırlar

- **3 açık anlık etkinlik:** kullanıcının organizatörü olduğu (`created_by`),
  silinmemiş ve arşivlenmemiş anlık tayfalardan etkinliği **bitmemiş ve
  iptal edilmemiş** olanlar sayılır. Etkinliği biten ya da iptal edilen,
  7 günlük arşiv beklemesinde olsa da açık sayılmaz. Dördüncü istek
  `instant-event-limit` (429) alır.
- Eşzamanlılık: sayım ve ekleme, kullanıcı başına advisory kilit
  (`instant_create:<user_id>`) altında aynı transaction'da yapılır; aynı anda
  gelen istekler sırayla işlenir, sınır aşılamaz (testli).
- **Tayfa açma sınırlarına sayılmaz** (öneri kabul edildi): kullanıcı başına
  günde 1 tayfa ve gateway'deki IP başına saatte 1 sınırı anlık oluşturmaya
  uygulanmaz; anlık tayfalar da günlük tayfa sayımına girmez. Gerekçe:
  "Bir şey yapalım" sık ve hafif bir eylemdir; kötüye kullanımı 3 açık
  sınırı, günlük 10 doğrudan davet sınırı ve linkle gelenin onaya düşmesi
  sınırlar. Dönüştürme ise tayfa açmadır ve iki sınıra da girer (§7).

### 6. Arşiv ve temizlik

- Yeni saatlik iş **`instant-archive`**: anlık tayfanın etkinliği
  `COALESCE(cancelled_at, ends_at) + 7 gün` geçmişse (ya da tayfada canlı
  etkinlik kalmamışsa) tayfa `archived` olur (salt okunur). Denetim kaydı ve
  `tayfa.archived` olayı yazılır; **bildirim gitmez** (bekleniyordu; kanalın
  "N gün sonra kapanacak" bandı zaten görünür). Askıdaki ya da silme
  bekleyen tayfaya dokunulmaz (mevcut arşiv kuralıyla aynı).
- Haftalık **pasif tayfa temizliği** yalnızca `kind = standard` tayfalara
  bakar: anlık tayfa uyarılmaz ve "7 günde 3 üye" ile "30 gün hareketsiz"
  kurallarıyla arşivlenmez.
- Dönüşen tayfada "7 günde 3 üye" kuralının 7 günü **dönüşüm anından**
  (`COALESCE(converted_at, created_at)`) sayılır; hareketsizlik saati
  dönüşümde sıfırlanır.
- Arşivden sonrası kalıcı tayfayla aynıdır: arşiv geri alınmaz; silme
  talebi, 30 gün sonra kalıcı silme ve hesap silmede veri temizliği
  değişmez.
- Etkinlik kanalı chat'te kendi kuralıyla (etkinlik bitişi + 7 gün)
  arşivlenir (ADR-0024); iki arşiv aynı ana denk gelir.

### 7. Kalıcıya dönüştürme

`POST /v1/tayfas/{id}/conversion` (`TayfaService.ConvertInstantTayfa`):

- Yalnızca anlık tayfanın yöneticisi (organizatör; `TayfaConvert`) ve
  yalnızca tayfa salt okunur değilken (arşivlenmiş, askıda, silme bekleyen
  tayfa dönüşmez).
- Gövde: ad (3–50), ilgi alanları, açıklama ve katılım politikası;
  kuralları tayfa oluşturmayla aynıdır.
- Dönüşüm **tayfa açmadır**: kullanıcı başına 24 saatte 1 sınırı
  (`tayfa-create-limit`) ve gateway'in IP başına saatte 1 sınırı uygulanır.
  Günlük sayım `kind = standard` tayfaları `COALESCE(converted_at,
  created_at)` ile sayar; dönüştürmek de sınırı kullanır.
- Aynı transaction'da: `kind = standard`, `converted_at = now`, ad, ilgi
  alanları, açıklama, katılım politikası; **bütün modüller açılır**;
  hareketsizlik saati sıfırlanır; denetim kaydı (`tayfa.converted`) ve
  `tayfa.updated` olayı (`fields` içinde `kind`).
- Üyeler, bekleyen istekler, davetler, etkinlik, kanal ve albüm olduğu gibi
  kalır. Bundan sonra normal tayfa kuralları geçerlidir: herkes davet
  linki üretir, rol değiştirilebilir, yeni etkinlik açılabilir, pasif
  temizlik işler. Keşfet'e açmak yine ayrı bir yönetim kararıdır (#210).

### 8. Liste ve görünüm

- `Tayfa` iki alan kazanır: `kind` (`standard` / `instant`) ve yalnızca
  anlık tayfada `instant_event_id` (istemci doğrudan etkinliği açar). İkisi
  de `GET /v1/tayfas`, `GET /v1/tayfas/{id}` ve oluşturma/dönüştürme
  cevaplarında döner. Bekleyen katılım istekleri listede yalnızca ad ve
  kimlik taşıdığı için `MyTayfa.kind` de eklenir.
- Anlık tayfa Keşfet'e hiçbir zaman çıkmaz (§1 CHECK); Keşfet sorgusu ayrıca
  `kind = standard` süzmelidir (#210 kabul kriterine yazılmalı).

### 9. Sözleşme

| Uç | gRPC | Not |
| --- | --- | --- |
| `POST /v1/instant-events` | `TayfaService.CreateInstantEvent` | Gövde: etkinlik oluşturma alanları + `invitee_user_ids` + `client_request_id`; cevap `{tayfa, event}` |
| `POST /v1/tayfas/{id}/conversion` | `TayfaService.ConvertInstantTayfa` | Cevap `Tayfa` |
| `GET /v1/tayfas`, `GET /v1/tayfas/{id}` | mevcut | `kind`, `instant_event_id` |

Yeni hata kodu: `instant-event-limit` (`ERROR_REASON_INSTANT_EVENT_LIMIT`,
`RESOURCE_EXHAUSTED`, HTTP 429). Kalıcı tayfada dönüştürme, anlık tayfada
ikinci etkinlik, modül, ayar, kod ve rol değişikliği `permission-denied`
döner.

### 10. Log, KVKK, platform paneli

- Yeni kişisel veri yoktur: tür bir tayfa özelliğidir. Log ve olaylara yalnız
  kimlikler ve tür yazılır; başlık, ad, davetli listesi yazılmaz.
- KVKK dışa aktarmada yeni bölüm gerekmez: anlık tayfa üyeliği ve etkinliği
  mevcut tayfa, üyelik ve etkinlik bölümlerinde zaten yer alır.
- Platform paneli anlık tayfayı diğer tayfalar gibi görür (askı, şikâyet);
  panelde tür gösterimi ayrı iş.

## Sonuçlar

- Anlık etkinlik bütün tayfa altyapısını (moderasyon, sohbet, check-in,
  albüm, şikâyet) yeni kod yazmadan kullanır; bedeli, izin tablosuna bir tür
  ayarı ve birkaç yolun bu ayarı taşımasıdır.
- Her anlık etkinlik bir tayfa satırı, üyelikler ve bir katılım kodu
  üretir; 3 açık sınırı ve arşiv bunu küçük tutar.
- Dönüşüm geriye alınamaz (kalıcı tayfa anlık olmaz).
- Saatlik `instant-archive` işi job_runs panosunda ayrı satır olarak görünür.

## Değerlendirilen alternatifler

- **Tayfasız etkinlik (ayrı tablo, katılımcı listesi):** sohbet, check-in,
  albüm, şikâyet ve moderasyon tayfa kimliğine bağlı olduğu için hepsinin
  ikinci bir yolu gerekirdi. Reddedildi; platform sahibinin kararı da
  "arka planda gizli tayfa"dır.
- **Ayrı izin tablosu:** "Tek izin tablosu" (ADR-0004) ilkesini bozar. Tür,
  mevcut `Settings` mekanizmasıyla (üyelerin etkinlik açması gibi) tabloya
  girdi.
- **Adsız anlık tayfa (`name` NULL):** her okuyucunun (bildirim, davet,
  panel) NULL'ı ayrıca ele alması gerekirdi; başlığı ad yapmak daha az yüzey
  değiştirir.
- **Sunucunun etkinlik kanalını kendiliğinden açması:** chat'e yeni bir olay
  ve tüketici gerekirdi; istemci mevcut uçla açabildiği için ertelendi.
- **"Açık" = arşivlenmemiş:** biten etkinliğin 7 günlük beklemesi yeni
  etkinliği engellerdi; "bitmemiş ve iptal edilmemiş" seçildi.

## Platform sahibine sorular

Bu ADR aşağıdakileri gerekçeyle seçti; farklı karar verilirse ADR ve kod
güncellenir.

1. **"Açık" anlık etkinlik:** bitmemiş ve iptal edilmemiş etkinlik (7 günlük
   arşiv beklemesindeki sayılmaz). Alternatif: arşivlenene kadar açık.
2. **Kim davet eder:** yalnızca organizatör (doğrudan ve link). Alternatif:
   her katılımcı arkadaşını davet eder, organizatör onaylar.
3. **Modüller:** yalnızca etkinlik modülü açık; duyuru, soru-cevap ve anket
   kapalı (dönüşünce hepsi açılır). Kanal açmak organizatörde.
4. **Rol değişikliği yok:** anlık tayfada moderatör atanamaz; tek onaylayan
   organizatördür.
5. **Doğrudan davet sınırı:** günde 10 (bütün tayfalarla ortak). Kalabalık
   bir anlık etkinlik için link yolu kullanılır.
6. **Arşivden sonra dönüşüm yok:** "Bu ekiple tayfa kur" arşivden önce
   (bitiş + 7 gün) kullanılmalı.
7. **Tayfa açma sınırları:** anlık oluşturma saymaz; dönüştürme sayar (günde
   1 kullanıcı, saatte 1 IP).
