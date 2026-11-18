# ADR-0026: Tayfa istatistikleri, kişisel istatistik ve Keşfet vitrini (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15)
- **Tarih:** 2026-11-15
- **Revizyon:** 2026-11-18 ([ADR-0039](0039-rozetler.md)): §6'daki rozetler
  ayrı ADR'de karara bağlandı.
- **Revizyon:** 2026-11-18 (platform sahibi, sohbette;
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md) §7): `log`
  metrikleri, organizatörün girdiği kişisel ölçümler,
  `sensitive_attendance` türleri.
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 3 (v3)" §5). Bu ADR onların mimariye taşınmasıdır.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0027](0027-tayfa-ana-sayfa-bilesenleri.md)

## Bağlam

v3'ün ikinci derinliği tayfa istatistikleridir: çevre kulübünün bir yılda
topladığı çöp, kitap kulübünün okuduğu sayfa, yürüyüş tayfasının çıktığı
irtifa. Kaynak, ADR-0025'teki `measure`, `log` ve `scoreboard`
bileşenleriyle temel katılım verisidir.

Platform sahibinin kararları (2026-11-15):

- Ölçümün **kişisel mi toplam mı** olduğunu tür belirler; kişisel girişi
  yalnızca check-in'i doğrulanmış katılımcı yapar, toplamı organizatör girer.
- İstatistikler tayfanın **Keşfet vitrinidir**. Keşfet'te görünen tayfa
  istatistiklerini gösterir; yönetim "vitrinde göster"i kapatabilir.
- Vitrinde **yalnızca tayfa toplamları**; kişisel katkı tayfa içinde kalır.
- Kullanıcı isterse **bireysel istatistiklerini profilinde** paylaşır.
- Vitrinde **konum ve zaman örüntüsü yok**: yalnızca sayılar, en fazla şehir
  ve yıl.
- **3 etkinlikten** azı olan tayfanın istatistik kartı vitrine çıkmaz.
- Eğitim'de deneme netleri yalnızca kişiseldir; vitrine çıkmaz, sıralama
  gösterilmez. "Çözülen soru" = en az 1 faydalı oy almış cevabı olan soru.

Keşfet'in kendisi (listeleme, arkadaş ağı üzerinden ilk aşama, güvenlik
ağı) Faz 2'dir ve ayrı bir ADR'de tasarlanır. Bu ADR yalnızca istatistiğin
verisini, hesaplanmasını ve görünürlük kurallarını belirler.

## Karar

### 1. Kaynak: donmuş veri

İstatistik yalnızca **donmuş** etkinliklerden hesaplanır: bitişinden 7 gün
geçmiş (düzeltme penceresi kapanmış) ve iptal edilmemiş etkinlikler. Böylece
bir sayı vitrine çıktıktan sonra geriye dönük değişmez ve organizatör
düzeltmeleri istatistiği oynatmaz.

Silinen etkinlik (soft delete) istatistikten düşer; bir sonraki hesaplamada
toplam yeniden kurulur.

### 2. Toplamlar core'da, ayrı tabloda

- `tayfa_stats` (core): `tayfa_id`, `year`, `metric_key`, `value`,
  `event_count`, `computed_at`. `metric_key` iki türlüdür:
  - **Ortak metrikler:** etkinlik sayısı, toplam katılım (attended),
    farklı katılımcı sayısı, toplam süre. Her türde vardır, "Genel" dahil.
  - **Ölçüm metrikleri:** `<tür>.<ölçüm anahtarı>` (ör.
    `trash_pickup.weight`), birimi tür sürümünün tanımından.
- `user_stats` (core): aynı yapıda, kişi başına; yalnızca `scope: personal`
  ölçümler ve kişinin kendi katılımı.
- Hesaplama arka plan işidir (ADR-0009'daki `jobs` modeli): etkinlik
  donduğunda o tayfanın ve katılımcıların ilgili yılı yeniden hesaplanır.
  İş idempotenttir; aynı yıl iki kez hesaplansa aynı sonucu yazar.
- Tablolar **türetilmiştir**: silinip kaynaktan yeniden kurulabilir.
  Yedeklemede atlanmaz ama kaynakla çelişirse kaynak kazanır.

### 3. Görünürlük katmanları

| Kitle | Ne görür |
|-------|----------|
| Tayfa üyesi | Tayfa toplamları (her yıl ve tüm zamanlar), kişisel katkılar (kişisel ölçümlerde kim ne kadar; `sensitive` ölçümler hariç) |
| Keşfet vitrini (üye olmayan) | Yalnızca vitrine izin verilen ölçüm metrikleri ve ortak metriklerin **tayfa toplamı**; yıl ve tayfanın şehri. Kişi, tarih, saat, gün, ilçe, koordinat yok |
| Profil (başkası) | Kişinin seçtiği görünürlüğe göre kendi `user_stats`'ı |

Kurallar:

- **Vitrin bayrağı:** `tayfas.stats_showcase` (varsayılan `true`). Yalnızca
  yönetici değiştirir; izin tablosunda `tayfa.stats_showcase.update`.
  Değişiklik denetim kaydına yazılır.
- **Eşik:** Tayfanın donmuş, iptal edilmemiş etkinlik sayısı 3'ün altındaysa
  vitrin uç noktası istatistik döndürmez (alan yok, "henüz yok" değil;
  istemci kartı göstermez). Eşik ortam değişkeniyle değil, kodda sabittir;
  değiştirmek ürün kararıdır.
- **Metrik bazında vitrin izni:** Tür tanımındaki ölçümün `showcase`
  parametresi `false` ise o metrik hiçbir zaman vitrine çıkmaz. `sensitive:
  true` olan ölçüm (Eğitim'deki deneme neti gibi) yalnızca kişinin kendisine
  görünür: tayfa toplamına, kişisel katkı listesine, vitrine ve profil
  istatistiğine girmez; katalog doğrulaması böyle bir ölçümde
  `showcase: true`'yu reddeder.
- **Sıralama yok:** Vitrinde tayfalar arası sıralama ya da "en çok" listesi
  yoktur; Keşfet ADR'si aksini kararlaştırmadıkça.
- **Zaman örüntüsü yok:** Vitrin uçları yalnızca yıl düzeyinde toplam döner;
  ay, hafta, gün ya da saat kırılımı yalnızca tayfa içi uçlarda vardır.

### 4. Kişisel istatistik görünürlüğü

- Ayar identity'dedir: `users.stats_visibility` (`none` varsayılan /
  `friends` / `friends_and_mutual`), profil tamamlama alanlarının
  `about_visibility`'siyle aynı değerler ve aynı kitle mantığı. core,
  bugünkü `activity_visibility` gibi kopyasını `user_profiles`'ta tutar
  (ADR-0009 kopya modeli).
- Varsayılan **kapalı**dır (platform sahibi: kullanıcı isterse paylaşır).
- Kişinin kendi istatistiği her zaman kendine görünür.
- Profil ucu (`UserProfile`) `stats` alanını yalnızca görünürlük izin
  veriyorsa döner; izin yoksa alan yoktur (gizlendiği belli edilmez).

### 5. Genç kullanıcı güvenliği

- Vitrin ve profil istatistiklerinde konum, tarih ve tekrar eden zaman
  örüntüsü olmadığı için "her cumartesi şu parkta" çıkarımı yapılamaz.
- Eşik, iki-üç kişilik tayfada toplamın tek tek kişileri ele vermesini
  azaltır; ortak metriklerde "farklı katılımcı sayısı" vitrine eşik
  geçilmeden çıkmaz.
- Yaşa göre özellik kısıtı yoktur (ürün ilkesi); kişisel istatistik
  varsayılan kapalı olduğu için 16–17 yaştaki kullanıcının verisi kendi
  açmadıkça kimseye görünmez.

### 6. Rozetler (sonraki adım)

Rozetler (Faz 3) `tayfa_stats` ve `user_stats`'tan ve `celebration`
bileşeninden beslenir; kuralları ayrı ADR'de. Bu ADR'deki tablolar rozet
hesaplaması için yeterli olacak şekilde yıl ve metrik bazındadır.

## Gerekçe

- **Donmuş veri:** Vitrindeki sayı geriye dönük oynarsa güven kaybolur;
  7 günlük düzeltme penceresi zaten var.
- **Türetilmiş tablo:** Toplamı her istekte etkinliklerden hesaplamak
  Keşfet trafiğinde pahalıdır; ama tek doğruluk kaynağı bileşen tablolarıdır,
  toplam her zaman yeniden kurulabilir.
- **Kişisel görünürlük identity'de:** Profilin diğer görünürlük ayarları
  orada; kullanıcı tek ekranda yönetir, core kopyayla okur.
- **Kodda sabit eşik:** Ortam değişkeni ele geçirilen bir yapılandırmayla
  gizlilik korumasını sessizce sıfıra indirmeye izin verirdi (ADR-0011).

## Sonuçlar

- core'a iki türetilmiş tablo, bir arka plan işi ve bir vitrin alanı eklenir.
- identity'ye bir görünürlük ayarı, core'a kopyası eklenir; `UserProfile` ve
  `Account` sözleşmeleri genişler.
- Keşfet ADR'si (Faz 2) vitrin uç noktasını tanımlarken bu ADR'deki
  görünürlük kurallarını değiştiremez; değiştirecekse bunu açıkça yazar.
- Aydınlatma metnine kişisel istatistik ve vitrin eklenir (hukukçu
  listesine).

## Değerlendirilen alternatifler

- **Anlık hesaplama (tablo yok):** Basit ama Keşfet'te her vitrin
  görüntülemesi tayfanın tüm etkinliklerini taramak demek.
- **Olayla artımlı sayaç:** Hızlı ama düzeltme, silme ve tekrar gelen olayla
  kolayca bozulur; yeniden kurulabilir toplam daha güvenli.
- **Vitrinde kişisel liderlik tablosu:** Platform sahibi istemedi; genç
  kullanıcılar için kimlik açığa çıkarma riski.
