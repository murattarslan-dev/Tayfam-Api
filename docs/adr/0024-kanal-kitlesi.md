# ADR-0024: Kanal kitlesi — etkinlik ve seçili üye kanalları

- **Durum:** Önerildi (platform sahibinin onayını bekliyor)
- **Tarih:** 2026-11-13
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-13,
  issue #183 ve yorumları; Notion Proje Planı §6 Sohbet). Bu ADR onların
  mimariye taşınmasıdır.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0014](0014-websocket-erisim-tokeniyla.md); issue #183

## Bağlam

Kanallar bugüne kadar iki kitle tanıyordu: bütün tayfa (`members`) ve
yönetim (`staff`). İkisi de core'un tek izin tablosundan, rolle çözülür;
chat'te üyelik kopyası yoktur (ADR-0009: "Üyelik ve rol her istekte core'a
sorulur").

Platform sahibi iki kitle daha istedi (issue #183):

- **Etkinlik kanalı:** kanalı açan, etkinliğin organizatörü ve "geliyorum"
  (`going`) diyenler. Canlıdır: cevabını değiştiren çıkar; bekleme listesi,
  `maybe`, `not_going` dışarıdadır. Etkinlik bitince (iptalde iptal anı
  bitiş sayılır) 7 gün sonra arşivlenir; o 7 gün "Kanal N gün sonra
  kapanacak" bandı görünür.
- **Seçili üye kanalı:** kanalı açan ve seçtikleri; üyeliği yalnızca açan
  değiştirir.

Yönetim bu iki türe rolüyle girmez. Sonradan giren eski mesajları görür,
çıkan ya da çıkarılan hiçbirini göremez ve bildirim almaz.

Issue, katılım değişikliğinin "olayla (outbox) chat'e gitmesini" önerdi.
Bu iki şekilde okunabilir: chat katılımın kopyasını olaylardan kurar ya da
olay yalnızca chat'e "yeniden sor" der.

## Karar

### 1. Etkinlik kanalının kitlesi core'a sorulur; chat katılımın kopyasını tutmaz

- chat, etkinlik kanalına her erişimde (liste, kanal, mesaj, gönderme,
  WebSocket aboneliği ve yeniden denetimi) core'un
  `ChannelAccessService.GetEventChannelAccess`'ına **çağıranın kendi
  token'ıyla** sorar: çağıran kitlede mi (gidiyor, organizatör ya da kanalı
  açan), kanal açabilir mi, etkinlik ne zaman bitti/iptal edildi, kitle kaç
  kişi. Liste 100'lük gruplarla sorulur.
- Katılımın tek kaynağı core'dur. Kopya tutulsaydı sırasız ve tekrar gelen
  olaylarla (ADR-0002) bozulabilir, tayfadan ayrılma gibi başka yollarla
  eskiyebilir ve chat ele geçirildiğinde kimin hangi etkinliğe gittiğinin
  listesini sızdırırdı (ADR-0011 "tutmadığın veri çalınamaz").
- core'un `rsvp.changed` ve `rsvp.promoted` olayları chat'e
  `EventAttendanceChanged` olarak gider; **cevabın kendisi gitmez**. chat
  kişinin önbellekteki izinlerini düşürür ve tayfadaki aboneliklerini
  kişinin kendi token'ıyla yeniden denetler (`membership.changed` ile aynı
  model): kanaldan çıkan kişinin aboneliği hemen kesilir. Tüketici durum
  yazmadığı için kendiliğinden idempotenttir ve sıradan bağımsızdır.
- Cevaplar önbelleğe alınmaz (ilk sürüm). Yük sorun olursa
  `ChannelAccess`'teki gibi kısa süreli, olayla düşürülen bir önbellek
  eklenebilir.

### 2. Seçili üye kanalının üyeleri chat'tedir

- `channel_members` (chat): kanal ve kullanıcı kimliği, kanalı açan dahil.
  Bu chat'in kendi verisidir (kanal içeriğinin parçası); core'da karşılığı
  yoktur.
- Eklenecek kişilerin tayfanın etkin üyesi olduğu core'a sorulur
  (`FilterActiveMembers`, çağıranın token'ıyla); biri değilse hiçbiri
  eklenmez.
- Erişim için hem tayfa üyeliği (core, her istekte) hem kanal üyeliği
  (chat) gerekir. Kişi tayfadan ayrılınca, çıkarılınca ya da banlanınca
  core'un `membership.changed` olayı artık `ended` bayrağını taşır; chat o
  tayfanın seçili kanallarındaki satırlarını siler (yeniden katılan kişi
  kanallara kendiliğinden dönmez). Hangi değişiklik olduğu yine gitmez.
  Hesap silinince satırlar silinir.
- Üye çıkarılınca kanalın aboneleri yeniden denetlenir
  (`Publisher.ChannelChanged`): çıkarılanın aboneliği kesilir.

### 3. Arşiv zamanı chat'te, bilgi olayla gelir

- Etkinlik kanalı açılırken core'un söylediği bitiş (ya da iptal anı)
  + 7 gün `channels.archive_at` olarak yazılır. core'un `event.updated`
  (yalnızca zaman değiştiğinde; yük artık `ends_at` taşır) ve
  `event.cancelled` (`cancelled_at`) olayları chat'e
  `EventScheduleChanged` olarak gider; chat bilgiyi yalnızca elindekinden
  yeniyse uygular (`archive_source_at`, olayın core'daki zamanı).
- Kanal `archive_at`'ten itibaren yazmaya kapalıdır; saatlik iş
  `archived_at`'i doldurur. Zamanı geçmiş etkinlik kanalı arşivden
  çıkarılamaz. Bir etkinliğin aynı anda tek açık kanalı olur.
- Arka plan işinin kullanıcı token'ı olmadığı için zamanlama bilgisi
  olayla gelir; kitle bilgisi (kişisel) gelmez.

### 4. Kim açar, kim yönetir

- Etkinlik kanalını yönetici, moderatör ya da etkinliği yönetebilen kişi
  (`caller_can_manage`; susturulmuşken ve tayfa salt okunurken değil)
  açar — bu cevap da core'dandır. Seçili üye kanalını yönetici ve
  moderatör açar (`CHANNEL_ACTION_MANAGE`).
- Bu iki türün adını, yavaş modunu ve arşivini mevcut kural yönetir
  (`MANAGE`), ama kanalı görmeyen yönetim onu bulamaz. Tür sonradan
  değiştirilemez (`members`/`staff` ile `event`/`selected` arasında geçiş
  yok).

## Sonuçlar

- chat, etkinlik kanallarında her istekte core'a bir çağrı daha yapar.
- core'un ChannelAccessService'i üç metod kazanır; hepsi çağıranın
  token'ını ister, etkin üye olmayana `tayfa-not-found` der.
- chat'in olay sözleşmesi iki olay ve `MembershipChanged.ended` ile büyür;
  core'un `event.updated`/`event.cancelled` yükü zaman alanlarıyla büyür.
- `channel_members` KVKK veri dışa aktarmasına bugün eklenmedi (okundu
  işaretleri gibi); gerekirse ayrı karar.
- Notion veri modeli (v15) `channels.event_id`, `archive_at`,
  `archive_source_at` ve `channel_members` ile güncellenmelidir.

## Değerlendirilen alternatifler

- **Katılımın chat'te kopyası (olaylardan):** her istekte core çağrısı
  olmazdı; ama sıra/tekrar sorunları, eskime ve chat ele geçirildiğinde
  sızan katılım listesi yüzünden reddedildi.
- **gateway'in core'dan aldığı kitleyi chat'e iletmesi:** ADR-0011
  ("gateway'den geçti" gerekçe değildir) gereği reddedildi.
- **Arşivin her erişimde core'dan hesaplanması:** kimse kanala bakmasa da
  arşivlenmesi gerektiği için (iş, kullanıcısız çalışır) yetmez.
