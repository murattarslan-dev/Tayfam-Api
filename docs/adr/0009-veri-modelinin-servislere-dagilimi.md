# ADR-0009: Veri modelinin servislere dağılımı

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md), [ADR-0008](0008-kvkk-ve-platform-yonetimi.md)
- **Kaynak:** Notion: Veri Modeli (taslak v13) ve ER diyagramı

## Bağlam

Notion'daki veri modeli v13 tek bir PostgreSQL veritabanı varsayar; tablolar
arasında foreign key'ler ve `ON DELETE CASCADE / SET NULL` davranışları vardır.
ADR-0002 ile her servisin kendi veritabanı olacağına karar verildi. Bu ADR,
v13'teki tabloların hangi servise ait olduğunu ve servis sınırını geçen
foreign key'lerin yerine ne kullanılacağını belirler.

v13'teki genel kurallar (UUIDv7 ID'ler, `created_at`/`updated_at`,
`text + CHECK` sabit değerler, uzunluk sınırları, soft delete ve 30 günlük
kalıcı silme, `client_request_id` ile tekrar gönderim koruması, kısmi
indeksler, Türkçe `name_normalized` alanları) **her servisin veritabanında
aynen geçerlidir**. Bu ADR yalnızca dağılımı ve servisler arası ilişkileri
ele alır.

## Karar

### Tabloların sahipliği

**identity** (kimlik, oturum, KVKK ve platform yönetimi)

- `users`, `auth_identities`, `devices`, `sessions`, `refresh_tokens`
- `consent_records`, `data_export_requests`
- `platform_admins`, `platform_audit_logs`, `platform_sanctions`

**core** (tayfa ve tayfa içi her şey, sosyal ilişkiler, medya)

- Tayfa: `tayfas`, `tayfa_interests`, `tayfa_modules`, `memberships`,
  `invites`
- Duyuru ve etkinlik: `announcements`, `events`, `event_rsvps`,
  `event_series`, türe özel tablolar (`event_convoy`, `event_camp`,
  `event_hike`, `event_fishing`, `event_gaming`, `event_sports` ve alt
  tabloları), `fishing_catches`, turnuva tabloları (Faz 2)
- Soru-cevap: `questions`, `answers`, `answer_votes`, `tags`, `question_tags`
- Anket: `polls`, `poll_options`, `poll_votes`
- Sosyal: `friendships`, `user_blocks`
- Geçmiş: `user_activity_records`
- Dosyalar: `uploads`
- Denetim: `reports`, `audit_logs`
- Abonelik (Faz 2): `plans`, `tayfa_subscriptions`, `payment_events`

**chat** (mesajlaşma, E2E anahtarları, bildirimler)

- Kanal: `channels`, `channel_messages`, `channel_reads`
- Birebir: `dm_conversations`, `dm_messages`, `dm_envelopes`,
  `dm_conversation_reads`
- DM arşivi (ADR-0005, v13'e eklenir): `dm_archive_entries`,
  `history_key_shares`, `device_link_requests`
- E2E anahtarları: `device_keys` (v13'teki `devices` tablosunun anahtar
  alanları), `device_signed_prekeys`, `device_one_time_prekeys`,
  `user_key_events`
- Bildirim: `notifications`, `notification_preferences`, `push_tokens`
- Bağlantı: `realtime_tickets`

**Her serviste:** `domain_events`, `event_consumers` (outbox, ADR-0002) ve
`user_profiles` (aşağıya bakın).

### `devices` tablosunun bölünmesi

v13'teki `devices` tablosu hem oturum, hem push, hem de E2E alanları taşır.
Bunlar üç sorumluluk olduğu için bölünür:

- identity `devices`: `id`, `user_id`, `platform`, `last_seen_at`. Cihazın
  varlığının tek kaynağıdır; `sessions.device_id` buna bağlıdır.
- chat `device_keys`: `device_id`, `identity_key_public`, `registration_id`.
- chat `push_tokens`: `device_id`, `push_token`.

identity bir cihazı silince (`device.removed`), chat o cihazın anahtarlarını,
ön anahtarlarını, push token'ını ve bekleyen zarflarını siler ve
`user_key_events`'e kayıt yazar.

### Kullanıcı profili kopyaları

core ve chat, listelerde yazar adı ve avatar göstermek için her istekte
identity'ye sormaz. Bunun yerine kendi veritabanlarında salt okunur bir
`user_profiles (user_id, display_name, avatar_upload_id, updated_at)` kopyası
tutar. Kopya identity'nin `user.profile_updated` ve `user.deleted` olaylarıyla
güncellenir. Kopyanın kısa süre eski kalması kabul edilir.

### Servisler arası foreign key'lerin karşılıkları

| v13'teki ilişki | Yeni karşılığı |
|-----------------|----------------|
| Kullanıcı → üyelik, katılım, oy, arkadaşlık, engelleme, katılım geçmişi (CASCADE) | identity `user.deleted` yayınlar, core siler |
| Kullanıcı → cihaz anahtarları, zarflar, bildirimler, okunma bilgisi (CASCADE) | identity `user.deleted` yayınlar, chat siler |
| Kullanıcı → yazdığı içerik (`author_id`, `created_by` vb., SET NULL) | `user.deleted` ile core ve chat yazar alanını `null` yapar |
| `users.avatar_upload_id` → `uploads` | ID ile referans; core yüklemeyi `attached` yapar |
| Tayfa → kanallar ve mesajları (CASCADE) | core `tayfa.deleted` ve kalıcı silmede `tayfa.purged` yayınlar, chat soft delete ve kalıcı silme uygular |
| Üyelik değişiklikleri (rol, çıkarma, ban, susturma) | core `membership.changed` yayınlar; chat önbelleğini temizler, `staff` kanal aboneliklerini keser |
| Tayfa askı ve arşiv | core `tayfa.status_changed` yayınlar; chat kanalları salt okunur yapar |
| Oturum iptali, platform yaptırımı | identity `session.revoked`, `user.sanctioned` yayınlar; chat WebSocket bağlantılarını kapatır |
| Bildirim oluşturma | core ve identity `notification.requested` yayınlar; chat kaydı yazar ve push gönderir |

Tüm tüketici metodları idempotenttir (ADR-0002).

### Senkron sorgular (gRPC)

| Çağıran → hedef | Metod | Amaç |
|-----------------|-------|------|
| chat → core | `GetMembership(tayfa_id, user_id)` | Kanal erişimi, rol, susturma; kısa süre önbelleklenir |
| chat → core | `CheckDirectMessage(sender_id, recipient_id)` | Notion v7 kuralları: ortak aktif tayfa, arkadaşlık, engelleme, ortak etkinlik geçmişi. Sonuç: yazamaz, istek, aktif |
| chat → core | `ListActiveMemberIDs(tayfa_id)` | Canlı olay ve bildirim dağıtımı |
| identity → core, chat | `ExportUserData(user_id)` | KVKK veri dışa aktarma (ADR-0008) |
| gateway → hepsi | Uygulama ve panel uçları | ADR-0002, ADR-0008 |

Sosyal ilişkiler (arkadaşlık, engelleme) core'dadır; böylece birebir mesaj izni
tek bir servise tek bir çağrıyla sorulur.

### Veri modeline eklenenler

- `tayfas.cover_upload_id`, `events.cover_upload_id`, her ikisinde
  `cover_public_key` (herkese açık kopyanın adı) ve paylaşılabilirlik alanı
  (ADR-0007). `uploads.purpose` değerlerine `cover` eklenir.
- DM arşivi tabloları (ADR-0005).
- Her serviste `user_profiles` kopyası.

Notion'daki veri modeli sayfası bu ADR'ye göre güncellenir; ER diyagramı servis
sınırlarını gösterecek şekilde yeniden çizilir.

### Veritabanı içi korumalar

v13'teki trigger ve kısıtlar (son yönetici koruması, rol hiyerarşisi, tek
seçimli ankette tek oy, etkinlik türü bütünlüğü için bileşik foreign key)
**servis içinde kaldıkları sürece** aynen uygulanır. Hepsi core'un kendi
tablolarıyla ilgili olduğu için bölünmeden etkilenmez.

## Gerekçe

- Sahiplik çizgileri sorumluluklara göre çizilmiştir: kimlik ve hesap,
  tayfa ve içerik, mesajlaşma ve bildirim.
- Sosyal grafiği core'da toplamak, en sık kullanılan servisler arası
  sorguyu (birebir mesaj izni) tek çağrıya indirir.
- Profil kopyaları, listelerde servisler arası N+1 çağrıyı önler ve olay ile
  durum aktarımı (event-carried state transfer) desenini öğrenme fırsatı
  sunar.
- Foreign key davranışlarını olaylara çevirmek, ADR-0002'deki "veritabanları
  arası foreign key yok" kuralını, v13'teki silme ve anonimleştirme
  davranışlarını kaybetmeden uygular.

## Sonuçlar

- Bir kullanıcının silinmesi veya bir tayfanın kalıcı silinmesi servisler
  arasında nihai tutarlılıkla tamamlanır.
- Servisler arası referans bütünlüğü veritabanı tarafından değil, olaylar ve
  idempotent tüketicilerle sağlanır. Bunu doğrulayan entegrasyon testleri
  gerekir (örneğin "kullanıcı silindikten sonra core ve chat'te kişisel kayıt
  kalmaz").
- Notion'daki veri modeli ve ER diyagramı güncellenmelidir.

## Değerlendirilen alternatifler

- **Arkadaşlık ve engellemeyi chat'e koymak:** Birebir mesaj kuralına yakın,
  ama arkadaşlık davet ve profil görünürlüğü gibi core işlerinde de
  kullanılıyor; ortak etkinlik geçmişi zaten core'da.
- **Bildirimleri core'da tutmak:** Bildirim kaynaklarının çoğu core'da, ama
  push gönderimi ve açık bağlantı bilgisi chat'te; bildirimi gösteren ve
  ileten yer bir arada tutuldu.
- **Profil bilgisini her istekte identity'den almak:** Kopya tutmaz, ama her
  liste için ek çağrı ve identity'ye sıkı bağımlılık getirir.
