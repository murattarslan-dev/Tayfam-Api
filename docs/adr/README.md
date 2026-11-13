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
| [0015](0015-veritabani-rolleri.md) | Veritabanında sahip ve çalışma zamanı kullanıcısının ayrılması | Önerildi |
| [0016](0016-desteklenen-istemci-surumu.md) | Desteklenen en düşük istemci sürümü | Kabul edildi |
| [0017](0017-yalnizca-mobil-istemci.md) | Yalnızca mobil istemci; web derlemesi yalnızca prod dışında | Kabul edildi |
| [0018](0018-birebir-mesaj-e2e-sunucu-tarafi.md) | Birebir mesajda uçtan uca şifreleme: sunucu tarafı | Önerildi |
| [0019](0019-push-bildirimleri.md) | Push bildirimleri (FCM) | Kabul edildi |
| [0020](0020-goruntu-isleme-ve-medya-sunumu.md) | Görüntü işleme istemcide, sunucuda başlık denetimi; medya bağlama ve sunumu | Kabul edildi |
| [0021](0021-kvkk-veri-disa-aktarma.md) | KVKK veri dışa aktarma: toplama, şifreleme ve depolama | Kabul edildi |
| [0022](0022-uygulama-butunlugu.md) | Uygulama bütünlüğü (Play Integrity / App Attest) oturuma bağlanır | Kabul edildi |
| [0023](0023-demo-verisi.md) | Prod'da kalıcı demo tayfaları ve sahte hesaplar | Önerildi |

Ürün kararları (kavram modeli, özellikler, veri modeli, KVKK metni, platform
yönetim paneli) Notion'daki "Tayfam — Proje Planı" sayfasındadır. ADR'ler bu
kararları mimariye taşır; bir ADR Notion'daki bir kararı değiştiriyorsa bunu
açıkça belirtir.
