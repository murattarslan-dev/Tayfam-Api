# Mimari Karar Kayıtları (ADR)

Bu klasör, Tayfam API'nin mimari kararlarını ve gerekçelerini tutar. Her karar
ayrı bir dosyadır ve numarası değişmez. Kararlar silinmez; değişen bir kararın
yerine yenisi yazılır ve eskisinin durumu "Yerine geçildi: ADR-XXXX" olur.

**Durumlar:** Önerildi · Kabul edildi · Yerine geçildi · Reddedildi

| No | Başlık | Durum |
|----|--------|-------|
| [0001](0001-mikroservis-mimarisi.md) | Kubernetes üzerinde gRPC ile konuşan mikroservisler | Kabul edildi |
| [0002](0002-servis-sinirlari-ve-veri-sahipligi.md) | Servis sınırları ve veri sahipliği | Kabul edildi |
| [0003](0003-kod-yapisi-ve-sozlesmeler.md) | Kod yapısı, sözleşmeler ve veritabanı erişimi | Kabul edildi |
| [0004](0004-kimlik-dogrulama-ve-yetkilendirme.md) | Kimlik doğrulama ve yetkilendirme | Kabul edildi |
| [0005](0005-gercek-zamanli-iletisim-ve-chat.md) | Gerçek zamanlı iletişim ve chat | Kabul edildi |
| [0006](0006-dagitim-ve-ortamlar.md) | Dağıtım ve ortamlar | Kabul edildi |
| [0007](0007-dosyalar-ve-medya.md) | Dosyalar ve medya | Kabul edildi (kısmen ADR-0020 ile değişti) |
| [0008](0008-kvkk-ve-platform-yonetimi.md) | KVKK ve platform yönetimi | Kabul edildi |
| [0009](0009-veri-modelinin-servislere-dagilimi.md) | Veri modelinin servislere dağılımı | Kabul edildi |
| [0010](0010-repo-kurallari-ve-is-akisi.md) | Repo kuralları ve geliştirme iş akışı | Kabul edildi |
| [0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md) | Güvenlik ilkesi: ihlal varsayımı ve hasar sınırlama | Kabul edildi |
| [0012](0012-dogum-tarihi-ve-kotuye-kullanim.md) | Doğum tarihi, genç kullanıcı güvenliği ve kötüye kullanım tespiti | Kabul edildi |
| [0013](0013-servis-kimligi.md) | Kullanıcısız servisler arası çağrılarda servis kimliği | Kabul edildi |
| [0014](0014-websocket-erisim-tokeniyla.md) | WebSocket bağlantısı kullanıcının erişim token'ıyla | Kabul edildi |
| [0015](0015-veritabani-rolleri.md) | Veritabanında sahip ve çalışma zamanı kullanıcısının ayrılması | Kabul edildi |
| [0016](0016-desteklenen-istemci-surumu.md) | Desteklenen en düşük istemci sürümü | Kabul edildi |
| [0017](0017-yalnizca-mobil-istemci.md) | Yalnızca mobil istemci; web derlemesi yalnızca prod dışında | Kabul edildi |
| [0018](0018-birebir-mesaj-e2e-sunucu-tarafi.md) | Birebir mesajda uçtan uca şifreleme: sunucu tarafı | Kabul edildi |
| [0019](0019-push-bildirimleri.md) | Push bildirimleri (FCM) | Kabul edildi; kısmen 0036 |
| [0020](0020-goruntu-isleme-ve-medya-sunumu.md) | Görüntü işleme istemcide, sunucuda başlık denetimi; medya bağlama ve sunumu | Kabul edildi |
| [0021](0021-kvkk-veri-disa-aktarma.md) | KVKK veri dışa aktarma: toplama, şifreleme ve depolama | Kabul edildi |
| [0022](0022-uygulama-butunlugu.md) | Uygulama bütünlüğü (Play Integrity / App Attest) oturuma bağlanır | Kabul edildi |
| [0023](0023-demo-verisi.md) | Prod'da kalıcı demo tayfaları ve sahte hesaplar | Kabul edildi; kısmen 0035 |
| [0024](0024-kanal-kitlesi.md) | Kanal kitlesi: etkinlik ve seçili üye kanalları | Kabul edildi |
| [0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md) | Etkinlik türü kataloğu ve bileşen modeli (v3) | Kabul edildi; kısmen 0037, 0041, 0042, 0043, 0044 |
| [0026](0026-tayfa-istatistikleri-ve-vitrin.md) | Tayfa istatistikleri, kişisel istatistik ve Keşfet vitrini (v3) | Kabul edildi; kısmen 0037 |
| [0027](0027-tayfa-ana-sayfa-bilesenleri.md) | Tayfa ana sayfa bileşenleri ve günün içeriği (v3) | Kabul edildi |
| [0028](0028-etkinlik-fotograf-albumu.md) | Etkinlik fotoğraf albümü | Kabul edildi |
| [0029](0029-bosum-bos-vakit.md) | "Boşum": arkadaşlara görünen boş vakit (v2) | Kabul edildi |
| [0030](0030-arkadaslik-puani-ve-arkadas-onerisi.md) | Arkadaşlık puanı ve arkadaş önerisi (v2) | Kabul edildi |
| [0031](0031-sohbet-ekleri-fotograf-ve-yer.md) | Sohbet ekleri: fotoğraf ve yer (kanal ve uçtan uca birebir) | Kabul edildi |
| [0032](0032-anlik-etkinlik.md) | Anlık (bireysel) etkinlik: gizli, tek etkinlikli tayfa (v2) | Kabul edildi; kısmen 0038 |
| [0033](0033-kesfet-ilk-asama.md) | Keşfet ilk aşama: arkadaş ağı ve güvenlik ağı (v2) | Kabul edildi; kısmen 0035 |
| [0034](0034-dev-main-dal-modeli-ve-otomatik-surum.md) | dev/main dal modeli ve otomatik sürüm | Kabul edildi |
| [0035](0035-kesfet-herkese-acik-ve-demo-temizligi.md) | Keşfet herkese açık, Keşfet demo verisi ve demo temizleme komutu | Kabul edildi |
| [0036](0036-push-firebase-installation-id.md) | Push hedefi olarak Firebase Installation ID (FID) | Kabul edildi |
| [0037](0037-katalog-ayrintilari-ve-sema-ekleri.md) | Katalog ayrıntıları ve şema ekleri (v3) | Kabul edildi; kısmen 0044 |
| [0038](0038-simdi-baslat.md) | Şimdi başlat: doğaçlama etkinlik, QR ile katılım ve şimdi bildirimi (v3) | Kabul edildi |
| [0039](0039-rozetler.md) | Rozetler (v3) | Kabul edildi |
| [0040](0040-tayfa-modulleri-v3.md) | v3 tayfa modülleri: ortak kütüphane, soru havuzu, materyal kütüphanesi, üye garajı, ders-konu listesi | Kabul edildi |
| [0041](0041-v3-tek-seferde-gecis-ve-yayin.md) | v3'e tek seferde geçiş ve yayın | Kabul edildi |
| [0042](0042-katalog-degisikligi-olayi.md) | Katalog değişikliği olayı (`catalog.changed`) ve identity'deki kopya | Kabul edildi |
| [0043](0043-ders-konu-alan-turu.md) | Ders/konu listesine bağlı tür alanı (`study_tags`) | Kabul edildi |
| [0044](0044-yazboz-esli-kip.md) | Yazbozda eşli kip (`side: pair`) | Kabul edildi |
| [0045](0045-apple-ile-giris.md) | Apple ile giriş (yalnızca iOS) ve hesap silmede token iptali | Kabul edildi |
| [0046](0046-pilot-oncesi-sifirlama.md) | Pilot öncesi prod sıfırlama (bir kerelik, servis başına SQL betiği) | Kabul edildi |

Ürün kararları (kavram modeli, özellikler, KVKK metni, platform yönetim
paneli) claude.ai "Tayfam" projesinin `tayfam/` dokümanlarındadır (2026-11-26'ya
kadar Notion'daydı; ADR'lerdeki Notion atıfları tarihçedir). ADR'ler bu
kararları mimariye taşır; bir ADR bir ürün kararını değiştiriyorsa bunu
açıkça belirtir.
