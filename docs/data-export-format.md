# Veri dışa aktarma biçimi (sürüm 1)

KVKK veri dışa aktarmasının (issue #144, [ADR-0021](adr/0021-kvkk-veri-disa-aktarma.md))
dosya biçimi ve alanları. Bu belge sözleşmedir: core ve chat'in
`ExportMyData` akışlarındaki kayıtlar ve istemcinin çözdüğü dosya buna
uyar. Alan eklemek uyumludur; alan silmek ya da anlamını değiştirmek
`format_version`'ı artırır.

## Şifreli dosya (`aes-256-gcm-v1`)

İstemci `POST /v1/me/data-exports/{data_export_id}/download` yanıtındaki
`url`'den dosyayı indirir ve aynı yanıttaki `key` (base64, 32 bayt) ile
çözer:

```
bayt 0        : 0x01 (biçim sürümü)
bayt 1..12    : nonce (12 bayt, rastgele)
bayt 13..     : AES-256-GCM şifreli metin, sonunda 16 baytlık etiket
ek veri (AAD) : "tayfam/data-export/v1/" (ASCII, 22 bayt) + talep kimliğinin 16 baytı (UUID, ağ sırası)
```

Çözülen içerik bir zip dosyasıdır (Deflate). Şifreli dosya en çok
16 MiB + 29 bayttır. Etiket doğrulanmazsa dosya kullanılmaz.

## Zip içeriği

Her dosya UTF-8 JSON'dur. Bölüm dosyaları bir JSON dizisidir (`[…]`); boş
bölümün dosyası `[]`'dir. Zamanlar RFC 3339, UTC. Kimlikler UUID metnidir.
Diğer kullanıcılar yalnızca kimlikle geçer (ad yoktur); tayfa ve etkinlik
adları içeriktir ve yer alır.

### `manifest.json`

| Alan | Açıklama |
|------|----------|
| `format_version` | `1` |
| `data_export_id` | Talep kimliği |
| `user_id` | Kullanıcı |
| `generated_at` | Arşivin oluşturulduğu an |
| `sections` | `[{"file": "core/events.json", "records": 3}, …]` |

### identity

| Dosya | Alanlar |
|-------|---------|
| `identity/profile.json` | `user_id`, `display_name`, `avatar_upload_id`, `locale`, `birth_date` (YYYY-AA-GG), `birth_date_visibility`, `birth_date_audience`, `activity_visibility`, `stats_visibility` (issue #267), `ref_code`, `referred_by_user_id`, `referred_at`, `registered_at`, `created_at`; profil tamamlama formu (issue #186): `interests`, `about`, `occupation`, `team`, `languages`, `social_accounts` (`platform`, `username`), `declined_profile_fields`, `about_visibility`, `profile_prompt_snoozed_until` (tek kayıtlık dizi) |
| `identity/sign_in_identities.json` | `provider`, `email`, `created_at`, `last_used_at` |
| `identity/devices.json` | `device_id`, `platform`, `created_at`, `last_seen_at` |
| `identity/consents.json` | `document_type`, `document_version`, `accepted_at`, `withdrawn_at` |
| `identity/data_exports.json` | `data_export_id`, `status`, `requested_at`, `ready_at`, `expires_at`, `downloaded_at` |

### core

| Dosya | Alanlar |
|-------|---------|
| `core/memberships.json` | `tayfa_id`, `tayfa_name`, `role`, `status`, `joined_via`, `joined_at`, `muted_until`, `created_at` |
| `core/event_rsvps.json` | `event_id`, `tayfa_id`, `event_title`, `status`, `attendance`, `attendance_method`, `checked_in_at`, `created_at`, `updated_at` |
| `core/hike_checkins.json` | Etkinliklerdeki yoklama kayıtlarınız (`roll_call` bileşeni, #242; dosya adı Faz 1'den kalmadır, bütün türlerin yoklamalarını taşır), etkinlik başına bir kayıt: `event_id`, `roll_calls` (`[{component_key, started_at, returned_at, created_at}]`; dönmediyseniz `returned_at` `null`) |
| `core/convoy_vehicles.json` | Etkinliklere yazdığınız araçlar (`vehicles` bileşeni; dosya adı Faz 1'den kalmadır, bütün etkinlik türlerinin araçlarını taşır): `event_id`, `component_key`, `make`, `model`, `model_year`, `color`, `plate`, `seats`, `garage_vehicle_id` (garajınızdan seçildiyse; yoksa `null`), `created_at`, `updated_at` |
| `core/vehicle_seat_requests.json` | Başkasının aracına yazdığınız koltuk istekleri: `event_id`, `component_key`, `vehicle_id`, `status` (`pending`, `approved`), `created_at`, `decided_at`. Aracın sahibi ve plakası dışa aktarılmaz |
| `core/measurements.json` | Etkinliklerdeki kişisel ölçüm değerleriniz (ölçüm bileşeni): kendi girdikleriniz ve organizatörün ya da hakemin sizin adınıza girdikleri. `event_id`, `component_key`, `value`, `entered_by_self` (değeri siz mi girdiniz), `created_at`, `updated_at`. Etkinliğin toplam ölçümleri etkinliğe aittir ve dışa aktarılmaz |
| `core/team_memberships.json` | Etkinliklerde içinde olduğunuz takımlar (takım kurma bileşeni). `event_id`, `component_key`, `team_id`, `team_position` (takımın sırası), `created_at`. Takım arkadaşlarınız ve takımın adı dışa aktarılmaz |
| `core/scores.json` | Etkinliklerde yazboz masalarındaki yerleriniz ve skorlarınız (yazboz bileşeni). `event_id`, `component_key`, `table_id`, `table_label`, `preset_key`, `position` (masadaki yeriniz), `scores` (el el: `round`, `points` ya da `outcome`: `win`, `draw`, `loss`), `created_at`. Masadaki diğer kişiler ve skorları dışa aktarılmaz |
| `core/bracket_matches.json` | Eşleşme ağaçlarındaki maçlarınız (eşleşme ağacı bileşeni), kuradaki yeriniz üzerinden. `event_id`, `component_key`, `round` (tur), `position` (kuradaki yeriniz), `games` (oyun oyun: `number`, kendi `points`'iniz ya da kendi `outcome`'unuz: `win`, `draw`, `loss`), `created_at`. Rakibiniz ve skoru dışa aktarılmaz |
| `core/ranking_entries.json` | Etkinliklerde organizatörün ya da hakemin sizin adınıza elle girdiği dereceler (derece bileşeni): `event_id`, `component_key`, `value` (süre saniye olarak ya da sıra; bileşenin ölçütüne göre), `created_at`, `updated_at`. Ölçümden hesaplanan dereceler `core/measurements.json`'daki değerlerinizdir; sıranız saklanmaz |
| `core/celebration_wins.json` | Kutlamalarda organizatörün sizi elle kazanan seçtiği etkinlikler (kutlama bileşeni): `event_id`, `component_key`, `announced_at` (ilan zamanı), `created_at`. Kimin ilan ettiği ve diğer kazananlar dışa aktarılmaz; yazboz, eşleşme ağacı ve dereceden gelen kazanmalarınız o bölümlerdedir |
| `core/event_questions.json` | Etkinliklerde sorduğunuz sorular (anonim olanlar dahil) ve oy verdiğiniz sorular (soru toplama bileşeni): `id`, `event_id`, `component_key`, `asked` (soruyu siz mi sordunuz); kendi sorularınızda `body`, `anonymous`, `answered_at` (cevaplandı işaretlendiyse), `created_at`, `deleted_at`; oy verdiyseniz `voted_at`. Başkasının sorusunun metni ve yazarı, diğer oylar ve kimin cevaplandı işaretlediği dışa aktarılmaz |
| `core/badges.json` | Kazandığınız rozetler: `badge_key` (rozetin katalogdaki anahtarı; adı ve ikonu katalogdadır), `period` (`all` ya da yıllık rozette yıl), `earned_at`; şampiyonluk rozetinde `event_id` ve `tayfa_id`. Tayfa rozetleri tayfanındır, dışa aktarılmaz |
| `core/user_stats.json` | Kişisel istatistiğiniz (ADR-0026): `tayfa_id`, `year` (Europe/Istanbul takvim yılı; tüm zamanlarda `null`), `metric_key` (ortak metrik `attended` / `duration` ya da `<tür>.<metrik>`, ölçümde `<tür>.<ölçüm>`, günlükte `<tür>.<günlük>.<stat>`), `value`, `event_count` (katkı veren etkinlik sayısı), `computed_at`. Donmuş etkinliklerden türetilir; hassas ölçümler ve hassas katılımlı türler yoktur |
| `core/daily_items.json` | Tayfaların günün içeriği havuzuna eklediğiniz içerikler (silinenler dahil): `id`, `tayfa_id`, `body`, `attribution` (söyleyen ya da kaynak; yoksa `null`), `pin_month`, `pin_day`, `pin_year` (sabitlenmemişse `null`; yılsız sabitleme her yıl), `created_at`, `updated_at`, `deleted_at`. Havuzdaki sırası ve kimin sildiği dışa aktarılmaz |
| `core/library_items.json` | Ortak kütüphanede sahibi olduğunuz kalemler (geri çekilenler dahil): `id`, `tayfa_id`, `kind` (`book`, `camping_gear`, `board_game`, `tool`, `other`), `name`, `note` (yoksa ya da geri çekildiyse `null`), `status` (`available`, `on_loan`, `withdrawn`), `created_at`, `updated_at`, `withdrawn_at`. Fotoğraf dışa aktarılmaz |
| `core/library_loans.json` | Ortak kütüphanedeki ödünçleriniz (borç alan ya da kalemin sahibi olarak; biten ödünç 1 yıl tutulur) ve beklediğiniz sıralar: `id`, `item_id`, `tayfa_id`, `item_name`, `role` (`borrower` / `owner`), `state` (`requested`, `approved`, `handed_over`, `returned`, `declined`, `cancelled`, `closed`; sıradaki yer için `queued`), `requested_at` (sırada: sıraya girdiğiniz zaman), `approved_at`, `handed_over_at`, `due_at`, `ended_at`. Karşı taraf dışa aktarılmaz |
| `core/materials.json` | Materyal kütüphanesine eklediğiniz materyaller (silinenler dahil; silinen 30 gün sonra kalıcı silinir): `id`, `tayfa_id`, `kind` (`link` / `note_photo`), `title`, `url` ve `domain` (yalnızca bağlantıda; yoksa `null`), `subject_key`, `topic_key` (yoksa `null`), `created_at`, `updated_at`, `deleted_at`. Fotoğraf ve materyali kimin sildiği dışa aktarılmaz |
| `core/garage_vehicles.json` | Üye garajınızdaki araçlar (silinenler dahil; silinen 30 gün sonra kalıcı silinir): `id`, `make`, `model`, `model_year`, `color`, `note` (yoksa `null`), `created_at`, `updated_at`, `deleted_at`. Garajda plaka yoktur; fotoğraf ve aracı kimin kaldırdığı dışa aktarılmaz |
| `core/garage_visibility.json` | Garajınızı tayfa başına gösterme seçimleriniz: `tayfa_id`, `visible`, `updated_at`. Seçim yapmadığınız tayfalarda varsayılan (tayfanın ilgi alanında `automotive` varsa görünür) geçerlidir ve satır yoktur |
| `core/guest_players.json` | Etkinliklere eklediğiniz misafir oyuncular (yazboz bileşeni): `id`, `event_id`, `nickname` (takma ad; etkinliğin bitiminden 30 gün sonra silinir, o zaman `null`), `created_at`. Misafirin masa ve kura yerleri ve skorları dışa aktarılmaz |
| `core/draw_entries.json` | Etkinliklerdeki kuralarda çekilen yeriniz (kura bileşeni). `event_id`, `component_key`, `position` (kuradaki sıranız), `drawn_at`. Kuradaki diğer kişiler dışa aktarılmaz |
| `core/event_referees.json` | Etkinliklerdeki hakem atamalarınız (#238): `event_id`, `tayfa_id`, `component_key` (bütün bileşenlerdeyse `null`), `scope_id` (masa ya da maç; bileşenin tamamıysa `null`), `created_at`. Atayan ve diğer hakemler dışa aktarılmaz |
| `core/checklist_items.json` | Liste bileşeninde (#241) eklediğiniz ya da üstlendiğiniz maddeler: `id`, `event_id`, `component_key`, `name`, `quantity`, `assigned_to_me`, `added_by_me`, `created_at`, `updated_at`. Maddeyi başkası eklediyse ya da üstlendiyse o kişi dışa aktarılmaz |
| `core/vocabulary.json` | Tayfa sözlüğüne ilk eklediğiniz değerler (#244): `id`, `tayfa_id`, `namespace` (ad alanı, ör. `fish_species`), `value`, `merged_into` (moderatör başka bir değerle birleştirdiyse hedefin kimliği; yoksa `null`), `created_at`. Değer tayfanın ortak verisidir; hesap silinince kalır, ekleyen boşalır |
| `core/activity_records.json` | `id`, `event_id`, `tayfa_id`, `event_type`, `mode`, `event_title`, `city`, `district`, `started_at`, `duration_minutes`, `attendance_method` |
| `core/announcements.json` | `id`, `tayfa_id`, `title`, `body`, `pinned`, `created_at`, `updated_at`, `deleted_at` |
| `core/events.json` | `id`, `tayfa_id`, `type`, `mode`, `title`, `description`, `starts_at`, `ends_at`, `meeting_point_name`, `meeting_lat`, `meeting_lng`, `city`, `district`, `online_url`, `capacity`, `status`, `cancelled_at`, `cancel_reason`, `type_version` (katalog türünün sürümü), `open_ended` ("Şimdi başlat" ile açılan açık uçlu etkinlik, ADR-0038), `fields` (türün alan değerleri: alan anahtarı → değer; alan yoksa `{}`), `created_at`, `updated_at`, `deleted_at`. Etkinliğin bileşen içerikleri (rota, program, hedef vb.) etkinliğe aittir ve bu dosyada yoktur; kendi katkılarınız ilgili bölümlerdedir |
| `core/questions.json` | Soru-Cevap ve soru havuzu soruları: `id`, `tayfa_id`, `pool` (`qa` / `question_pool`), `subject_key`, `topic_key` (soru havuzunda ders ve konu anahtarı, yoksa `null`), `title` (soru havuzunda boş), `body`, `created_at`, `updated_at`, `deleted_at`. Fotoğraf dışa aktarılmaz |
| `core/answers.json` | `id`, `question_id`, `tayfa_id`, `pool`, `body`, `vote_count`, `created_at`, `updated_at`, `deleted_at`. Fotoğraf dışa aktarılmaz |
| `core/answer_votes.json` | `answer_id`, `created_at` |
| `core/polls.json` | `id`, `tayfa_id`, `event_id`, `question`, `anonymous`, `multiple_choice`, `options` (`[{id, text, position}]`), `closes_at`, `closed_at`, `created_at`, `deleted_at` |
| `core/poll_votes.json` | `poll_id`, `option_id`, `option_text`, `poll_question`, `anonymous`, `created_at` (gizli anket dahil yalnızca kullanıcının kendi oyu) |
| `core/fishing_catches.json` | Bütün günlük kayıtları (#243; ad Faz 1'den): `id`, `event_id`, `component_key`, `values`, `vocabulary` (sözlükten seçilen alanlarda alan anahtarı → `{vocabulary_id, value}`, #244), `lat`, `lng`, `location_visibility`, `photo_upload_id`, `created_at`, `deleted_at` |
| `core/event_photos.json` | Etkinlik albümlerine yüklediğiniz fotoğraflar (#206; silinenler dahil): `id`, `event_id`, `tayfa_id`, `upload_id`, `caption`, `created_at`, `deleted_at`. Dosyaların kendisi yoktur (`core/uploads.json`) |
| `core/friendships.json` | `user_id` (karşı taraf), `status`, `requested_by_me`, `created_at`, `accepted_at` |
| `core/blocks.json` | `user_id` (engellenen), `created_at` |
| `core/availability.json` | "Boşum" (#207): `date` (YYYY-MM-DD), `slots`, `note` (yoksa null), `created_at`, `updated_at`. Geçmiş günler saklanmadığı için yalnızca bugün ve sonrası |
| `core/reports.json` | `id`, `tayfa_id`, `target_type`, `target_id`, `reason`, `description`, `evidence_text`, `status`, `resolution`, `created_at` |
| `core/social_settings.json` | Ayar değiştirildiyse ya da puan tutuluyorsa tek kayıt: `profiling_enabled`, `hide_from_suggestions`, `discover_visible_to_friends` (ADR-0033), `now_from_friends` (ADR-0038 §4), `updated_at` (hiç değiştirilmediyse `null`), `scored_people_count` (hakkınızda arkadaşlık puanı tutulan kişi sayısı; puan değerleri ve kişiler girmez, ADR-0030) |
| `core/friend_suggestions.json` | `user_id` (önerilen kişi), `created_at`, `dismissed_at` (kapatılmadıysa `null`), `withdrawn_at` (geri çekilmediyse `null`) |
| `core/discover.json` | Keşfet güvenlik ağı (ADR-0033); veri varsa tek kayıt: `feedback` ("Her şey yolunda mıydı?" cevaplarınız: `event_id`, `answer` = `ok` / `not_ok` / `serious`, `created_at`, `updated_at`) ve `safety_contacts` (seçtiğiniz güvenlik kişileri: `event_id`, `contact_user_id`, `grace_minutes`, `returned_at`, `alert_sent_at`, `created_at`) ve `discover_joins` (Keşfet'ten katıldığınız tayfalar: `tayfa_id`, `joined_at`, `left_at`; ayrılıştan 30 gün sonra silinir). Başkasının sizi güvenlik kişisi seçtiği kayıtlar o kişinin verisidir, girmez |
| `core/uploads.json` | `id`, `purpose`, `status`, `tayfa_id`, `width`, `height`, `attached_type`, `attached_id`, `created_at`, `ready_at` (dosyaların kendisi yoktur; istemci kendi dosyalarını `POST /v1/media/urls` ile indirir) |

### chat

| Dosya | Alanlar |
|-------|---------|
| `chat/channel_messages.json` | `id`, `channel_id`, `tayfa_id`, `body` (silinmiş mesajda `null`), `created_at`, `deleted_at` |
| `chat/notification_preferences.json` | `scope_type` (`tayfa`, `tayfa_now`: tayfanın şimdi bildirimleri, `dm_conversation`), `scope_id`, `level`, `muted_until`, `updated_at` |

## Girmeyenler

Birebir mesajlar ve meta verisi (uçtan uca şifreli; istemci cihazda dışa
aktarır), push token'ları, refresh token'lar ve oturum ayrıntıları, IP
kayıtları, bildirim listesi (90 günlük geçici kayıt), denetim kayıtları,
moderasyon işaretleri, şikâyetlerin öncelik ve platform alanları,
başka kullanıcıların verisi ve yüklenen dosyaların kendisi (ADR-0021
madde 5 ve 7).

v3 denetimiyle (issue #275) açıkça girmeyenler: QR katılım kodları (2
dakikalık; yalnızca SHA-256 özeti saklanır), şimdi bildirimi sayaçları,
oluşturduğunuz davet bağlantıları (yalnızca özeti saklanır), etkinlik
serisinin tekrar kuralı (serinin etkinlikleri `core/events.json`'dadır),
başkasının verisi hakkında yaptığınız organizatör ve moderatör işlemleri
(kimin sildiği, gizlediği, cevaplandı işaretlediği, ilan ettiği, başkası
adına girdiği ölçüm ve derece, yoklamayı işaretleyen, geri çeken) ve
arkadaşlık isteği ile davet sonuç sayaçları (ADR-0030; yalnızca puanlamanın
girdisi). Çekirdekteki kişi kolonlarının tam listesi ve her birinin hesap
silmede ne olduğu `internal/core/app/userevents/kvkk_test.go`'dadır; yeni bir
kişi kolonu bu listeye eklenmeden CI geçmez.
