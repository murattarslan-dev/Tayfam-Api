# ADR-0023: Prod'da kalıcı demo tayfaları ve sahte hesaplar

- **Durum:** Önerildi
- **Tarih:** 2026-11-13
- **Karar veren:** Murat Tarslan (kararlar sohbette verildi, 2026-11-13;
  issue #179)
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0013](0013-servis-kimligi.md),
  [ADR-0015](0015-veritabani-rolleri.md)

## Bağlam

Mağaza incelemesi, ekran görüntüleri ve tanıtımlar için prod'da içi dolu
tayfalar gerekir (Notion "Mağaza Sayfası": inceleme ekibi için demo tayfa ve
tayfa kodu). Pilot öncesi prod bir oyun alanıdır; gerçek kullanıcı verisi
henüz yoktur, ama olacaktır. Demo verisi gerçek verinin yanında durur ve
onunla karışmamalıdır.

Veriyi veritabanına elle SQL ile basmak izinleri, denetim kaydı trigger'larını
(`app.actor_id`), doğum tarihi ve kanal mesajı şifrelemesini, outbox olaylarını
ve servislerdeki profil kopyalarını atlar; servis sınırını da deler (ADR-0002).

## Karar

1. **Demo verisi prod'da kalır; tazeleme ve silme yoktur.** Veri bir kez
   yazılır. Etkinlikler eskir; sessizleşen tayfaları mevcut pasif tayfa
   temizliği (30 gün → uyarı → arşiv) kendiliğinden arşivler. Silme veya
   yeniden yazma komutu yoktur.
2. **İşaretleme, şema değişikliği en az olacak biçimde:**
   - Tayfaların adında **"Demo"** geçer; hepsi kapalıdır (`private`, katılım
     onaylı). core'da şema değişikliği yoktur.
   - Sahte hesapların giriş kimliği `auth_identities.provider = 'demo'`,
     `subject = demo-NN`, `email = demo-NN@demo.tayfam.app`'tir. identity
     migration `00011` sağlayıcı kısıtına `demo`'yu ekler ve demo satırının
     bu biçimde olmasını kısıtla zorlar. Panel sayıları bu sağlayıcı (ya da
     e-posta alan adı) ile ayrılabilir. Notion veri modelindeki
     `auth_identities.provider (google|apple)` bu ADR ile `google|apple|demo`
     olur.
3. **Sahte hesaplar giriş yapamaz.** Google/Apple kimlikleri yoktur;
   `session.SignIn` yalnızca `google` ve `apple` doğrulayıcılarını kabul
   eder, `demo` hiçbir giriş yolunda eşleşmez. Hepsi yetişkindir; doğum
   tarihi kayıttaki gibi şifrelidir. Onay kaydı (consent) yazılmaz: metinleri
   onaylayan bir kişi yoktur. Adlar gerçek kişileri taklit etmeyen sıradan
   adlardır (ad + soyadı baş harfi). Birebir mesaj (E2E, cihaz yok) ve
   fotoğraf yoktur.
4. **Yazma yolu servislerin kendisidir.** Araç identity binary'sinin alt
   komutudur (`identity demo-seed`, `internal/identity/app/demo`):
   - Hesapları identity kendi veritabanında, kayıt akışının yaptığı gibi
     açar: şifreli doğum tarihi, referans kodu, `user.profile_updated`
     outbox olayı (core ve chat profil kopyalarını relay'den alır).
   - core ve chat'teki her şey servislerin herkese açık gRPC metodlarıyla,
     her sahte hesabın **kendi** kısa ömürlü (5 dakika) erişim token'ıyla
     yazılır; istemcinin gateway üzerinden izlediği yolun aynısıdır. İzin
     tablosu, trigger'lar, şifreleme ve olaylar servislerin kendi kodunda
     çalışır. Yeni servis metodu, servis kimliği (ADR-0013) veya izinli
     çağıran eklenmez; araç core ve chat veritabanlarına bağlanmaz.
   - Token'lar oturum satırı olmadan, çalıştırma başına rastgele bir oturum
     kimliğiyle imzalanır; yenilenemez ve hiçbir yere yazılmaz.
5. **Platform sahibinin hesabı referans koduyla verilir** (çalıştırma anında
   bayrakla; repoda, örneklerde ve log'da kod ya da e-posta yoktur). Sahibin
   hesabıyla yalnızca tayfa başına bir `JoinByCode` çağrılır; onayı ve rol
   değişikliğini tayfanın sahte kurucusu yapar. Sahip içeriğin sonunda
   eklenir: demo içeriği için bildirim almaz. Rolleri: bir tayfada yönetici,
   birinde moderatör, ikisinde üye.
6. **Tekrar çalıştırmak kopya üretmez.** Hesaplar demo `subject`'iyle
   bulunur; her oluşturma isteği kaydın anahtarından türetilen sabit bir
   `client_request_id` (UUIDv8, SHA-256) taşır ve servis ilk kaydı döndürür;
   üyelik, kanal ve kamp malzemesi eklenmeden önce okunur. Var olan kayıt
   yeniden yazılmaz. Arşivlenmiş ya da askıdaki tayfa atlanır.
7. **Çalıştırma yetkisi platform sahibindedir.** Hiçbir otomasyon (Flux, CI,
   Job) aracı çalıştırmaz. Platform sahibi, çalışan identity pod'unda
   `kubectl exec` ile bir kez çalıştırır (`docs/runbooks/demo-verisi.md`).
   Böylece yeni manifest, ServiceAccount, NetworkPolicy veya secret
   bağlaması gerekmez: araç identity'nin zaten sahip olduğu yapılandırma,
   `identity_app` veritabanı kullanıcısı ve ağ izinleriyle çalışır.

## Gerekçe

- Servislerin kendi yolundan yazmak, demo verisinin gerçek veriyle aynı
  kurallara uymasını garanti eder; elle SQL'in atlayacağı denetim kaydı,
  şifreleme ve olaylar eksiksiz oluşur.
- Araç identity'nin içindedir çünkü token imzalama anahtarı ve doğum tarihi
  anahtarı yalnızca identity'dedir; başka bir bileşene bu anahtarları vermek
  hasar alanını genişletirdi (ADR-0011). Kullanıcı token'ı her serviste
  yeniden doğrulanır; araç servislerden fazlasını yapamaz.
- `kubectl exec` yalnızca cluster'a erişimi olan platform sahibinin
  yapabileceği bir iştir; ayrı bir Job tanımı Flux'ın yanlışlıkla
  çalıştırabileceği ve kalıcı yetki isteyen bir nesne olurdu.
- Silme komutu olmaması, prod'da toplu silme yapabilen bir araç
  bulundurmamak demektir.

## Sonuçlar

- identity'nin imzalama anahtarını tutan süreç zaten her kullanıcı için token
  imzalayabilir; bu araç yeni bir yetenek eklemez, ama bu yeteneği bir
  komutla kullanır. Komut yalnızca `identity` container'ı içinde ve
  platform sahibinin kubectl erişimiyle çalışır.
- Panel sayıları (toplam kullanıcı, tayfa) demo kayıtlarını içerir; ayırmak
  gerekirse sorgu `auth_identities.provider = 'demo'` ve tayfa adındaki
  "Demo" ile ayırır. Panelde ayrı sayaç bu ADR'nin kapsamında değildir.
- Demo içeriği gerçek kullanıcıların arama veya keşif sonuçlarında görünmez:
  tayfalar kapalıdır, etkinlikler keşfedilebilir değildir.
- Sahte hesaplar KVKK silme ve dışa aktarma akışlarına giremez (giriş
  yoktur). Platform yöneticisi panelden askıya alabilir veya yasaklayabilir.
- Veri seti `internal/identity/app/demo/data.go`'dadır; yeni bir kayıt
  eklenip araç yeniden çalıştırılırsa yalnızca yeni kayıt yazılır.

## Değerlendirilen alternatifler

- **Veritabanına SQL betiği:** En kısa yol, ama izinleri, trigger'ları,
  şifrelemeyi, outbox'ı ve profil kopyalarını atlar; servis sınırını deler.
  Reddedildi (issue #179).
- **Ayrı bir binary ile üç servisin `app` paketlerini doğrudan çağırmak:**
  Üç veritabanına birden bağlanır ve üç servisin anahtarlarını tek bir
  sürece verir; depguard sınırlarını aşar. Reddedildi.
- **Yeni servis metodları ve demo için ServiceAccount (ADR-0013):** Prod'da
  kalıcı, yalnızca bir kez kullanılacak yazma metodları ve izinli çağıran
  listesi açar. Reddedildi.
- **Kubernetes Job:** Ek ServiceAccount, NetworkPolicy ve secret
  bağlamaları ister; aynı işi exec ile identity container'ı yapabilir.
  Gerekirse ileride Job'a taşınabilir.
- **`demo` yerine Google sağlayıcısıyla sahte `subject`:** Şema değişikliği
  gerektirmez, ama hesapları "Google kimliği var" gibi gösterir ve işareti
  belirsizleştirir. Reddedildi.
- **`tayfam-ops` alt komutu:** tayfam-ops platform sahibinin bilgisayarında
  çalışan, hiçbir servise ve veritabanına bağlanmayan yerel araçtır
  (depguard); prod cluster'ındaki servislere erişmesi bu sınırı bozar.
