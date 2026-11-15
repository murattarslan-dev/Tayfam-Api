# ADR-0028: "Boşum" — arkadaşlara görünen boş vakit (v2)

- **Durum:** Önerildi
- **Tarih:** 2026-11-15
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §3; issue #207). Bu ADR onların mimariye
  taşınmasıdır; kararın bıraktığı ayrıntılar aşağıda gerekçesiyle seçildi ve
  "Açık sorular"da listelendi.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md)

## Bağlam

Kullanıcı ileriye dönük boş olduğu günleri girer; arkadaşları arkadaş
listesinde onun yanında "boş vakit" rozetini ve dokununca boş günlerini
görür. Platform sahibinin kararları (2026-11-15):

- **Bildirim yok.** Ne push ne uygulama içi bildirim; yalnızca arkadaş
  listesinde rozet.
- **Tüm arkadaşlar görür;** kişi seçimi yok, sayı sınırı yok. Notion
  Özellik Haritası'ndaki eski "bildirim + haftalık sınır" taslağı geçersizdir.
- Giriş: tarih + gün dilimi (sabah / öğleden sonra / akşam / tüm gün),
  birden çok gün, en fazla 30 gün ileri; isteğe bağlı not (60 karakter,
  kötüye kullanım filtresinden geçer; eşleşme yayını engellemez, panel
  kuyruğuna düşer).
- Geçmiş günler kendiliğinden silinir.
- Engellenen kullanıcı görmez; arkadaşlıktan çıkan görmez.

Kapsam dışı: anlık etkinlik (#209). Rozetteki "Bir şey yapalım" düğmesi
istemcide anlık etkinlik uçları gelince bağlanır.

## Karar

### 1. Sahip servis: core

Boş vakit, arkadaşlık grafiğiyle birlikte core'dadır. Görünürlük kuralı
"kabul edilmiş arkadaş ve iki yönden engel yok"tur; `friendships` ve
`user_blocks` core'da olduğu için kural tek veritabanında, tek sorguda
uygulanır (ADR-0009). identity'de tutulsaydı her okuma core'a arkadaşlık
sorardı; chat'te tutulsaydı bildirim hattına yakın dururdu, oysa bildirim
yoktur. Başka servise kopya, olay veya bildirim gitmez.

### 2. Tablo: `user_availability` (core)

| Alan | Tür | Kısıt | Not |
| --- | --- | --- | --- |
| `id` | uuid PK | UUIDv7, Go'da | Dışa aktarma sayfalaması için |
| `user_id` | uuid 🔗 | | identity kullanıcısı; FK değil |
| `day` | date | `UNIQUE(user_id, day)` | Takvim günü (§3) |
| `slots` | text[] | 1–3 eleman; `morning` / `afternoon` / `evening` / `all_day`; `all_day` tek başına | Go sıralar ve tekilleştirir |
| `note` | text | NULL ya da 1–60 karakter | Kontrol karakteri yok (Go) |
| `created_at`, `updated_at` | timestamptz | | `updated_at` trigger'la |

- Bir gün için tek satır vardır; aynı güne birden çok dilim girilebilir
  (sabah ve akşam). "Tüm gün" diğer dilimlerle birlikte seçilemez.
- Yazma, günün satırını bütünüyle değiştirir (`PUT`); silme kalıcıdır.
  İçerik tablolarındaki soft delete burada yoktur: kayıt tayfaya ait içerik
  değil, kişinin kendi durum bilgisidir; silinen boş vaktin geri alınacak ya
  da moderasyonda incelenecek bir hali yoktur ("tutmadığın veri çalınamaz",
  ADR-0011).
- `core_app` (ADR-0015) yalnızca `slots`, `note` ve `updated_at`'i
  güncelleyebilir ve satır silebilir; `user_id` ve `day` değiştirilemez.
- Sayı sınırı gerekmez: aralık en fazla 31 gündür (bugün + 30) ve günde tek
  satır vardır, yani kişi başı en fazla 31 satır.

### 3. Zaman: gün ve dilim saat değildir

- `day` bir takvim günüdür, `slots` o günün kaba bir bölümüdür; ikisi de
  bir an (instant) değildir ve UTC'ye çevrilmez. Sunucu "saat"e hiç
  dokunmaz; istemci dilimi kendi diliyle gösterir.
- **"Bugün" Europe/Istanbul takvimine göredir** (yaş hesabıyla aynı;
  Veri Modeli v15 "Yaş"). Yazmada `bugün ≤ day ≤ bugün + 30`; okumada
  `day ≥ bugün`. Yurt dışındaki bir kullanıcı için gece yarısı civarında
  birkaç saatlik kayma kabul edilir; kullanıcı başına saat dilimi tutmak bu
  kayma için gereksiz bir kişisel veri olurdu.
- Aralık Go'da ve veritabanında kontrol edilir: `INSERT` trigger'ı aynı
  aralığı Postgres'in saatiyle (`now()`, `Europe/Istanbul`) denetler. `day`
  güncellenemediği için trigger yalnızca eklemede çalışır.
- Bugünün kaydı gün bitene kadar görünür; sabah dilimi öğleden sonra
  kendiliğinden kaybolmaz (istemci isterse gizler).

### 4. Görünürlük

Bir kişinin boş vakitlerini yalnızca şunlar okur:

- Kişinin kendisi.
- Kişiyle **kabul edilmiş arkadaşlığı** olan, aralarında **iki yönden de
  engel olmayan** kullanıcı; hedef kişi canlı olmalıdır (silinmemiş, platform
  banı yok).

Diğer her durumda (arkadaş değil, istek bekliyor, engel var, hesap yok ya
da banlı) cevap `user-not-found`'dur: arkadaş olmayan biri boş vakit
girilip girilmediğini de, engellendiğini de öğrenemez (ADR-0011, mevcut
engelleme davranışıyla aynı). Kural her istekte core'da, arkadaşlık
tablosundan yeniden okunur; arkadaşlıktan çıkma ya da engelleme bir sonraki
istekte etkilidir, kopya veya önbellek yoktur. Kişi seçimi ve sayı sınırı
yoktur.

### 5. Bildirim yok

Boş vakit eklemek, değiştirmek ve silmek `domain_events`'e olay yazmaz,
`notification.requested` üretmez, push göndermez. Rozet yalnızca arkadaş
listesi çekilince hesaplanır.

### 6. Uçlar

| Uç | gRPC (`core.v1.AvailabilityService`) | Açıklama |
| --- | --- | --- |
| `GET /v1/me/availability` | `ListMyAvailability` | Kendi gelecek günlerin (en fazla 31) |
| `PUT /v1/me/availability/{date}` | `SetAvailability` | O günü ekler ya da bütünüyle değiştirir |
| `DELETE /v1/me/availability/{date}` | `DeleteAvailability` | O günü siler; yoksa da başarılı |
| `GET /v1/users/{user_id}/availability` | `ListUserAvailability` | Bir arkadaşın gelecek günleri (§4) |
| `GET /v1/me/friends` → `Friend.next_available` | `ListFriends` | Rozet: en yakın boş gün ve dilimleri |

- `next_available` yalnızca kabul edilmiş arkadaş satırında ve arkadaşın
  bugünden sonraki (bugün dahil) bir kaydı varsa döner; istek listelerinde
  hiç yoktur. Not taşımaz; not yalnızca arkadaşın günleri açılınca döner.
- Liste uçları sayfalanmaz: aralık kişi başı en fazla 31 gündür.
- Yeni hata kodu yoktur: geçersiz tarih, dilim ya da not `invalid-request`,
  görünmeyen kişi `user-not-found`.
- `ListFriends`'te rozet sayfa sorgusunun içinde (LATERAL) hesaplanır;
  arkadaş başına ek sorgu yoktur.

### 7. Not ve kötüye kullanım filtresi

Kelime filtresi (`moderation_terms`, `moderation_flags`; ADR-0012, Notion
"Kötüye Kullanım" §1) henüz şemada ve kodda yoktur (Veri Modeli v15
"Planlanan, şemada yok"). Bu ADR filtreyi yazmaz. Kararla uyumlu olarak
not, eşleşse bile yayınlanır; filtre geldiğinde `user_availability.note`
filtrenin kapsamına eklenir (eşleşme `moderation_flags`'e kullanıcı hedefli
düşer). O zamana kadar notu görebilen kitle yalnızca kişinin arkadaşlarıdır
ve not şikâyet akışında kullanıcı şikâyetiyle ele alınır. Not log'a, olaya,
denetim kaydına ve hata mesajına yazılmaz; tarih ve dilimler de log'a girmez
(kişinin programıdır).

### 8. Saklama, KVKK

- Geçmiş günler okumada süzülür ve günlük saklama işi (`retention`)
  `day < bugün` satırlarını kalıcı siler.
- Hesap silmede (`user.deleted`) kişinin bütün satırları hemen silinir.
- Platform banında satırlar silinmez, gizlenir (ban kalkarsa geri gelir;
  profilin banda gizlenmesiyle aynı).
- KVKK dışa aktarmada `core/availability.json` bölümü (gün, dilimler, not,
  zamanlar). Aydınlatma metni açısından yeni bir işleme amacı yoktur;
  kullanıcının kendi girdiği ve yalnızca arkadaşlarına gösterilen bir
  içeriktir (hukukçu listesine not olarak eklenebilir).
- Platform paneli boş vakitleri görmez.

## Sonuçlar

- Görünürlük arkadaşlık tablosuna bağlı olduğu için ayrı bir izin modeli
  yoktur; arkadaşlık ve engelleme kuralı değişirse boş vakit de kendiliğinden
  uyar.
- Kişi başı 31 satır sınırı tabloyu küçük tutar; temizlik günlük tek
  `DELETE`'tir.
- Europe/Istanbul seçimi yurt dışındaki kullanıcı için gün sınırında kayma
  bırakır; gerekirse ileride istek başına istemci saat dilimiyle
  genişletilebilir (sözleşme `date` alanını değiştirmeden).
- Filtre gelene kadar notta eşleşme panele düşmez; bu bilinçli olarak
  filtre işine bırakıldı.

## Değerlendirilen alternatifler

- **Gün başına tek dilim:** Daha basit ama "sabah ve akşam boşum" girilemez.
  Küme (text[]) seçildi; `all_day` tek başına.
- **Dilim başına satır:** Not güne aittir; aynı notun dilim başına
  tekrarlanması gerekirdi.
- **Kullanıcı saat dilimi tutmak:** Gün sınırındaki kayma için kalıcı bir
  kişisel veri olurdu; Europe/Istanbul yeterli.
- **Soft delete:** Geri alınacak ya da incelenecek bir içerik değil; kalıcı
  silme daha az veri tutar.
- **Toplu yazma ucu (bütün listeyi değiştiren PUT):** Gün başına `PUT`
  idempotenttir ve eşzamanlı iki cihazda bir günün değişikliği diğerini
  silmez; birden çok gün birden çok istekle girilir.

## Açık sorular (platform sahibine)

1. "Bugün" Europe/Istanbul takvimine göre; yurt dışındaki kullanıcı için
   kayma kabul edilebilir mi?
2. Notun kelime filtresine bağlanması filtre işine (moderation_terms) mi
   bırakılsın, yoksa ondan önce ayrı bir iş mi açılsın?
3. Aynı güne birden çok dilim (küme) ve "tüm gün" tek başına — uygun mu?
4. Rozet `next_available` 30 günlük pencerenin tamamına bakıyor (en yakın
   gün); "yakında" daha dar bir pencere mi olmalı (örn. 7 gün)?
