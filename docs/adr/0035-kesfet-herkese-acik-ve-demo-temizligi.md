# ADR-0035: Keşfet herkese açık, Keşfet demo verisi ve demo temizleme komutu

- **Durum:** Kabul edildi (platform sahibi, 2026-11-17, sohbette)
- **Tarih:** 2026-11-17
- **Karar veren:** Murat Arslan (ürün kararları, 2026-11-17 sohbeti; mimari
  oturumunun aktarımıyla; issue #227). Notion "Kötüye Kullanım ve Genç
  Kullanıcı Güvenliği" → "Keşfet tasarımı" → "2026-11-17 kararı". Issue'da
  açık bırakılan ayrıntılar "mimari oturumu kararı" diye işaretlidir.
- **İlgili:** [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0023](0023-demo-verisi.md),
  [ADR-0030](0030-arkadaslik-puani-ve-arkadas-onerisi.md),
  [ADR-0033](0033-kesfet-ilk-asama.md)
- **Değiştirdiği:** ADR-0033 §4 ve §5'teki "en az bir görünür arkadaş"
  şartı ve "Hukukçu listesi"ndeki 16–17 yaş hafif önlemleri; ADR-0023 §1
  ("silme yoktur") ve §5 (sahibin token'ının yalnızca `JoinByCode` için
  kullanılması). İki ADR'ye "kısmen yerini aldı" notu düşüldü.

## Bağlam

ADR-0033 Keşfet'i "arkadaş ağı" olarak açtı: bir etkinlik yalnızca
izleyicinin en az bir görünür arkadaşı varsa listelenir, katılım isteği de
aynı şartı ister. Platform sahibi 2026-11-17'de bu şartı kaldırdı: Keşfet'e
açık ve panelden onaylı tayfaların, halka açık yerde buluşan etkinliklerini
herkes görür. Arkadaş bilgisi öne çıkarma ve "Ayşe ve 2 arkadaşın" olarak
kalır.

Bunu prod'da (yayın öncesi test ortamı) denemek için içi dolu, Keşfet'e
açık demo tayfaları ve platform sahibinin hesabı etrafında arkadaşlık ve
engel gerekir. ADR-0023'teki demo aracı silme bilmez; Keşfet demo seti ise
denendikten sonra kaldırılabilmelidir.

## Karar

### 1. Keşfet herkese açık (platform sahibi)

- Listelenebilir tayfanın (ADR-0033 §3, değişmedi) uygun etkinliğini
  (`discoverable`, yüz yüze, planlı, silinmemiş, bitmemiş, 30 gün içinde
  başlıyor, etkinlik modülü açık) **herkes** görür; izleyicinin aktif,
  bekleyen, davetli ya da banlı üyeliği olan tayfalar yine dışarıdadır.
- **Arkadaş bilgisi kalır:** öğede en çok 3 görünür arkadaş ve görünür
  arkadaş sayısı. Arkadaş yoksa `friends` boş, `friend_count` 0. Görünür
  arkadaş kuralı (ADR-0033 §4: kabul edilmiş arkadaş, engel yok, aktif üye,
  `going` ya da `attended`, geçmişi `private` değil, "Keşfet'te arkadaşlarım
  görebilsin" açık) ve ayar aynen kalır; gizli arkadaş sayılmaz da.
- **Sıralama:** (1) görünür arkadaşlarla arkadaşlık puanı toplamı, azalan;
  (2) görünür arkadaş sayısı, azalan; (3) başlangıç, artan; (4) etkinlik
  kimliği. Arkadaşı olmayan öğeler yalnızca (3) ve (4) ile sıralanır (puan
  ve sayı 0 olduğu için arkadaşlı öğelerin arkasına düşerler). Puan hiçbir
  cevapta dönmez (ADR-0033 §4, değişmedi).
- **Katılım:** arkadaşı olmayan kullanıcı da Keşfet'ten istek gönderebilir.
  Şart: tayfanın izleyiciye görünen bir etkinliği olması (akışla aynı
  fonksiyon). İstek her zaman `pending` ve `joined_via = 'discover'`;
  Keşfet etiketi, güvenlik ağı, tayfa başvurusu (yönetici açar, en az 5
  üye, panel onayı), anlık tayfanın hiç çıkmaması ve `DISCOVER_ENABLED`
  aynen kalır.

### 2. Tek tanım (veritabanı, core migration `00029`)

- Yeni `discover_event_visible(izleyici, etkinlik, an)`: arkadaştan bağımsız
  bütün koşulların tek tanımı. Akış (`ListDiscoverFeed`), Keşfet'ten katılım
  (`DiscoverTayfaVisible`) ve `discover_friend_visible` bunu çağırır.
- `discover_friend_visible` aynı imzayla yeniden yazıldı: `discover_event_
  visible` + arkadaşa özgü koşullar. Kurallar sorgular arasında kaymaz
  (ADR-0033 §3'teki ilke).
- `events_discover_idx` (`starts_at`, `discoverable` ve silinmemiş): akış
  artık izleyicinin arkadaşlarından değil, bütün uygun etkinliklerden
  başlar.
- Sözleşme geriye uyumludur: proto'da alan değişmedi, yalnızca açıklama;
  `gateway.yaml`'da `DiscoverItem.friend_count` `minimum` 1'den 0'a indi,
  `friends` ve `friend_count` zorunlu kalır (`buf breaking` temiz).

### 3. Engel (mimari oturumu kararı)

ADR-0033 engeli yalnızca "görünür arkadaş" kuralında tanımlıyordu. Keşfet
yabancılara açılınca engellenen kişi, kendisini engelleyenin tayfasını
Keşfet'te bulup katılmak isteyebilir. En güvenli ve en az veri kullanan
seçenek seçildi:

- İzleyiciyle tayfanın **herhangi bir aktif üyesi** arasında (iki yönden)
  engel varsa tayfanın etkinlikleri izleyicinin Keşfet'inde görünmez ve
  izleyici o tayfaya Keşfet'ten istek gönderemez (`TAYFA_NOT_FOUND`; diğer
  "görünmüyor" nedenleriyle aynı cevap).
- Engelli/engelleyen kişi hiçbir zaman görünür arkadaş sayılmaz ve
  gösterilmez (issue'daki asgari kural; zaten böyleydi).
- Yalnızca aktif üyelik sayılır: bekleyen isteği ya da eski üyeliği olan
  kişiyle engel tayfayı gizlemez (o kişi tayfada değildir).
- Kod ya da davetle katılım bu ADR'nin kapsamında değildir; mevcut kurallar
  aynen kalır.
- **Kalan sızıntı (kabul edildi):** engellenen kişi, başka bir hesapla
  bakarak bir tayfanın kendisinden gizlendiğini, yani o tayfadaki birinin
  kendisini engellediğini çıkarabilir. Kim olduğu söylenmez; tayfada en az 5
  aktif üye vardır. Alternatifi (engellenenin engelleyenin tayfasını
  görmesi ve istek gönderebilmesi) daha zararlı bulundu.

### 4. 18 yaş altı belirtilmez (platform sahibi)

Keşfet'ten katılım isteğinde, bekleyen istekler listesinde ve onayda
yöneticiye yaş, "18 yaş altı" ya da benzeri bir işaret gösterilmez. ADR-0033
"Hukukçu listesi"ndeki "hafif önlemler" (organizatörün "18+" işareti,
gence yöneticiye uyarı) bu kararla kapandı. Katılım yolu identity'nin
`MinorService`'ine sormaz; `TestNoAgeSignalToStaff` uygulama ve gRPC
tiplerinde yaşa dair alan olmadığını ve üyelik servisinin yaş bilgisine
erişmediğini sabitler. 5651 ek m. 4 yaş doğrulama yönetmeliğiyle ilgili
hukukçu sorusu (Notion) ayrıca durur.

### 5. Keşfet demo verisi (`identity demo-seed` genişlemesi)

- ADR-0023'ün 4 kapalı tayfası ve 18 hesabı değişmez.
- **Keşfet demo seti:** 6 tayfa (adında "Demo"; yürüyüş, voleybol, kamp,
  fotoğraf yürüyüşü, balık, konvoy), her biri kendi 6 sahte hesabıyla
  (`demo-21` … `demo-56`; hiçbir hesap iki tayfada değil). Halka açık yerde
  buluşan, `discoverable`, 30 gün içinde başlayan etkinlikler, katılım
  yanıtları, birkaç duyuru. Yazma yolu ADR-0023 §4 ile aynıdır: her hesabın
  kendi kısa ömürlü token'ı, servislerin herkese açık gRPC metodları.
- **Başvuru normal yoldan:** kurucunun token'ıyla `SetTayfaDiscover`. Onayı
  platform sahibi panelden verir; araç panel onayını atlamaz, admin yetkisi
  ve admin token'ı kullanmaz. Yöneticinin isteği zaten açıksa araç yeniden
  başvurmaz (panel reddettiyse ikinci çalıştırma tekrar kuyruğa sokmaz).
  Çıktı tayfaları ve durumlarını listeler.
- **Sahip bu tayfalarda üye değildir** (Keşfet, izleyicinin üyesi olduğu
  tayfaları göstermez).
- **Arkadaşlar:** 4 sahte hesap sahibe arkadaşlık isteği gönderir; sahibin
  token'ı kabul eder. Bu hesaplar Belgrad, Moda ve Abant etkinliklerine
  `going` der ("X ve N arkadaşın").
- **Engeller:** sahip 2 hesabı (Ankara), 2 başka hesap sahibi (Bodrum)
  engeller. §3 gereği bu iki tayfa sahibin Keşfet'inde görünmez. Kapadokya'da
  ne arkadaş ne engel vardır: arkadaşsız öğe.
- **Sahibin token'ı** (ADR-0023 §5'in genişlemesi): kapalı tayfalara
  `JoinByCode`, demo arkadaşın bekleyen isteğini `AcceptFriendRequest`,
  kendi engel listesini okumak (`ListBlockedUsers`) ve henüz engellenmemiş
  demo hesabı `BlockUser`. Başka hiçbir şey; token yine 5 dakikalıktır ve
  yazılmaz. İkinci çalıştırmada yalnızca kendi engel listesi okunur.
- Profil ayarları varsayılan kalır (geçmiş `tayfa`, Keşfet'te görünür).
- **Tekrar çalıştırma kopya üretmez** (ADR-0023 §6): tayfa, etkinlik ve
  duyuru sabit `client_request_id` taşır; üyelik, Keşfet durumu,
  arkadaşlık (`ALREADY_FRIENDS`) ve engel listesi önce okunur. Bir
  arkadaşlık isteği engel yüzünden reddedilirse (`USER_NOT_FOUND`, sahip
  elle engellemiş olabilir) o hesap atlanır ve sayısı çıktıda görünür.
- Yeni servis metodu ve servis kimliği eklenmedi (ADR-0023 §4).

### 6. Demo temizleme komutu (`identity demo-purge`)

ADR-0023 §1'deki "silme yoktur" kararı bu kadarıyla değişir:

- Varsayılan: yalnızca **Keşfet demo seti** (6 tayfa, 36 hesap). `-hepsi`:
  kapalı mağaza demo tayfaları ve 18 hesabı da. `-dry-run`: hiçbir şey
  silmeden ne silineceğini sayı olarak gösterir.
- **Normal silme yolları:**
  - Tayfa, demo kurucunun kendi token'ıyla `RequestDeletion` ile silinir:
    hemen salt okunur ve Keşfet dışı, 72 saat sonra core'un işi siler.
    Arşivlenmiş ya da askıdaki tayfa kurucu tarafından silinemez; çıktıda
    "salt okunur, atlandı" yazar. Hesapları kapanıp aktif üyesi kalmazsa
    core onu kendisi silme beklemesine alır (mevcut kural).
  - Sahte hesap, hesap silme akışının kendisiyle kapatılır:
    `deletion.Erase` (kullanıcının kendi silme isteğinin adımları, giriş
    tazeliği kontrolü olmadan; sahte hesabın oturumu yoktur). Kişisel veri
    hemen silinir, `user.deleted` outbox'a yazılır; core ve chat üyelikleri,
    arkadaşlıkları, engelleri ve profil kopyalarını kaldırır. Sahibin demo
    hesaplarla arkadaşlığı ve engelleri böylece düşer; sahibin token'ı
    kullanılmaz.
  - Önce tayfalar, sonra hesaplar (kurucunun token'ı gerekir).
- **Sınır (kodda ve testle sabit):**
  - Hesap yalnızca veri setindeki bir `subject` ile, `auth_identities.provider
    = 'demo'`, `subject = demo-NN`, `email = demo-NN@demo.tayfam.app` olan
    satır üzerinden bulunur ve aynı satır, kullanıcı kilitlendikten sonra
    silme transaction'ında yeniden okunur.
  - Tayfa yalnızca veri setindeki bir demo kurucunun aktif yöneticisi olduğu,
    standart, adı o kurucunun veri setindeki adıyla birebir aynı ve "Demo"
    içeren tayfadır. Gerçek kullanıcının kurduğu (demo kurucu orada yalnızca
    üye) ya da adı farklı tayfaya dokunulmaz.
  - `TestPurgeTouchesOnlyDemoData` ve `TestPurgeBoundary`: gerçek hesap
    (sahip dahil, `demo-21` Google kimliğiyle bile), gerçek kullanıcının
    tayfası ve `-hepsi` olmadan kapalı set korunur.
- **Tekrar çalıştırılabilir:** silinen hesabın demo kimliği yoktur, kurucusu
  silinmiş tayfa bulunmaz; ikinci çalıştırma hiçbir şey yapmaz.
- **Log** yalnızca sayı yazar (tayfa, hesap); ad, kod, token yok.
- **Yetki** ADR-0023 §7 gibidir: yalnızca platform sahibi, identity
  container'ında `kubectl exec` ile çalıştırır; hiçbir otomasyon
  çalıştırmaz (`docs/runbooks/demo-verisi.md`).

## Gerekçe

- Arkadaş şartının kaldırılması ürün kararıdır; mimari, kuralları yine tek
  SQL fonksiyonunda tutar (ADR-0011: yeni bir uç kuralı atlayamaz).
- Engelde "aktif üyelerden biri" kuralı, Keşfet'in yabancılara açılmasıyla
  doğan "engellenen kişi engelleyeni bulur" yolunu kapatır ve hiçbir veri
  döndürmez.
- Temizlik normal silme yollarını kullandığı için denetim kaydı, outbox,
  profil kopyaları ve 72 saatlik bekleme gerçek kullanıcıdaki gibi işler;
  toplu SQL silmesi yoktur. Komut sahibin token'ını kullanmaz ve yalnızca
  işaretli demo kayıtlarına dokunabilir: ele geçirilse bile (identity
  container'ı zaten her kullanıcı için token imzalayabilir, ADR-0023
  Sonuçlar) yeni bir yetenek eklemez.

## Sonuçlar

- core: migration `00029`, akış ve katılım sorguları; sözleşmede açıklama ve
  `friend_count` alt sınırı. İstemci (Tayfam-App #214) arkadaşsız öğeyi
  göstermelidir.
- identity: `deletion.Erase` (davranış değişmeden ayrıldı), demo aracına
  Keşfet seti, sosyal adımlar ve `demo-purge`.
- Pilot öncesi ön koşullar (kelime listesiyle başlık filtresi, hukukçu
  görüşmesi; ADR-0033 2026-11-17 eki, issue #224) aynen durur. Keşfet artık
  yabancılara açık olduğundan başlık filtresinin önemi arttı.
- Aydınlatma metnine Keşfet maddesi (arkadaşın katılımının arkadaşlara
  gösterilmesi) ADR-0033'teki gibi pilot öncesi eklenmelidir.

## Değerlendirilen alternatifler

- **Engelde yalnızca asgari kural (görünür arkadaş sayılmaz):** engellenen
  kişi engelleyenin tayfasını Keşfet'te görüp katılmak isteyebilirdi;
  reddedildi.
- **Engeli yalnızca tayfa yöneticileriyle sınırlamak:** sıradan bir üyeyi
  engelleyen kullanıcıyı korumaz; reddedildi.
- **Temizliği SQL ile yapmak ya da yeni bir admin metodu:** outbox, denetim
  kaydı ve servis sınırlarını atlar (ADR-0023'teki gerekçe); reddedildi.
- **Hesapları kapatmadan yalnızca tayfaları silmek:** arkadaşlıklar ve
  engeller sahibin hesabında kalırdı; issue hesapların kapanmasını istiyor.
- **Purge'ün sahibin token'ıyla engel kaldırması:** hesap silme engelleri
  zaten kaldırdığı için gereksiz; sahibin token'ını hiç kullanmamak daha
  dar yetkidir.
